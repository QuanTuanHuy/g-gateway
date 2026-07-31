package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

var benchmarkSnapshotRevision uint64

func BenchmarkSnapshotAcquireRelease(b *testing.B) {
	plugins, err := plugin.NewBuiltinRegistry()
	if err != nil {
		b.Fatal(err)
	}
	builder, err := NewBuilder(plugins)
	if err != nil {
		b.Fatal(err)
	}
	registry, err := upstream.NewRegistry(upstream.RegistryOptions{MaxRetiredSnapshots: 64, HealthWorkers: 2, HealthQueueCapacity: 16})
	if err != nil {
		b.Fatal(err)
	}
	manager := NewManager(builder, registry, nil)
	if err := manager.Apply(1, testResources()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			b.Errorf("Manager.Close() error = %v", err)
		}
	})

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		lease, ok := manager.Acquire()
		if !ok {
			b.Fatal("Acquire rejected active snapshot")
		}
		benchmarkSnapshotRevision = lease.Snapshot().Revision()
		lease.Release()
	}
}
