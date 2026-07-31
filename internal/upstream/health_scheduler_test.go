package upstream

import (
	"context"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type recordingProber struct {
	calls atomic.Int32
	wait  chan struct{}
}

func (p *recordingProber) Probe(ctx context.Context, target ProbeTarget) ProbeResult {
	p.calls.Add(1)
	if p.wait != nil {
		select {
		case <-p.wait:
		case <-ctx.Done():
		}
	}
	return ProbeResult{
		Target:      target,
		Observation: Observation{Source: SourceActive, Kind: OutcomeSuccess, Status: 200},
	}
}

func (*recordingProber) CloseIdleConnections() {}

func TestHealthCoordinatorActivatesLazilyAndUsesFixedWorkers(t *testing.T) {
	prober := &recordingProber{}
	coordinator, err := newHealthCoordinator(healthCoordinatorOptions{
		Workers:       2,
		QueueCapacity: 4,
		HTTPProber:    prober,
		TCPProber:     prober,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeCoordinator(t, coordinator) })
	health := newEndpointHealth("users\x00http://a:80", schedulerHealthPolicy(), 1)
	target := ProbeTarget{
		EndpointID: "users\x00http://a:80",
		URL:        mustURL(t, "http://a:80"),
		Generation: 1,
		Policy:     *health.policy.Active,
	}
	registration := coordinator.Register(target, health)
	if got := coordinator.Stats(); got.Scheduled != 0 || got.Workers != 2 {
		t.Fatalf("stats before activation = %+v", got)
	}
	registration.ActivateHealth()
	eventuallyHealth(t, func() bool { return prober.calls.Load() >= 1 })
	if health.State() != HealthHealthy {
		t.Fatalf("state = %v, want healthy", health.State())
	}
}

func TestHealthCoordinatorDiscardsStaleGeneration(t *testing.T) {
	prober := &recordingProber{}
	coordinator, err := newHealthCoordinator(healthCoordinatorOptions{
		Workers:       1,
		QueueCapacity: 1,
		HTTPProber:    prober,
		TCPProber:     prober,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeCoordinator(t, coordinator) })
	health := newEndpointHealth("users\x00http://a:80", schedulerHealthPolicy(), 2)
	registration := coordinator.Register(ProbeTarget{
		EndpointID: "users\x00http://a:80",
		URL:        mustURL(t, "http://a:80"),
		Generation: 1,
		Policy:     *health.policy.Active,
	}, health)
	registration.ActivateHealth()
	eventuallyHealth(t, func() bool { return prober.calls.Load() >= 1 })
	if health.State() != HealthUnknown {
		t.Fatalf("stale probe changed state to %v", health.State())
	}
}

func TestHealthCoordinatorCloseCancelsProbe(t *testing.T) {
	prober := &recordingProber{wait: make(chan struct{})}
	coordinator, err := newHealthCoordinator(healthCoordinatorOptions{
		Workers:       1,
		QueueCapacity: 1,
		HTTPProber:    prober,
		TCPProber:     prober,
	})
	if err != nil {
		t.Fatal(err)
	}
	health := newEndpointHealth("users\x00http://a:80", schedulerHealthPolicy(), 1)
	coordinator.Register(ProbeTarget{
		EndpointID: "users\x00http://a:80",
		URL:        mustURL(t, "http://a:80"),
		Generation: 1,
		Policy:     *health.policy.Active,
	}, health).ActivateHealth()
	eventuallyHealth(t, func() bool { return prober.calls.Load() == 1 })
	closeCoordinator(t, coordinator)
}

func schedulerHealthPolicy() model.HealthPolicy {
	return model.HealthPolicy{Active: &model.ActiveHealthPolicy{
		Type:              model.HealthCheckHTTP,
		Timeout:           50 * time.Millisecond,
		HealthyInterval:   5 * time.Millisecond,
		UnhealthyInterval: 5 * time.Millisecond,
		HealthySuccesses:  1,
		HTTPFailures:      1,
		TransportFailures: 1,
		Timeouts:          1,
		HealthyStatuses:   []uint16{200},
		UnhealthyStatuses: []uint16{503},
		Path:              "/",
	}}
}

func eventuallyHealth(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func closeCoordinator(t *testing.T, coordinator *HealthCoordinator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
