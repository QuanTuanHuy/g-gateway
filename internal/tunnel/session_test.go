package tunnel

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionCopiesOpaqueBytesBidirectionally(t *testing.T) {
	session, downstream, upstream := pipeSession(t, 0)
	result := make(chan Result, 1)
	go func() { result <- session.Run(context.Background()) }()

	writeAndRead(t, downstream, upstream, []byte("client-to-upstream"))
	writeAndRead(t, upstream, downstream, []byte{0, 1, 2, 0xff})
	_ = downstream.Close()
	_ = upstream.Close()

	got := waitResult(t, result)
	if got.DownstreamToUpstream != uint64(len("client-to-upstream")) || got.UpstreamToDownstream != 4 {
		t.Fatalf("result byte counts = %+v", got)
	}
	if got.Reason != ReasonClientEOF && got.Reason != ReasonUpstreamEOF {
		t.Fatalf("result reason = %q", got.Reason)
	}
}

func TestSessionActivityInEitherDirectionResetsIdleTimeout(t *testing.T) {
	session, downstream, upstream := pipeSession(t, 50*time.Millisecond)
	result := make(chan Result, 1)
	go func() { result <- session.Run(context.Background()) }()

	for index := 0; index < 4; index++ {
		if index%2 == 0 {
			writeAndRead(t, downstream, upstream, []byte{byte(index)})
		} else {
			writeAndRead(t, upstream, downstream, []byte{byte(index)})
		}
		time.Sleep(30 * time.Millisecond)
		select {
		case got := <-result:
			t.Fatalf("session expired despite activity: %+v", got)
		default:
		}
	}

	got := waitResult(t, result)
	if got.Reason != ReasonIdleTimeout {
		t.Fatalf("result reason = %q, want %q", got.Reason, ReasonIdleTimeout)
	}
	_ = downstream.Close()
	_ = upstream.Close()
}

func TestSessionRecordsActivityWhenNotificationIsAlreadyPending(t *testing.T) {
	session, downstream, upstream := pipeSession(t, time.Second)
	defer downstream.Close()
	defer upstream.Close()
	session.activity <- struct{}{}
	session.signalActivity()
	first := session.lastActivity.Load()
	time.Sleep(time.Millisecond)
	session.signalActivity()
	if second := session.lastActivity.Load(); second <= first {
		t.Fatalf("last activity did not advance: first=%d second=%d", first, second)
	}
}

func TestSessionZeroIdleTimeoutNeverExpires(t *testing.T) {
	session, downstream, upstream := pipeSession(t, 0)
	result := make(chan Result, 1)
	go func() { result <- session.Run(context.Background()) }()

	time.Sleep(80 * time.Millisecond)
	select {
	case got := <-result:
		t.Fatalf("zero-timeout session stopped: %+v", got)
	default:
	}
	session.ForceClose(ReasonShutdown)
	if got := waitResult(t, result); got.Reason != ReasonShutdown {
		t.Fatalf("result reason = %q, want %q", got.Reason, ReasonShutdown)
	}
	_ = downstream.Close()
	_ = upstream.Close()
}

func TestSessionEOFPropagatesCloseWrite(t *testing.T) {
	var downstreamCloseWrite, upstreamCloseWrite atomic.Int32
	session, err := NewSession(
		Endpoint{
			Reader:      strings.NewReader(""),
			Writer:      io.Discard,
			Closer:      closeFunc(func() error { return nil }),
			CloseWriter: func() error { downstreamCloseWrite.Add(1); return nil },
		},
		Endpoint{
			Reader:      strings.NewReader(""),
			Writer:      io.Discard,
			Closer:      closeFunc(func() error { return nil }),
			CloseWriter: func() error { upstreamCloseWrite.Add(1); return nil },
		},
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	session.Run(context.Background())
	if downstreamCloseWrite.Load() != 1 || upstreamCloseWrite.Load() != 1 {
		t.Fatalf("CloseWrite calls downstream=%d upstream=%d", downstreamCloseWrite.Load(), upstreamCloseWrite.Load())
	}
}

func TestSessionConcurrentForceCloseIsIdempotent(t *testing.T) {
	session, downstream, upstream := pipeSession(t, time.Hour)
	result := make(chan Result, 1)
	go func() { result <- session.Run(context.Background()) }()

	var callers sync.WaitGroup
	for index := 0; index < 16; index++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			session.ForceClose(ReasonShutdown)
		}()
	}
	callers.Wait()
	if got := waitResult(t, result); got.Reason != ReasonShutdown {
		t.Fatalf("result reason = %q, want %q", got.Reason, ReasonShutdown)
	}
	_ = downstream.Close()
	_ = upstream.Close()
}

func TestSessionPreservesFirstCausalStreamReason(t *testing.T) {
	firstEOF := new(Session)
	firstEOF.setReason(ReasonClientEOF)
	firstEOF.setReason(ReasonIOError)
	if got := firstEOF.currentReason(); got != ReasonClientEOF {
		t.Fatalf("EOF then error reason=%q", got)
	}
	firstError := new(Session)
	firstError.setReason(ReasonIOError)
	firstError.setReason(ReasonUpstreamEOF)
	if got := firstError.currentReason(); got != ReasonIOError {
		t.Fatalf("error then EOF reason=%q", got)
	}
	firstError.setReason(ReasonShutdown)
	if got := firstError.currentReason(); got != ReasonShutdown {
		t.Fatalf("controller reason=%q", got)
	}
}

func TestSessionRejectsInvalidEndpointsAndTimeout(t *testing.T) {
	valid := Endpoint{Reader: strings.NewReader(""), Writer: io.Discard, Closer: closeFunc(func() error { return nil })}
	if _, err := NewSession(Endpoint{}, valid, 0); err == nil {
		t.Fatal("NewSession() accepted incomplete downstream endpoint")
	}
	if _, err := NewSession(valid, valid, -time.Second); err == nil {
		t.Fatal("NewSession() accepted negative idle timeout")
	}
}

type closeFunc func() error

func (function closeFunc) Close() error { return function() }

func pipeSession(t *testing.T, idleTimeout time.Duration) (*Session, net.Conn, net.Conn) {
	t.Helper()
	downstreamPeer, downstreamTunnel := net.Pipe()
	upstreamTunnel, upstreamPeer := net.Pipe()
	session, err := NewSession(
		Endpoint{Reader: downstreamTunnel, Writer: downstreamTunnel, Closer: downstreamTunnel},
		Endpoint{Reader: upstreamTunnel, Writer: upstreamTunnel, Closer: upstreamTunnel},
		idleTimeout,
	)
	if err != nil {
		t.Fatal(err)
	}
	return session, downstreamPeer, upstreamPeer
}

func writeAndRead(t *testing.T, writer net.Conn, reader net.Conn, payload []byte) {
	t.Helper()
	written := make(chan error, 1)
	go func() {
		_, err := writer.Write(payload)
		written <- err
	}()
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != string(payload) {
		t.Fatalf("copied payload = %v, want %v", buffer, payload)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

func waitResult(t *testing.T, result <-chan Result) Result {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session result")
		return Result{}
	}
}
