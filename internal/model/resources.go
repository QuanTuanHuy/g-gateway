package model

import (
	"encoding/json"
	"time"
)

type PredicateOperator string

const (
	PredicateExists    PredicateOperator = "exists"
	PredicateNotExists PredicateOperator = "not_exists"
	PredicateEquals    PredicateOperator = "equals"
	PredicateNotEquals PredicateOperator = "not_equals"
	PredicateOneOf     PredicateOperator = "one_of"
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
	Endpoints []string
	Transport TransportConfig
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
	}
	for i := range in.Services {
		out.Services[i] = in.Services[i]
		out.Services[i].Plugins = clonePluginAttachments(in.Services[i].Plugins)
	}
	for i := range in.Upstreams {
		out.Upstreams[i] = in.Upstreams[i]
		out.Upstreams[i].Endpoints = append([]string(nil), in.Upstreams[i].Endpoints...)
	}

	return out
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
