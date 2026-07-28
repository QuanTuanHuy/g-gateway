package upstream

import (
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestEndpointHealthTransitionsAndActiveOnlyRecovery(t *testing.T) {
	health := newEndpointHealth("users\x00http://a:80", thresholdTwoHealthPolicy(), 1)
	if health.State() != HealthUnknown || !health.Selectable() {
		t.Fatalf("initial state = %v selectable=%v", health.State(), health.Selectable())
	}
	health.Observe(Observation{Source: SourcePassive, Kind: OutcomeHTTPFailure, Status: 503})
	health.Observe(Observation{Source: SourcePassive, Kind: OutcomeHTTPFailure, Status: 503})
	if health.State() != HealthUnhealthy || health.Selectable() {
		t.Fatalf("failed state = %v selectable=%v", health.State(), health.Selectable())
	}
	health.Observe(Observation{Source: SourcePassive, Kind: OutcomeSuccess, Status: 200})
	health.Observe(Observation{Source: SourcePassive, Kind: OutcomeSuccess, Status: 200})
	if health.State() != HealthUnhealthy {
		t.Fatalf("passive recovered unhealthy endpoint: %v", health.State())
	}
	health.Observe(Observation{Source: SourceActive, Kind: OutcomeSuccess, Status: 200})
	health.Observe(Observation{Source: SourceActive, Kind: OutcomeSuccess, Status: 200})
	if health.State() != HealthHealthy || !health.Selectable() {
		t.Fatalf("recovered state = %v selectable=%v", health.State(), health.Selectable())
	}
}

func TestEndpointHealthRetainsFailureCategoriesUntilSuccess(t *testing.T) {
	health := newEndpointHealth("users\x00http://a:80", thresholdTwoHealthPolicy(), 1)
	health.Observe(Observation{Source: SourceActive, Kind: OutcomeTransportFailure})
	health.Observe(Observation{Source: SourceActive, Kind: OutcomeTimeout})
	if health.transport != 1 || health.timeouts != 1 {
		t.Fatalf("failure counters = transport:%d timeouts:%d", health.transport, health.timeouts)
	}
	health.Observe(Observation{Source: SourceActive, Kind: OutcomeNeutral, Status: 302})
	if health.transport != 1 || health.timeouts != 1 {
		t.Fatalf("neutral changed counters = transport:%d timeouts:%d", health.transport, health.timeouts)
	}
	health.Observe(Observation{Source: SourceActive, Kind: OutcomeSuccess, Status: 200})
	if health.transport != 0 || health.timeouts != 0 || health.successes != 1 {
		t.Fatalf("success counters = success:%d transport:%d timeouts:%d", health.successes, health.transport, health.timeouts)
	}
}

func TestEndpointHealthClassifiesPassiveHTTPStatus(t *testing.T) {
	health := newEndpointHealth("users\x00http://a:80", thresholdTwoHealthPolicy(), 1)
	health.Observe(Observation{Source: SourcePassive, Kind: OutcomeSuccess, Status: 503})
	health.Observe(Observation{Source: SourcePassive, Kind: OutcomeSuccess, Status: 503})
	if health.State() != HealthUnhealthy {
		t.Fatalf("state = %v, want unhealthy", health.State())
	}
}

func TestEndpointHealthIgnoresObservationsAfterRetirement(t *testing.T) {
	health := newEndpointHealth("users\x00http://a:80", thresholdTwoHealthPolicy(), 7)
	health.Retire()
	health.Observe(Observation{Source: SourceActive, Kind: OutcomeTimeout})
	health.Observe(Observation{Source: SourceActive, Kind: OutcomeTimeout})
	if health.State() != HealthUnknown || health.timeouts != 0 {
		t.Fatalf("retired health changed: state=%v timeouts=%d", health.State(), health.timeouts)
	}
}

func thresholdTwoHealthPolicy() model.HealthPolicy {
	return model.HealthPolicy{
		Active: &model.ActiveHealthPolicy{
			Type:              model.HealthCheckHTTP,
			HealthySuccesses:  2,
			HTTPFailures:      2,
			TransportFailures: 2,
			Timeouts:          2,
			HealthyStatuses:   []uint16{200},
			UnhealthyStatuses: []uint16{503},
		},
		Passive: &model.PassiveHealthPolicy{
			HTTPFailures:      2,
			TransportFailures: 2,
			Timeouts:          2,
			UnhealthyStatuses: []uint16{503},
		},
	}
}
