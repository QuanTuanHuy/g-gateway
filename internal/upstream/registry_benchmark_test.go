package upstream

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func BenchmarkRegistryReconcile(b *testing.B) {
	b.Run("full", func(b *testing.B) {
		b.StopTimer()
		baseline, _ := generatePhase3AResources(b, normalPhase3AProfile)
		revision := model.CloneResourceSet(model.ResourceSet{Upstreams: baseline}).Upstreams
		for upstreamIndex := range revision {
			revision[upstreamIndex].Transport.ResponseHeaderTimeout = 3 * time.Second
			for endpointIndex := range revision[upstreamIndex].Endpoints {
				revision[upstreamIndex].Endpoints[endpointIndex].URL = strings.Replace(
					revision[upstreamIndex].Endpoints[endpointIndex].URL,
					".example:8080",
					".full.example:8080",
					1,
				)
			}
		}
		benchmarkRegistryReconcile(b, baseline, revision)
	})
	b.Run("weight-only", func(b *testing.B) {
		b.StopTimer()
		baseline, _ := generatePhase3AResources(b, normalPhase3AProfile)
		revision := reweightPhase3AResources(baseline, 1)
		benchmarkRegistryReconcile(b, baseline, revision)
	})
}

func benchmarkRegistryReconcile(
	b *testing.B,
	baseline []model.Upstream,
	revision []model.Upstream,
) {
	b.Helper()
	registry, err := NewRegistry(RegistryOptions{MaxRetiredSnapshots: 64, HealthWorkers: 2, HealthQueueCapacity: 16})
	if err != nil {
		b.Fatal(err)
	}
	initial, err := registry.Prepare(model.ResourceSet{Upstreams: baseline})
	if err != nil {
		b.Fatal(err)
	}
	active := initial.Commit()
	if active == nil {
		b.Fatal("initial commit returned nil")
	}
	b.Cleanup(func() {
		active.Retire()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := registry.Close(ctx); err != nil {
			b.Errorf("Registry.Close() error = %v", err)
		}
	})

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for b.Loop() {
		candidate, prepareErr := registry.Prepare(model.ResourceSet{Upstreams: revision})
		if prepareErr != nil {
			b.Fatal(prepareErr)
		}
		candidate.Rollback()
	}
}
