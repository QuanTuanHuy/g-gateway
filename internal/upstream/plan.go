package upstream

import (
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

var ErrNoEndpoint = errors.New("upstream plan has no selectable endpoint")
var ErrNoHealthyEndpoint = errors.New("upstream plan has no healthy untried endpoint")

type planEndpoint struct {
	runtime  *endpointRuntime
	health   *EndpointHealth
	identity string
	weight   uint32
}

type AttemptSet struct {
	ordinals [5]uint32
	count    uint8
}

func (a *AttemptSet) Add(ordinal uint32) bool {
	if a == nil || a.Contains(ordinal) || a.count >= uint8(len(a.ordinals)) {
		return false
	}
	a.ordinals[a.count] = ordinal
	a.count++
	return true
}

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

func (p *Plan) ActivateHealth() {
	if p == nil {
		return
	}
	for _, registration := range p.healthRegistrations {
		registration.ActivateHealth()
	}
}

func (p *Plan) CreditPrimary() {
	if p != nil && p.budget != nil {
		p.budget.CreditPrimary()
	}
}

func (p *Plan) AcquireRetry() (RetryPermit, bool) {
	if p == nil || p.budget == nil {
		return RetryPermit{}, false
	}
	return p.budget.Acquire()
}

func (p *Plan) Select(request *http.Request) (Selection, error) {
	return p.SelectNext(request, nil)
}

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

type Selection struct {
	endpoint     *endpointRuntime
	health       *EndpointHealth
	transport    *transportRuntime
	ordinal      uint32
	balancer     model.BalancerType
	hashFallback bool
}

func (s Selection) Valid() bool {
	return s.endpoint != nil && s.transport != nil
}

func (s Selection) Target() *url.URL {
	if s.endpoint == nil {
		return nil
	}
	return s.endpoint.target
}

func (s Selection) RoundTrip(request *http.Request) (*http.Response, error) {
	if !s.Valid() {
		return nil, ErrNoEndpoint
	}
	return s.transport.RoundTrip(request)
}

func (s Selection) EndpointID() string {
	if s.endpoint == nil {
		return ""
	}
	return s.endpoint.identity
}

func (s Selection) Ordinal() uint32 {
	return s.ordinal
}

func (s Selection) Balancer() model.BalancerType {
	return s.balancer
}

func (s Selection) HashFallback() bool {
	return s.hashFallback
}

func (s Selection) Observe(observation Observation) {
	if s.health != nil {
		s.health.Observe(observation)
	}
}

type PlanSet struct {
	registry  *Registry
	plans     map[string]*Plan
	refs      atomic.Int64
	retired   atomic.Bool
	finalized atomic.Bool
	owned     resourceRefs
}

func (s *PlanSet) Plan(id string) (*Plan, bool) {
	if s == nil {
		return nil, false
	}
	plan, ok := s.plans[id]
	return plan, ok
}

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

func (s *PlanSet) Retire() {
	if s == nil || !s.retired.CompareAndSwap(false, true) {
		return
	}
	s.registry.registerRetired(s)
	s.Release()
}
