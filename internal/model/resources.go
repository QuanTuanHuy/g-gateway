package model

import (
	"encoding/json"
	"time"
)

type PredicateOperator string
type BalancerType string
type HashSourceType string
type HealthCheckType string

const (
	PredicateExists    PredicateOperator = "exists"
	PredicateNotExists PredicateOperator = "not_exists"
	PredicateEquals    PredicateOperator = "equals"
	PredicateNotEquals PredicateOperator = "not_equals"
	PredicateOneOf     PredicateOperator = "one_of"

	BalancerWeightedRoundRobin BalancerType = "weighted_round_robin"
	BalancerConsistentHash     BalancerType = "consistent_hash"

	HashSourceHeader     HashSourceType = "header"
	HashSourceCookie     HashSourceType = "cookie"
	HashSourceRemoteAddr HashSourceType = "remote_addr"
	HashSourceLiteral    HashSourceType = "literal"

	HealthCheckHTTP HealthCheckType = "http"
	HealthCheckTCP  HealthCheckType = "tcp"
)

type ResourceSet struct {
	Routes    []Route
	Services  []Service
	Upstreams []Upstream
}

type Route struct {
	ID          string
	Priority    int
	Match       RouteMatch
	ServiceRef  string
	UpstreamRef string
	Plugins     []PluginAttachment
	Resilience  RouteResiliencePolicy
}

type RouteMatch struct {
	Hosts   []string
	Path    string
	Methods []string
	Headers []Predicate
	Queries []Predicate
}

type Predicate struct {
	Name     string
	Operator PredicateOperator
	Values   []string
}

type Service struct {
	ID          string
	UpstreamRef string
	Plugins     []PluginAttachment
}

type PluginAttachment struct {
	Name      string
	Enabled   bool
	RawConfig json.RawMessage
}

type Upstream struct {
	ID        string
	Endpoints []Endpoint
	Balancer  BalancerPolicy
	Transport TransportConfig
	Health    HealthPolicy
	Retry     RetryPolicy
}

type HealthPolicy struct {
	Active  *ActiveHealthPolicy
	Passive *PassiveHealthPolicy
}

type ActiveHealthPolicy struct {
	Type              HealthCheckType
	Timeout           time.Duration
	HealthyInterval   time.Duration
	UnhealthyInterval time.Duration
	HealthySuccesses  uint8
	HTTPFailures      uint8
	TransportFailures uint8
	Timeouts          uint8
	HealthyStatuses   []uint16
	UnhealthyStatuses []uint16
	Path              string
	Host              string
}

type PassiveHealthPolicy struct {
	HTTPFailures      uint8
	TransportFailures uint8
	Timeouts          uint8
	UnhealthyStatuses []uint16
}

type RetryOnPolicy struct {
	ConnectFailure        bool
	ConnectionFailure     bool
	ResponseHeaderTimeout bool
	Statuses              []uint16
}

type RetryBudgetPolicy struct {
	RatioPer1000 uint16
	Burst        uint16
	MaxInflight  uint16
}

type RetryPolicy struct {
	MaxAttempts  uint8
	Methods      []string
	RetryOn      RetryOnPolicy
	Budget       RetryBudgetPolicy
	TotalTimeout time.Duration
}

type RouteResiliencePolicy struct {
	TotalTimeout *time.Duration
	MaxAttempts  *uint8
	Methods      *[]string
	RetryOn      *RetryOnPolicy
}

type Endpoint struct {
	URL    string
	Weight uint32
}

type BalancerPolicy struct {
	Type    BalancerType
	HashKey HashKeyPolicy
}

type HashKeyPolicy struct {
	Sources []HashKeySource
}

type HashKeySource struct {
	Type  HashSourceType
	Name  string
	Value string
}

type TransportConfig struct {
	DialTimeout               time.Duration
	ResponseHeaderTimeout     time.Duration
	IdleConnectionTimeout     time.Duration
	MaxIdleConnections        int
	MaxIdleConnectionsPerHost int
}

func CloneResourceSet(in ResourceSet) ResourceSet {
	out := ResourceSet{
		Routes:    make([]Route, len(in.Routes)),
		Services:  make([]Service, len(in.Services)),
		Upstreams: make([]Upstream, len(in.Upstreams)),
	}

	for i := range in.Routes {
		out.Routes[i] = in.Routes[i]
		out.Routes[i].Match = cloneRouteMatch(in.Routes[i].Match)
		out.Routes[i].Plugins = clonePluginAttachments(in.Routes[i].Plugins)
		out.Routes[i].Resilience = cloneRouteResiliencePolicy(in.Routes[i].Resilience)
	}
	for i := range in.Services {
		out.Services[i] = in.Services[i]
		out.Services[i].Plugins = clonePluginAttachments(in.Services[i].Plugins)
	}
	for i := range in.Upstreams {
		out.Upstreams[i] = in.Upstreams[i]
		out.Upstreams[i].Endpoints = append([]Endpoint(nil), in.Upstreams[i].Endpoints...)
		out.Upstreams[i].Balancer.HashKey.Sources = append(
			[]HashKeySource(nil),
			in.Upstreams[i].Balancer.HashKey.Sources...,
		)
		out.Upstreams[i].Health = cloneHealthPolicy(in.Upstreams[i].Health)
		out.Upstreams[i].Retry = cloneRetryPolicy(in.Upstreams[i].Retry)
	}

	return out
}

func cloneRouteResiliencePolicy(in RouteResiliencePolicy) RouteResiliencePolicy {
	out := in
	if in.TotalTimeout != nil {
		value := *in.TotalTimeout
		out.TotalTimeout = &value
	}
	if in.MaxAttempts != nil {
		value := *in.MaxAttempts
		out.MaxAttempts = &value
	}
	if in.Methods != nil {
		value := cloneStrings(*in.Methods)
		out.Methods = &value
	}
	if in.RetryOn != nil {
		value := cloneRetryOnPolicy(*in.RetryOn)
		out.RetryOn = &value
	}
	return out
}

func cloneHealthPolicy(in HealthPolicy) HealthPolicy {
	out := in
	if in.Active != nil {
		value := *in.Active
		value.HealthyStatuses = cloneUint16s(in.Active.HealthyStatuses)
		value.UnhealthyStatuses = cloneUint16s(in.Active.UnhealthyStatuses)
		out.Active = &value
	}
	if in.Passive != nil {
		value := *in.Passive
		value.UnhealthyStatuses = cloneUint16s(in.Passive.UnhealthyStatuses)
		out.Passive = &value
	}
	return out
}

func cloneRetryPolicy(in RetryPolicy) RetryPolicy {
	out := in
	out.Methods = cloneStrings(in.Methods)
	out.RetryOn = cloneRetryOnPolicy(in.RetryOn)
	return out
}

func cloneRetryOnPolicy(in RetryOnPolicy) RetryOnPolicy {
	out := in
	out.Statuses = cloneUint16s(in.Statuses)
	return out
}

func cloneUint16s(in []uint16) []uint16 {
	if in == nil {
		return nil
	}
	return append([]uint16{}, in...)
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string{}, in...)
}

func cloneRouteMatch(in RouteMatch) RouteMatch {
	out := in
	out.Hosts = append([]string(nil), in.Hosts...)
	out.Methods = append([]string(nil), in.Methods...)
	out.Headers = clonePredicates(in.Headers)
	out.Queries = clonePredicates(in.Queries)
	return out
}

func clonePredicates(in []Predicate) []Predicate {
	out := make([]Predicate, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Values = append([]string(nil), in[i].Values...)
	}
	return out
}

func clonePluginAttachments(in []PluginAttachment) []PluginAttachment {
	out := make([]PluginAttachment, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].RawConfig = append(json.RawMessage(nil), in[i].RawConfig...)
	}
	return out
}
