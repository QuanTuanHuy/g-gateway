package tunnel

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistryRollbackReleasesPendingRegistrationOnce(t *testing.T) {
	registry := NewRegistry(nil)
	session, downstream, upstream := pipeSession(t, 0)
	defer downstream.Close()
	defer upstream.Close()
	var releases atomic.Int32
	registration, err := registry.Register(session, func() { releases.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.Stats(); got != (Stats{Pending: 1}) {
		t.Fatalf("Stats() = %+v", got)
	}
	registration.Rollback()
	registration.Rollback()
	registration.Activate(context.Background())
	if releases.Load() != 1 || registry.Stats() != (Stats{}) {
		t.Fatalf("release count=%d stats=%+v", releases.Load(), registry.Stats())
	}
}

func TestRegistryCloseAdmissionRejectsNewRegistration(t *testing.T) {
	registry := NewRegistry(nil)
	registry.CloseAdmission()
	session, downstream, upstream := pipeSession(t, 0)
	defer downstream.Close()
	defer upstream.Close()
	if _, err := registry.Register(session, func() {}); !errors.Is(err, ErrAdmissionClosed) {
		t.Fatalf("Register() error = %v", err)
	}
}

func TestRegistryRegisteredTunnelActivatesAfterAdmissionCloses(t *testing.T) {
	observer := newRecordingObserver()
	registry := NewRegistry(observer)
	session, downstream, upstream := pipeSession(t, 0)
	var releases atomic.Int32
	registration, err := registry.Register(session, func() { releases.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	registry.CloseAdmission()
	registration.Activate(context.Background())
	_ = downstream.Close()
	_ = upstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := registry.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if releases.Load() != 1 || observer.opened.Load() != 1 || observer.closed.Load() != 1 {
		t.Fatalf("release=%d opened=%d closed=%d", releases.Load(), observer.opened.Load(), observer.closed.Load())
	}
	if registry.Stats() != (Stats{}) {
		t.Fatalf("final stats=%+v", registry.Stats())
	}
}

func TestRegistryCloseAdmissionRacingRegisterLeavesNoRegistrations(t *testing.T) {
	registry := NewRegistry(nil)
	var releases atomic.Int32
	var callers sync.WaitGroup
	for index := 0; index < 100; index++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			session, downstream, upstream := pipeSession(t, 0)
			defer downstream.Close()
			defer upstream.Close()
			registration, err := registry.Register(session, func() { releases.Add(1) })
			if err == nil {
				registration.Rollback()
				return
			}
			if !errors.Is(err, ErrAdmissionClosed) {
				t.Errorf("Register() error = %v", err)
			}
		}()
	}
	registry.CloseAdmission()
	callers.Wait()
	if registry.Stats() != (Stats{}) {
		t.Fatalf("final stats=%+v", registry.Stats())
	}
}

func TestRegistryDrainWaitsForNaturalCompletion(t *testing.T) {
	registry := NewRegistry(nil)
	session, downstream, upstream := pipeSession(t, 0)
	registration, err := registry.Register(session, func() {})
	if err != nil {
		t.Fatal(err)
	}
	registration.Activate(context.Background())
	drained := make(chan error, 1)
	go func() { drained <- registry.Drain(context.Background()) }()
	select {
	case err := <-drained:
		t.Fatalf("Drain() returned before tunnel completion: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	_ = downstream.Close()
	_ = upstream.Close()
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if registry.Stats() != (Stats{}) {
		t.Fatalf("final stats=%+v", registry.Stats())
	}
}

func TestRegistryDrainDeadlineRollsBackPendingAndForceClosesActive(t *testing.T) {
	observer := newRecordingObserver()
	registry := NewRegistry(observer)
	active, activeDownstream, activeUpstream := pipeSession(t, 0)
	pending, pendingDownstream, pendingUpstream := pipeSession(t, 0)
	defer activeDownstream.Close()
	defer activeUpstream.Close()
	defer pendingDownstream.Close()
	defer pendingUpstream.Close()
	var releases atomic.Int32
	activeRegistration, err := registry.Register(active, func() { releases.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(pending, func() { releases.Add(1) }); err != nil {
		t.Fatal(err)
	}
	activeRegistration.Activate(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := registry.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain() error = %v", err)
	}
	if releases.Load() != 2 || registry.Stats() != (Stats{}) {
		t.Fatalf("release=%d stats=%+v", releases.Load(), registry.Stats())
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.last.Reason != ReasonShutdown {
		t.Fatalf("active close reason = %q", observer.last.Reason)
	}
}

type recordingObserver struct {
	opened atomic.Int32
	closed atomic.Int32
	bytes  [2]atomic.Uint64
	mu     sync.Mutex
	last   Result
}

func newRecordingObserver() *recordingObserver { return new(recordingObserver) }

func (observer *recordingObserver) TunnelOpened() { observer.opened.Add(1) }

func (observer *recordingObserver) TunnelBytes(direction Direction, count uint64) {
	observer.bytes[direction].Add(count)
}

func (observer *recordingObserver) TunnelClosed(result Result) {
	observer.closed.Add(1)
	observer.mu.Lock()
	observer.last = result
	observer.mu.Unlock()
}
