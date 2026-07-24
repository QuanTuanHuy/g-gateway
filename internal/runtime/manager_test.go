package runtime

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestManagerActivationKeepsLastKnownGood(t *testing.T) {
	resources := testResources()
	observer := &recordingObserver{}
	manager := NewManager(mustBuilder(t, resources.Upstreams), observer)
	if manager.Load() != nil {
		t.Fatal("Load() returned a snapshot before Apply")
	}

	if err := manager.Apply(1, resources); err != nil {
		t.Fatal(err)
	}
	if got := manager.Load(); got == nil || got.Revision() != 1 {
		t.Fatalf("Load() = %+v", got)
	}
	assertBuildError(t, manager.Apply(1, resources), "STALE_REVISION")
	if manager.Load().Revision() != 1 {
		t.Fatal("stale apply changed active snapshot")
	}

	invalid := model.CloneResourceSet(resources)
	invalid.Routes = nil
	assertBuildError(t, manager.Apply(2, invalid), "ROUTES_EMPTY")
	if manager.Load().Revision() != 1 {
		t.Fatal("invalid apply changed active snapshot")
	}

	if err := manager.Apply(3, resources); err != nil {
		t.Fatal(err)
	}
	if manager.Load().Revision() != 3 {
		t.Fatalf("active revision = %d", manager.Load().Revision())
	}
	if observer.appliedCount() != 2 || observer.rejectedCount() != 2 {
		t.Fatalf("observer applied=%d rejected=%d", observer.appliedCount(), observer.rejectedCount())
	}
}

func TestManagerConcurrentApplyActivatesHighestRevision(t *testing.T) {
	resources := testResources()
	manager := NewManager(mustBuilder(t, resources.Upstreams), nil)
	if err := manager.Apply(1, resources); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	for revision := uint64(4); revision <= 20; revision++ {
		revision := revision
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := manager.Apply(revision, resources)
			var buildErr *BuildError
			if err != nil && (!errors.As(err, &buildErr) || buildErr.Code != "STALE_REVISION") {
				t.Errorf("Apply(%d) error = %v", revision, err)
			}
		}()
	}
	wait.Wait()
	if got := manager.Load().Revision(); got != 20 {
		t.Fatalf("active revision = %d, want 20", got)
	}
}

func TestManagerSerializesSlowLowerRevisionBeforeHigherRevision(t *testing.T) {
	resources := testResources()
	builder := mustBuilder(t, resources.Upstreams)
	manager := NewManager(builder, nil)
	if err := manager.Apply(1, resources); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	builder.beforeBuild = func(revision uint64) {
		if revision == 4 {
			close(entered)
			<-release
		}
	}

	lowerDone := make(chan error, 1)
	go func() { lowerDone <- manager.Apply(4, resources) }()
	<-entered
	higherDone := make(chan error, 1)
	go func() { higherDone <- manager.Apply(5, resources) }()
	close(release)

	if err := <-lowerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-higherDone; err != nil {
		t.Fatal(err)
	}
	if got := manager.Load().Revision(); got != 5 {
		t.Fatalf("active revision = %d, want 5", got)
	}
}

type recordingObserver struct {
	mu       sync.Mutex
	applied  []Stats
	rejected []*BuildError
}

func (o *recordingObserver) SnapshotApplied(stats Stats) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.applied = append(o.applied, stats)
}

func (o *recordingObserver) SnapshotRejected(buildErr *BuildError, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rejected = append(o.rejected, buildErr)
}

func (o *recordingObserver) appliedCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.applied)
}

func (o *recordingObserver) rejectedCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.rejected)
}
