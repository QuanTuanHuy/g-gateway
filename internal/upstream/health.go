package upstream

import (
	"slices"
	"sync"
	"sync/atomic"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

// HealthState is the current selectability state of one endpoint.
type HealthState uint32

// ObservationSource identifies whether an outcome came from a scheduled probe
// or proxied request traffic.
type ObservationSource uint8

// OutcomeKind is the closed classification applied to a health observation.
type OutcomeKind uint8

const (
	// HealthUnknown is the initial selectable state before a threshold is
	// reached.
	HealthUnknown HealthState = iota
	// HealthHealthy is the selectable state after the configured success
	// threshold is reached.
	HealthHealthy
	// HealthUnhealthy is the non-selectable state after a failure threshold is
	// reached.
	HealthUnhealthy
)

const (
	// SourceActive identifies an observation from an active health probe.
	SourceActive ObservationSource = iota
	// SourcePassive identifies an observation from proxied request traffic.
	SourcePassive
)

const (
	// OutcomeSuccess records a completed healthy outcome.
	OutcomeSuccess OutcomeKind = iota
	// OutcomeHTTPFailure records a configured unhealthy HTTP status.
	OutcomeHTTPFailure
	// OutcomeTransportFailure records a non-timeout transport failure.
	OutcomeTransportFailure
	// OutcomeTimeout records an operation classified as timed out.
	OutcomeTimeout
	// OutcomeNeutral records an active HTTP status that changes no counters.
	OutcomeNeutral
)

// Observation is one active or passive endpoint-health outcome. A non-zero
// Status is classified against the applicable policy before counters change.
type Observation struct {
	// Source identifies active probing or passive request traffic.
	Source ObservationSource
	// Kind is the preliminary or final outcome classification.
	Kind OutcomeKind
	// Status is the HTTP response status, or zero for a transport-only outcome.
	Status int
}

// HealthTransition describes one endpoint state change delivered to a
// transition hook.
type HealthTransition struct {
	// EndpointID is the canonical endpoint identity.
	EndpointID string
	// Generation identifies the health runtime that produced the transition.
	Generation uint64
	// Source identifies the observation that caused the transition.
	Source ObservationSource
	// From is the previous endpoint state.
	From HealthState
	// To is the new endpoint state.
	To HealthState
}

// EndpointHealth tracks one generation of endpoint health. It starts unknown,
// treats unknown and healthy as selectable, permits recovery from unhealthy
// only through active successes, and is safe for concurrent observation and
// reads.
type EndpointHealth struct {
	state      atomic.Uint32
	generation uint64
	identity   string
	policy     model.HealthPolicy
	mu         sync.Mutex
	successes  uint8
	http       uint8
	transport  uint8
	timeouts   uint8
	onChange   func(HealthTransition)
	retired    atomic.Bool
}

func newEndpointHealth(identity string, policy model.HealthPolicy, generation uint64) *EndpointHealth {
	return &EndpointHealth{
		identity:   identity,
		policy:     policy,
		generation: generation,
	}
}

// State returns the endpoint's current health state.
func (h *EndpointHealth) State() HealthState {
	return HealthState(h.state.Load())
}

// Selectable reports whether the endpoint may receive request traffic.
func (h *EndpointHealth) Selectable() bool {
	return HealthState(h.state.Load()) != HealthUnhealthy
}

// Generation returns the immutable runtime generation used to reject stale
// probe results.
func (h *EndpointHealth) Generation() uint64 {
	return h.generation
}

// SetTransitionHook replaces the callback invoked after each state transition.
// The callback runs outside the health mutex.
func (h *EndpointHealth) SetTransitionHook(hook func(HealthTransition)) {
	h.mu.Lock()
	h.onChange = hook
	h.mu.Unlock()
}

// Retire idempotently prevents future observations from changing state.
func (h *EndpointHealth) Retire() {
	h.retired.Store(true)
}

// Observe applies one observation according to active/passive thresholds. A
// success resets all failure counters, a failure resets the success streak and
// increments only its category, neutral outcomes change nothing, and every
// transition resets all counters. Observations after retirement and passive
// observations without a passive policy are ignored.
func (h *EndpointHealth) Observe(observation Observation) {
	if observation.Source == SourcePassive && h.policy.Passive == nil {
		return
	}
	if h.retired.Load() {
		return
	}

	h.mu.Lock()
	if h.retired.Load() {
		h.mu.Unlock()
		return
	}
	observation = h.classifyHTTP(observation)
	if observation.Kind == OutcomeNeutral {
		h.mu.Unlock()
		return
	}
	previous := HealthState(h.state.Load())
	next := h.apply(previous, observation)
	var transition *HealthTransition
	hook := h.onChange
	if next != previous {
		h.state.Store(uint32(next))
		h.resetCounters()
		value := HealthTransition{
			EndpointID: h.identity,
			Generation: h.generation,
			Source:     observation.Source,
			From:       previous,
			To:         next,
		}
		transition = &value
	}
	h.mu.Unlock()

	if transition != nil && hook != nil {
		hook(*transition)
	}
}

func (h *EndpointHealth) classifyHTTP(observation Observation) Observation {
	if observation.Status == 0 {
		return observation
	}
	status := uint16(observation.Status)
	if observation.Source == SourcePassive {
		if h.policy.Passive == nil {
			observation.Kind = OutcomeNeutral
		} else if slices.Contains(h.policy.Passive.UnhealthyStatuses, status) {
			observation.Kind = OutcomeHTTPFailure
		} else {
			observation.Kind = OutcomeSuccess
		}
		return observation
	}
	if h.policy.Active != nil && h.policy.Active.Type == model.HealthCheckHTTP {
		switch {
		case slices.Contains(h.policy.Active.HealthyStatuses, status):
			observation.Kind = OutcomeSuccess
		case slices.Contains(h.policy.Active.UnhealthyStatuses, status):
			observation.Kind = OutcomeHTTPFailure
		default:
			observation.Kind = OutcomeNeutral
		}
	}
	return observation
}

func (h *EndpointHealth) apply(state HealthState, observation Observation) HealthState {
	switch observation.Kind {
	case OutcomeSuccess:
		h.http, h.transport, h.timeouts = 0, 0, 0
		if state == HealthUnhealthy && observation.Source != SourceActive {
			h.successes = 0
			return state
		}
		h.successes = saturatingIncrement(h.successes)
		if h.policy.Active != nil && h.successes >= h.policy.Active.HealthySuccesses {
			return HealthHealthy
		}
	case OutcomeHTTPFailure:
		h.successes = 0
		h.http = saturatingIncrement(h.http)
		if h.http >= h.failureThreshold(observation.Source, OutcomeHTTPFailure) {
			return HealthUnhealthy
		}
	case OutcomeTransportFailure:
		h.successes = 0
		h.transport = saturatingIncrement(h.transport)
		if h.transport >= h.failureThreshold(observation.Source, OutcomeTransportFailure) {
			return HealthUnhealthy
		}
	case OutcomeTimeout:
		h.successes = 0
		h.timeouts = saturatingIncrement(h.timeouts)
		if h.timeouts >= h.failureThreshold(observation.Source, OutcomeTimeout) {
			return HealthUnhealthy
		}
	}
	return state
}

func (h *EndpointHealth) failureThreshold(source ObservationSource, kind OutcomeKind) uint8 {
	if source == SourcePassive {
		if h.policy.Passive == nil {
			return 255
		}
		switch kind {
		case OutcomeHTTPFailure:
			return enabledThreshold(h.policy.Passive.HTTPFailures)
		case OutcomeTransportFailure:
			return enabledThreshold(h.policy.Passive.TransportFailures)
		case OutcomeTimeout:
			return enabledThreshold(h.policy.Passive.Timeouts)
		}
	}
	if h.policy.Active == nil {
		return 255
	}
	switch kind {
	case OutcomeHTTPFailure:
		return enabledThreshold(h.policy.Active.HTTPFailures)
	case OutcomeTransportFailure:
		return enabledThreshold(h.policy.Active.TransportFailures)
	case OutcomeTimeout:
		return enabledThreshold(h.policy.Active.Timeouts)
	default:
		return 255
	}
}

func enabledThreshold(value uint8) uint8 {
	if value == 0 {
		return 255
	}
	return value
}

func saturatingIncrement(value uint8) uint8 {
	if value == 255 {
		return value
	}
	return value + 1
}

func (h *EndpointHealth) resetCounters() {
	h.successes = 0
	h.http = 0
	h.transport = 0
	h.timeouts = 0
}
