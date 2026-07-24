package runtime

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type Observer interface {
	SnapshotApplied(Stats)
	SnapshotRejected(*BuildError, time.Duration)
}

type Manager struct {
	applyMu  sync.Mutex
	active   atomic.Pointer[Snapshot]
	builder  *Builder
	observer Observer
}

func NewManager(builder *Builder, observer Observer) *Manager {
	return &Manager{builder: builder, observer: observer}
}

func (m *Manager) Apply(revision uint64, resources model.ResourceSet) error {
	started := time.Now()
	m.applyMu.Lock()
	if active := m.active.Load(); active != nil && revision <= active.Revision() {
		buildErr := &BuildError{
			Code:     "STALE_REVISION",
			Stage:    StageValidate,
			Revision: revision,
			Field:    "revision",
			Cause:    fmt.Errorf("active revision is %d", active.Revision()),
		}
		m.applyMu.Unlock()
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
		m.applyMu.Unlock()
		m.notifyRejected(buildErr, time.Since(started))
		return buildErr
	}

	snapshot, err := m.builder.Build(revision, resources)
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
		m.applyMu.Unlock()
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
		m.applyMu.Unlock()
		m.notifyRejected(buildErr, duration)
		return buildErr
	}
	snapshot.stats.BuildDuration = duration
	m.active.Store(snapshot)
	stats := snapshot.stats
	m.applyMu.Unlock()
	m.notifyApplied(stats)
	return nil
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
