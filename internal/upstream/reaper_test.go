package upstream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestRetiredPlanSetWaitsForFinalLease(t *testing.T) {
	closed := atomic.Int64{}
	registry := newTestRegistry(t, 64, func() {
		closed.Add(1)
	})
	set := committedTestPlanSet(t, registry)
	if !set.TryAcquire() {
		t.Fatal("TryAcquire rejected active plan set")
	}

	set.Retire()
	registry.reapNow()
	if closed.Load() != 0 {
		t.Fatal("transport closed while request lease remained")
	}

	set.Release()
	registry.reapNow()
	if closed.Load() != 1 {
		t.Fatalf("transport close count = %d, want 1", closed.Load())
	}
	if set.TryAcquire() {
		t.Fatal("TryAcquire accepted finalized plan set")
	}
}

func TestPlanSetReleaseDoesNotBlockWhenWakeChannelIsFull(t *testing.T) {
	registry := &Registry{reapWake: make(chan struct{}, 1)}
	registry.reapWake <- struct{}{}
	set := &PlanSet{registry: registry}
	set.refs.Store(1)

	done := make(chan struct{})
	go func() {
		set.Release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Release blocked on full reaper wake channel")
	}
	if set.TryAcquire() {
		t.Fatal("TryAcquire accepted zero-reference plan set")
	}
}

func TestReaperEventuallyCleansAfterRelease(t *testing.T) {
	closed := atomic.Int64{}
	registry := newTestRegistry(t, 64, func() {
		closed.Add(1)
	})
	set := committedTestPlanSet(t, registry)
	if !set.TryAcquire() {
		t.Fatal("TryAcquire rejected active plan set")
	}
	set.Retire()
	set.Release()

	deadline := time.Now().Add(2 * time.Second)
	for closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if closed.Load() != 1 {
		t.Fatalf("transport close count = %d, want 1", closed.Load())
	}
	registry.reapNow()
	if closed.Load() != 1 {
		t.Fatalf("transport closed more than once: %d", closed.Load())
	}
}

func TestRegistryRejectsPrepareAtRetiredLimit(t *testing.T) {
	registry := newTestRegistry(t, 1, nil)
	set := committedTestPlanSet(t, registry)
	if !set.TryAcquire() {
		t.Fatal("TryAcquire rejected active plan set")
	}
	set.Retire()

	_, err := registry.Prepare([]model.Upstream{
		testUpstream("next", testEndpoint("http://next:8080", 1)),
	})
	assertConfigError(t, err, "RETIRED_SNAPSHOT_LIMIT", "runtime.max_retired_snapshots")

	set.Release()
	registry.reapNow()
}

func TestRegistryCloseHonorsContextWhileLeaseIsLive(t *testing.T) {
	registry := newTestRegistry(t, 64, nil)
	set := committedTestPlanSet(t, registry)
	if !set.TryAcquire() {
		t.Fatal("TryAcquire rejected active plan set")
	}
	set.Retire()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := registry.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want deadline exceeded", err)
	}

	set.Release()
	registry.reapNow()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := registry.Close(closeCtx); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func newTestRegistry(t testing.TB, maxRetiredSnapshots int, closeIdle func()) *Registry {
	t.Helper()
	registry := mustRegistry(t, maxRetiredSnapshots, nil)
	if closeIdle != nil {
		registryTestCloseIdle.Store(registry, closeIdle)
	}
	return registry
}

func committedTestPlanSet(t testing.TB, registry *Registry) *PlanSet {
	t.Helper()
	candidate := mustPrepare(t, registry, []model.Upstream{
		testUpstream("users", testEndpoint("http://users:8080", 1)),
	})
	plan, _ := candidate.Plan("users")
	if stored, ok := registryTestCloseIdle.Load(registry); ok {
		plan.transport.closeIdleConnections = stored.(func())
		registryTestCloseIdle.Delete(registry)
	}
	set := candidate.Commit()
	if set == nil {
		t.Fatal("Commit() = nil")
	}
	return set
}

var registryTestCloseIdle sync.Map
