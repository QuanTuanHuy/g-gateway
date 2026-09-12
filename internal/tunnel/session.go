package tunnel

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const copyBufferSize = 32 << 10

var copyBufferPool = sync.Pool{New: func() any {
	buffer := make([]byte, copyBufferSize)
	return &buffer
}}

// Direction identifies one direction of opaque tunnel traffic.
type Direction uint8

const (
	// DirectionDownstreamToUpstream identifies bytes sent by the client.
	DirectionDownstreamToUpstream Direction = iota
	// DirectionUpstreamToDownstream identifies bytes sent by the upstream.
	DirectionUpstreamToDownstream
)

// CloseReason is a bounded terminal classification for a tunnel session.
type CloseReason string

const (
	// ReasonClientEOF means the downstream stream ended naturally.
	ReasonClientEOF CloseReason = "client_eof"
	// ReasonUpstreamEOF means the upstream stream ended naturally.
	ReasonUpstreamEOF CloseReason = "upstream_eof"
	// ReasonIdleTimeout means no traffic occurred before the idle deadline.
	ReasonIdleTimeout CloseReason = "idle_timeout"
	// ReasonShutdown means gateway shutdown or context cancellation closed the session.
	ReasonShutdown CloseReason = "shutdown"
	// ReasonIOError means an opaque stream read or write failed.
	ReasonIOError CloseReason = "io_error"
)

// Endpoint describes one side of an opaque bidirectional stream.
type Endpoint struct {
	// Reader supplies bytes received from this endpoint.
	Reader io.Reader
	// Writer sends bytes to this endpoint.
	Writer io.Writer
	// Closer unblocks outstanding I/O and releases the endpoint.
	Closer io.Closer
	// CloseWriter best-effort propagates a half-close when supported.
	CloseWriter func() error
}

// Result contains bounded terminal session telemetry.
type Result struct {
	// Reason identifies why the session ended.
	Reason CloseReason
	// Duration is the elapsed Run lifetime.
	Duration time.Duration
	// DownstreamToUpstream is the number of bytes written upstream.
	DownstreamToUpstream uint64
	// UpstreamToDownstream is the number of bytes written downstream.
	UpstreamToDownstream uint64
}

// Session copies bytes between two endpoints until both directions drain or a
// controller closes the streams.
type Session struct {
	downstream Endpoint
	upstream   Endpoint
	idle       time.Duration

	started atomic.Bool
	down    atomic.Uint64
	up      atomic.Uint64

	activity  chan struct{}
	closeOnce sync.Once
	reasonMu  sync.Mutex
	reason    CloseReason
}

type copyOutcome struct {
	direction Direction
	eof       bool
}

// NewSession validates endpoints and returns an unstarted tunnel session.
func NewSession(downstream, upstream Endpoint, idleTimeout time.Duration) (*Session, error) {
	if err := validateEndpoint("downstream", downstream); err != nil {
		return nil, err
	}
	if err := validateEndpoint("upstream", upstream); err != nil {
		return nil, err
	}
	if idleTimeout < 0 {
		return nil, fmt.Errorf("idle timeout must be non-negative")
	}
	return &Session{
		downstream: downstream,
		upstream:   upstream,
		idle:       idleTimeout,
		activity:   make(chan struct{}, 1),
	}, nil
}

// Run owns the copy loops until the session terminates and returns its bounded
// result. A Session may be run once.
func (s *Session) Run(ctx context.Context) Result {
	startedAt := time.Now()
	if s == nil || !s.started.CompareAndSwap(false, true) {
		return Result{Reason: ReasonIOError, Duration: time.Since(startedAt)}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	outcomes := make(chan copyOutcome, 2)
	go s.copy(DirectionDownstreamToUpstream, s.downstream.Reader, s.upstream.Writer, s.upstream.CloseWriter, &s.down, outcomes)
	go s.copy(DirectionUpstreamToDownstream, s.upstream.Reader, s.downstream.Writer, s.downstream.CloseWriter, &s.up, outcomes)

	var timer *time.Timer
	var idle <-chan time.Time
	if s.idle > 0 {
		timer = time.NewTimer(s.idle)
		idle = timer.C
	}
	contextDone := ctx.Done()
	for completed := 0; completed < 2; {
		select {
		case outcome := <-outcomes:
			completed++
			if outcome.eof {
				if outcome.direction == DirectionDownstreamToUpstream {
					s.setReason(ReasonClientEOF)
				} else {
					s.setReason(ReasonUpstreamEOF)
				}
			} else {
				s.setReason(ReasonIOError)
				s.closeEndpoints()
			}
		case <-s.activity:
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(s.idle)
			}
		case <-idle:
			s.setReason(ReasonIdleTimeout)
			s.closeEndpoints()
			idle = nil
		case <-contextDone:
			s.setReason(ReasonShutdown)
			s.closeEndpoints()
			contextDone = nil
		}
	}
	if timer != nil {
		timer.Stop()
	}
	s.closeEndpoints()
	reason := s.currentReason()
	if reason == "" {
		reason = ReasonIOError
	}
	return Result{
		Reason:               reason,
		Duration:             time.Since(startedAt),
		DownstreamToUpstream: s.down.Load(),
		UpstreamToDownstream: s.up.Load(),
	}
}

// ForceClose idempotently records reason and closes both streams to unblock
// pending copy operations.
func (s *Session) ForceClose(reason CloseReason) {
	if s == nil {
		return
	}
	s.setReason(reason)
	s.closeEndpoints()
}

func (s *Session) copy(
	direction Direction,
	reader io.Reader,
	writer io.Writer,
	closeWriter func() error,
	count *atomic.Uint64,
	outcomes chan<- copyOutcome,
) {
	bufferPointer := copyBufferPool.Get().(*[]byte)
	buffer := *bufferPointer
	defer func() {
		copyBufferPool.Put(bufferPointer)
	}()
	for {
		read, readErr := reader.Read(buffer)
		if read > 0 {
			s.signalActivity()
			written := 0
			for written < read {
				countNow, writeErr := writer.Write(buffer[written:read])
				if countNow > 0 {
					written += countNow
					count.Add(uint64(countNow))
					s.signalActivity()
				}
				if writeErr != nil || countNow == 0 {
					outcomes <- copyOutcome{direction: direction}
					return
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				if closeWriter != nil {
					_ = closeWriter()
				}
				outcomes <- copyOutcome{direction: direction, eof: true}
			} else {
				outcomes <- copyOutcome{direction: direction}
			}
			return
		}
	}
}

func (s *Session) signalActivity() {
	select {
	case s.activity <- struct{}{}:
	default:
	}
}

func (s *Session) closeEndpoints() {
	s.closeOnce.Do(func() {
		_ = s.downstream.Closer.Close()
		_ = s.upstream.Closer.Close()
	})
}

func (s *Session) setReason(reason CloseReason) {
	s.reasonMu.Lock()
	if reasonPriority(reason) > reasonPriority(s.reason) {
		s.reason = reason
	}
	s.reasonMu.Unlock()
}

func (s *Session) currentReason() CloseReason {
	s.reasonMu.Lock()
	defer s.reasonMu.Unlock()
	return s.reason
}

func reasonPriority(reason CloseReason) int {
	switch reason {
	case ReasonShutdown:
		return 5
	case ReasonIdleTimeout:
		return 4
	case ReasonIOError:
		return 3
	case ReasonClientEOF, ReasonUpstreamEOF:
		return 2
	default:
		return 0
	}
}

func validateEndpoint(name string, endpoint Endpoint) error {
	if endpoint.Reader == nil || endpoint.Writer == nil || endpoint.Closer == nil {
		return fmt.Errorf("%s endpoint requires reader, writer, and closer", name)
	}
	return nil
}
