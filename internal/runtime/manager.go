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

type Observer interface {
	SnapshotApplied(Stats)
	SnapshotRejected(*BuildError, time.Duration)
}

type Manager struct {
	applyMu   sync.Mutex
	active    atomic.Pointer[Snapshot]
	builder   *Builder
	upstreams *upstream.Registry
	observer  Observer
	closed    atomic.Bool
}

type Lease struct {
	snapshot *Snapshot
	plans    *upstream.PlanSet
	released atomic.Bool
}

func NewManager(builder *Builder, upstreams *upstream.Registry, observer Observer) *Manager {
	return &Manager{builder: builder, upstreams: upstreams, observer: observer}
}

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

func (m *Manager) Acquire() (*Lease, bool) {
	if m == nil {
		return nil, false
	}
	for {
		snapshot := m.active.Load()
		if snapshot == nil || snapshot.plans == nil {
			return nil, false
		}
		if snapshot.plans.TryAcquire() {
			return &Lease{snapshot: snapshot, plans: snapshot.plans}, true
		}
		if m.active.Load() == snapshot {
			return nil, false
		}
	}
}

func (m *Manager) UpstreamStats() upstream.RegistryStats {
	if m == nil || m.upstreams == nil {
		return upstream.RegistryStats{}
	}
	return m.upstreams.Stats()
}

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

func (l *Lease) Snapshot() *Snapshot {
	if l == nil {
		return nil
	}
	return l.snapshot
}

func (l *Lease) Release() {
	if l == nil || !l.released.CompareAndSwap(false, true) {
		return
	}
	l.plans.Release()
}

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
