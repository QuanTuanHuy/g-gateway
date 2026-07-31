package upstream

import (
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

// ErrNoEndpoint reports that a plan or selection has no usable endpoint
// runtime or transport.
var ErrNoEndpoint = errors.New("upstream plan has no selectable endpoint")

// ErrNoHealthyEndpoint reports that every positive-weight endpoint is
// unhealthy or already present in the request's attempt set.
var ErrNoHealthyEndpoint = errors.New("upstream plan has no healthy untried endpoint")

type planEndpoint struct {
	runtime  *endpointRuntime
	health   *EndpointHealth
	identity string
	weight   uint32
}

// AttemptSet is a fixed-capacity set of at most five endpoint ordinals used to
// prevent one request from retrying the same endpoint. Its zero value is ready
// to use.
type AttemptSet struct {
	ordinals [5]uint32
	count    uint8
}

// Add inserts ordinal and reports success. It returns false for a nil set, a
// duplicate ordinal, or a full set.
func (a *AttemptSet) Add(ordinal uint32) bool {
	if a == nil || a.Contains(ordinal) || a.count >= uint8(len(a.ordinals)) {
		return false
	}
	a.ordinals[a.count] = ordinal
	a.count++
	return true
}

// Contains reports whether ordinal is present. It returns false for a nil set.
func (a *AttemptSet) Contains(ordinal uint32) bool {
	if a == nil {
		return false
	}
	for index := uint8(0); index < a.count; index++ {
		if a.ordinals[index] == ordinal {
			return true
		}
	}
	return false
}

// Plan is an immutable prepared upstream selection plan safe for concurrent
// requests. Its referenced health, retry-budget, and selection runtimes
// synchronize their own mutable state and remain valid only while the owning
// PlanSet reference is held.
type Plan struct {
	id                  string
	algorithm           model.BalancerType
	endpoints           []planEndpoint
	transport           *transportRuntime
	wrr                 wrrSelector
	continuum           continuum
	hashKey             hashKeyExtractor
	healthRegistrations []*healthRegistration
	budget              *retryBudget
}

// ActivateHealth lazily activates active-health scheduling for the plan's
// positive-weight endpoints. It is safe and idempotent.
func (p *Plan) ActivateHealth() {
	if p == nil {
		return
	}
	for _, registration := range p.healthRegistrations {
		registration.ActivateHealth()
	}
}

// CreditPrimary adds one primary-request credit to the plan's retry budget.
// It is a no-op when the plan or budget is nil.
func (p *Plan) CreditPrimary() {
	if p != nil && p.budget != nil {
		p.budget.CreditPrimary()
	}
}

// AcquireRetry reserves one retry permit when the plan's token and concurrency
// bounds allow it. It returns false when the plan or budget is nil.
func (p *Plan) AcquireRetry() (RetryPermit, bool) {
	if p == nil || p.budget == nil {
		return RetryPermit{}, false
	}
	return p.budget.Acquire()
}

// Select chooses a selectable endpoint without request-local exclusions.
func (p *Plan) Select(request *http.Request) (Selection, error) {
	return p.SelectNext(request, nil)
}

// SelectNext deterministically chooses a healthy or unknown positive-weight
// endpoint absent from attempted. It fails closed with ErrNoHealthyEndpoint
// when none remains and reports whether consistent hashing used its fallback
// key through the returned Selection.
func (p *Plan) SelectNext(request *http.Request, attempted *AttemptSet) (Selection, error) {
	if p == nil || len(p.endpoints) == 0 || p.transport == nil {
		return Selection{}, ErrNoEndpoint
	}
	var (
		ordinal  uint32
		fallback bool
	)
	switch p.algorithm {
	case model.BalancerWeightedRoundRobin:
		var ok bool
		ordinal, ok = p.wrr.selectNext(func(candidate uint32) bool {
			return p.selectable(candidate, attempted)
		})
		if !ok {
			return Selection{}, ErrNoHealthyEndpoint
		}
	case model.BalancerConsistentHash:
		sum, usedFallback := p.hashKey.sum64(request)
		var ok bool
		ordinal, ok = p.continuum.selectNext(sum, len(p.endpoints), func(candidate uint32) bool {
			return p.selectable(candidate, attempted)
		})
		if !ok {
			return Selection{}, ErrNoHealthyEndpoint
		}
		fallback = usedFallback
	default:
		return Selection{}, ErrNoEndpoint
	}
	if ordinal >= uint32(len(p.endpoints)) {
		return Selection{}, ErrNoEndpoint
	}
	return Selection{
		endpoint:     p.endpoints[ordinal].runtime,
		health:       p.endpoints[ordinal].health,
		transport:    p.transport,
		ordinal:      ordinal,
		balancer:     p.algorithm,
		hashFallback: fallback,
	}, nil
}

func (p *Plan) selectable(ordinal uint32, attempted *AttemptSet) bool {
	if ordinal >= uint32(len(p.endpoints)) || attempted.Contains(ordinal) {
		return false
	}
	health := p.endpoints[ordinal].health
	return health == nil || health.Selectable()
}

// Selection is one endpoint and shared transport chosen by a Plan. Its zero
// value is invalid, and its URL and runtime references must not outlive the
// owning PlanSet lease.
type Selection struct {
	endpoint     *endpointRuntime
	health       *EndpointHealth
	transport    *transportRuntime
	ordinal      uint32
	balancer     model.BalancerType
	hashFallback bool
}

// Valid reports whether the selection contains both endpoint and transport
// runtimes.
func (s Selection) Valid() bool {
	return s.endpoint != nil && s.transport != nil
}

// Target returns the immutable selected endpoint URL, or nil for an invalid
// selection. Callers must not mutate the returned URL.
func (s Selection) Target() *url.URL {
	if s.endpoint == nil {
		return nil
	}
	return s.endpoint.target
}

// RoundTrip sends request through the selected shared transport. It returns
// ErrNoEndpoint without performing I/O when the selection is invalid.
func (s Selection) RoundTrip(request *http.Request) (*http.Response, error) {
	if !s.Valid() {
		return nil, ErrNoEndpoint
	}
	return s.transport.RoundTrip(request)
}

// EndpointID returns the canonical endpoint identity, or an empty string for
// an invalid selection.
func (s Selection) EndpointID() string {
	if s.endpoint == nil {
		return ""
	}
	return s.endpoint.identity
}

// Ordinal returns the endpoint's stable ordinal within the prepared plan.
func (s Selection) Ordinal() uint32 {
	return s.ordinal
}

// Balancer returns the algorithm that produced the selection.
func (s Selection) Balancer() model.BalancerType {
	return s.balancer
}

// HashFallback reports whether consistent hashing fell back because every
// configured dynamic hash-key source was absent.
func (s Selection) HashFallback() bool {
	return s.hashFallback
}

// Observe forwards an outcome to the selected endpoint's health tracker. It is
// a no-op when health tracking is disabled.
func (s Selection) Observe(observation Observation) {
	if s.health != nil {
		s.health.Observe(observation)
	}
}

// PlanSet owns one immutable collection of plans and all registry resource
// references acquired for them. A committed set starts with one owner
// reference; each successful TryAcquire requires exactly one Release.
type PlanSet struct {
	registry  *Registry
	plans     map[string]*Plan
	refs      atomic.Int64
	retired   atomic.Bool
	finalized atomic.Bool
	owned     resourceRefs
}

// Plan returns the immutable plan identified by id.
func (s *PlanSet) Plan(id string) (*Plan, bool) {
	if s == nil {
		return nil, false
	}
	plan, ok := s.plans[id]
	return plan, ok
}

// TryAcquire adds one reference while the set's count remains positive. This
// permits an acquire racing with publication replacement to linearize before
// the retiring owner releases its reference.
func (s *PlanSet) TryAcquire() bool {
	if s == nil {
		return false
	}
	for {
		current := s.refs.Load()
		if current <= 0 {
			return false
		}
		if s.refs.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

// Release drops one reference and schedules asynchronous cleanup when the final
// reference reaches zero. It panics for a nil set or reference underflow.
func (s *PlanSet) Release() {
	if s == nil {
		panic("upstream PlanSet.Release called on nil")
	}
	for {
		current := s.refs.Load()
		if current <= 0 {
			panic("upstream PlanSet reference underflow")
		}
		if !s.refs.CompareAndSwap(current, current-1) {
			continue
		}
		if current == 1 {
			s.registry.signalReaper()
		}
		return
	}
}

// Retire idempotently registers the set for reaping and drops its initial owner
// reference. Resource cleanup waits for every acquired request reference and
// never runs inline on the retiring request path.
func (s *PlanSet) Retire() {
	if s == nil || !s.retired.CompareAndSwap(false, true) {
		return
	}
	s.registry.registerRetired(s)
	s.Release()
}
