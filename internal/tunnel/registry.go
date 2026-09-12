package tunnel

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrAdmissionClosed reports that the registry no longer accepts tunnels.
var ErrAdmissionClosed = errors.New("tunnel admission closed")

// Observer receives bounded tunnel lifecycle telemetry.
type Observer interface {
	// TunnelOpened records one transition into active ownership.
	TunnelOpened()
	// TunnelBytes records transferred bytes in one bounded direction.
	TunnelBytes(Direction, uint64)
	// TunnelClosed records one active session's terminal result.
	TunnelClosed(Result)
}

// Stats contains aggregate registry ownership counts.
type Stats struct {
	// Pending is the number of registered but not yet activated tunnels.
	Pending uint64
	// Active is the number of asynchronously running tunnels.
	Active uint64
}

// Registry transactionally owns pending and active tunnel sessions.
type Registry struct {
	observer Observer

	mu              sync.Mutex
	admissionClosed bool
	nextID          uint64
	registrations   map[uint64]*Registration
	wait            sync.WaitGroup
}

type registrationState uint8

const (
	registrationPending registrationState = iota
	registrationActive
	registrationDone
)

// Registration controls one pending-to-active ownership transaction.
type Registration struct {
	registry *Registry
	id       uint64
	session  *Session
	release  func()
	state    registrationState
	cleanup  sync.Once
}

// NewRegistry returns an open tunnel registry using observer when non-nil.
func NewRegistry(observer Observer) *Registry {
	return &Registry{observer: observer, registrations: make(map[uint64]*Registration)}
}

// Register adds session as pending ownership while admission remains open.
func (r *Registry) Register(session *Session, release func()) (*Registration, error) {
	if r == nil {
		return nil, fmt.Errorf("tunnel registry is required")
	}
	if session == nil {
		return nil, fmt.Errorf("tunnel session is required")
	}
	if release == nil {
		release = func() {}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.admissionClosed {
		return nil, ErrAdmissionClosed
	}
	r.nextID++
	registration := &Registration{
		registry: r,
		id:       r.nextID,
		session:  session,
		release:  release,
		state:    registrationPending,
	}
	r.registrations[registration.id] = registration
	r.wait.Add(1)
	return registration, nil
}

// CloseAdmission idempotently rejects future registrations.
func (r *Registry) CloseAdmission() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.admissionClosed = true
	r.mu.Unlock()
}

// Drain closes admission and waits for registered tunnels. If ctx expires,
// pending registrations are rolled back and active sessions are force-closed
// before Drain waits for cleanup and returns the context error.
func (r *Registry) Drain(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.CloseAdmission()
	drained := make(chan struct{})
	go func() {
		r.wait.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
	}

	r.mu.Lock()
	pending := make([]*Registration, 0)
	active := make([]*Session, 0)
	for id, registration := range r.registrations {
		switch registration.state {
		case registrationPending:
			registration.state = registrationDone
			delete(r.registrations, id)
			pending = append(pending, registration)
		case registrationActive:
			active = append(active, registration.session)
		}
	}
	r.mu.Unlock()
	for _, registration := range pending {
		registration.finish(false, Result{})
	}
	for _, session := range active {
		session.ForceClose(ReasonShutdown)
	}
	<-drained
	return ctx.Err()
}

// Stats returns a consistent aggregate ownership snapshot.
func (r *Registry) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var stats Stats
	for _, registration := range r.registrations {
		switch registration.state {
		case registrationPending:
			stats.Pending++
		case registrationActive:
			stats.Active++
		}
	}
	return stats
}

// Activate commits a pending registration and starts its Session
// asynchronously. It is safe to call after admission closes.
func (r *Registration) Activate(ctx context.Context) {
	if r == nil || r.registry == nil {
		return
	}
	registry := r.registry
	registry.mu.Lock()
	if r.state != registrationPending {
		registry.mu.Unlock()
		return
	}
	r.state = registrationActive
	registry.mu.Unlock()
	registry.observe(func(observer Observer) { observer.TunnelOpened() })
	go func() {
		result := r.session.Run(ctx)
		registry.mu.Lock()
		if r.state != registrationActive {
			registry.mu.Unlock()
			return
		}
		r.state = registrationDone
		delete(registry.registrations, r.id)
		registry.mu.Unlock()
		r.finish(true, result)
	}()
}

// Rollback releases a pending registration without running its Session.
func (r *Registration) Rollback() {
	if r == nil || r.registry == nil {
		return
	}
	registry := r.registry
	registry.mu.Lock()
	if r.state != registrationPending {
		registry.mu.Unlock()
		return
	}
	r.state = registrationDone
	delete(registry.registrations, r.id)
	registry.mu.Unlock()
	r.finish(false, Result{})
}

func (r *Registration) finish(active bool, result Result) {
	r.cleanup.Do(func() {
		if active {
			r.registry.observe(func(observer Observer) {
				observer.TunnelBytes(DirectionDownstreamToUpstream, result.DownstreamToUpstream)
			})
			r.registry.observe(func(observer Observer) {
				observer.TunnelBytes(DirectionUpstreamToDownstream, result.UpstreamToDownstream)
			})
			r.registry.observe(func(observer Observer) { observer.TunnelClosed(result) })
		}
		safeCallback(r.release)
		r.registry.wait.Done()
	})
}

func (r *Registry) observe(callback func(Observer)) {
	if r.observer == nil {
		return
	}
	safeCallback(func() { callback(r.observer) })
}

func safeCallback(callback func()) {
	defer func() { _ = recover() }()
	callback()
}
