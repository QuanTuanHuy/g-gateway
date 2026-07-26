package upstream

import (
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

var ErrNoEndpoint = errors.New("upstream plan has no selectable endpoint")

type planEndpoint struct {
	runtime  *endpointRuntime
	identity string
	weight   uint32
}

type Plan struct {
	id        string
	algorithm model.BalancerType
	endpoints []planEndpoint
	transport *transportRuntime
	wrr       wrrSelector
	continuum continuum
	hashKey   hashKeyExtractor
}

func (p *Plan) Select(request *http.Request) (Selection, error) {
	if p == nil || len(p.endpoints) == 0 || p.transport == nil {
		return Selection{}, ErrNoEndpoint
	}
	var (
		ordinal  uint32
		fallback bool
	)
	switch p.algorithm {
	case model.BalancerWeightedRoundRobin:
		ordinal = p.wrr.selectIndex()
	case model.BalancerConsistentHash:
		sum, usedFallback := p.hashKey.sum64(request)
		ordinal = p.continuum.selectIndex(sum)
		fallback = usedFallback
	default:
		return Selection{}, ErrNoEndpoint
	}
	if ordinal >= uint32(len(p.endpoints)) {
		return Selection{}, ErrNoEndpoint
	}
	return Selection{
		endpoint:     p.endpoints[ordinal].runtime,
		transport:    p.transport,
		ordinal:      ordinal,
		balancer:     p.algorithm,
		hashFallback: fallback,
	}, nil
}

type Selection struct {
	endpoint     *endpointRuntime
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
			s.registry.finalizePlanSet(s)
		}
		return
	}
}

func (s *PlanSet) Retire() {
	if s == nil || !s.retired.CompareAndSwap(false, true) {
		return
	}
	s.registry.markRetired(s)
	s.Release()
}
