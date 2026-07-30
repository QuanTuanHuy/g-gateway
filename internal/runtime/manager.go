package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

// Observer receives synchronous bounded snapshot lifecycle events. Manager
// operations isolate observer panics.
type Observer interface {
	// SnapshotApplied reports a newly published snapshot after the prior plan
	// set has been retired.
	SnapshotApplied(Stats)
	// SnapshotRejected reports a rejected build and its elapsed build duration.
	SnapshotRejected(*BuildError, time.Duration)
}

// Manager serializes configuration application and atomically publishes
// immutable snapshots. It is safe for concurrent Apply, Acquire, Load, stats,
// health-stop, and close operations.
type Manager struct {
	applyMu   sync.Mutex
	active    atomic.Pointer[Snapshot]
	builder   *Builder
	upstreams *upstream.Registry
	observer  Observer
	closed    atomic.Bool
}

// Lease retains one snapshot and its upstream plan set for a request. A
// successful lease must be released; Release is idempotent.
type Lease struct {
	snapshot *Snapshot
	plans    *upstream.PlanSet
	released atomic.Bool
}

// NewManager returns a Manager using builder and upstreams. Nil dependencies
// are reported as stable BuildError values by Apply rather than by the
// constructor.
func NewManager(builder *Builder, upstreams *upstream.Registry, observer Observer) *Manager {
	return &Manager{builder: builder, upstreams: upstreams, observer: observer}
}

// Apply serializes and transactionally compiles resources for a strictly newer
// revision. Success atomically publishes the new snapshot before retiring the
// prior plan set; rejection preserves the last-known-good snapshot. Apply
// rolls back upstream candidates on every failed path and rejects calls after
// Close begins.
func (m *Manager) Apply(revision uint64, resources model.ResourceSet) error {
	started := time.Now()
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	if m.closed.Load() {
		buildErr := &BuildError{
			Code:     "MANAGER_CLOSED",
			Stage:    StageValidate,
			Revision: revision,
			Cause:    fmt.Errorf("runtime manager is closed"),
		}
		m.notifyRejected(buildErr, time.Since(started))
		return buildErr
	}
	if active := m.active.Load(); active != nil && revision <= active.Revision() {
		buildErr := &BuildError{
			Code:     "STALE_REVISION",
			Stage:    StageValidate,
			Revision: revision,
			Field:    "revision",
			Cause:    fmt.Errorf("active revision is %d", active.Revision()),
		}
		m.notifyRejected(buildErr, time.Since(started))
		return buildErr
	}
	if m.builder == nil {
		buildErr := &BuildError{
			Code:     "BUILDER_UNAVAILABLE",
			Stage:    StageValidate,
			Revision: revision,
			Cause:    fmt.Errorf("runtime builder is nil"),
		}
		m.notifyRejected(buildErr, time.Since(started))
		return buildErr
	}
	if m.upstreams == nil {
		buildErr := &BuildError{
			Code:     "UPSTREAM_REGISTRY_UNAVAILABLE",
			Stage:    StageValidate,
			Revision: revision,
			Cause:    fmt.Errorf("upstream registry is nil"),
		}
		m.notifyRejected(buildErr, time.Since(started))
		return buildErr
	}

	candidate, err := m.upstreams.Prepare(resources.Upstreams)
	if err != nil {
		buildErr := upstreamBuildError(revision, err)
		m.notifyRejected(buildErr, time.Since(started))
		return buildErr
	}
	defer candidate.Rollback()

	snapshot, err := m.builder.Build(revision, resources, candidate)
	duration := time.Since(started)
	if err != nil {
		buildErr, ok := err.(*BuildError)
		if !ok {
			buildErr = &BuildError{
				Code:     "SNAPSHOT_BUILD_FAILED",
				Stage:    StageValidate,
				Revision: revision,
				Cause:    err,
			}
		}
		m.notifyRejected(buildErr, duration)
		return buildErr
	}
	if snapshot == nil || snapshot.Revision() != revision {
		buildErr := &BuildError{
			Code:     "SNAPSHOT_INVARIANT_FAILED",
			Stage:    StageValidate,
			Revision: revision,
			Cause:    fmt.Errorf("builder returned an invalid snapshot"),
		}
		m.notifyRejected(buildErr, duration)
		return buildErr
	}
	plans := candidate.Commit()
	if plans == nil {
		buildErr := &BuildError{
			Code:     "SNAPSHOT_INVARIANT_FAILED",
			Stage:    StageValidate,
			Revision: revision,
			Cause:    fmt.Errorf("candidate ownership transfer failed"),
		}
		m.notifyRejected(buildErr, duration)
		return buildErr
	}
	snapshot.plans = plans
	snapshot.stats.BuildDuration = duration
	old := m.active.Swap(snapshot)
	if old != nil && old.plans != nil {
		old.plans.Retire()
	}
	stats := snapshot.stats
	m.notifyApplied(stats)
	return nil
}

// Acquire returns a lease for one atomically observed snapshot revision. It
// reports false when the manager is nil or no current plan set can be retained.
func (m *Manager) Acquire() (Lease, bool) {
	if m == nil {
		return Lease{}, false
	}
	for {
		snapshot := m.active.Load()
		if snapshot == nil || snapshot.plans == nil {
			return Lease{}, false
		}
		if snapshot.plans.TryAcquire() {
			return Lease{snapshot: snapshot, plans: snapshot.plans}, true
		}
		if m.active.Load() == snapshot {
			return Lease{}, false
		}
	}
}

// UpstreamStats returns current upstream registry gauges, or their zero value
// when the manager or registry is nil.
func (m *Manager) UpstreamStats() upstream.RegistryStats {
	if m == nil || m.upstreams == nil {
		return upstream.RegistryStats{}
	}
	return m.upstreams.Stats()
}

// StopHealth idempotently prevents future active-health scheduling.
func (m *Manager) StopHealth() {
	if m != nil && m.upstreams != nil {
		m.upstreams.StopHealth()
	}
}

// Close serializes against Apply, prevents future publication, removes the
// active snapshot, retires its plan set, and waits for context-bounded upstream
// cleanup. It is safe to call more than once.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.applyMu.Lock()
	m.closed.Store(true)
	active := m.active.Swap(nil)
	if active != nil && active.plans != nil {
		active.plans.Retire()
	}
	m.applyMu.Unlock()
	if m.upstreams == nil {
		return nil
	}
	return m.upstreams.Close(ctx)
}

// Snapshot returns the retained immutable snapshot, or nil for a nil lease.
// The pointer remains valid only until Release.
func (l *Lease) Snapshot() *Snapshot {
	if l == nil {
		return nil
	}
	return l.snapshot
}

// Release idempotently drops the lease's plan-set reference. It is a no-op for
// a nil lease.
func (l *Lease) Release() {
	if l == nil || !l.released.CompareAndSwap(false, true) {
		return
	}
	l.plans.Release()
}

// Load returns the currently published snapshot without acquiring a lease.
// The result is intended for bounded observation only; request processing must
// use Acquire to retain upstream resources.
func (m *Manager) Load() *Snapshot {
	if m == nil {
		return nil
	}
	return m.active.Load()
}

func (m *Manager) notifyApplied(stats Stats) {
	if m.observer == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	m.observer.SnapshotApplied(stats)
}

func (m *Manager) notifyRejected(buildErr *BuildError, duration time.Duration) {
	if m.observer == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	m.observer.SnapshotRejected(buildErr, duration)
}

func upstreamBuildError(revision uint64, err error) *BuildError {
	var configErr *upstream.ConfigError
	if errors.As(err, &configErr) {
		return &BuildError{
			Code:         configErr.Code,
			Stage:        StageValidate,
			Revision:     revision,
			ResourceKind: "upstream",
			ResourceID:   configErr.UpstreamID,
			Field:        configErr.Field,
			Cause:        configErr,
		}
	}
	return &BuildError{
		Code:     "UPSTREAM_PREPARE_FAILED",
		Stage:    StageValidate,
		Revision: revision,
		Field:    "upstreams",
		Cause:    err,
	}
}
