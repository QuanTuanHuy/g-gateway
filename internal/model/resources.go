// Package model defines the canonical resources consumed by the gateway
// compiler and runtime.
//
// Resource values are treated as immutable after validation and compilation.
// Use CloneResourceSet when a caller needs an independently owned copy.
package model

import (
	"encoding/json"
	"time"
)

// PredicateOperator identifies how a route predicate compares a header or
// query value.
type PredicateOperator string

// BalancerType identifies the deterministic endpoint-selection algorithm used
// by an upstream.
type BalancerType string

// HashSourceType identifies one component of a consistent-hash key.
type HashSourceType string

// HealthCheckType identifies the protocol used by an active health check.
type HealthCheckType string

const (
	// PredicateExists matches when the named header or query parameter is
	// present, regardless of its value.
	PredicateExists PredicateOperator = "exists"
	// PredicateNotExists matches when the named header or query parameter is
	// absent.
	PredicateNotExists PredicateOperator = "not_exists"
	// PredicateEquals matches when the named header or query parameter equals
	// the predicate's single configured value.
	PredicateEquals PredicateOperator = "equals"
	// PredicateNotEquals matches when the named header or query parameter does
	// not equal the predicate's single configured value.
	PredicateNotEquals PredicateOperator = "not_equals"
	// PredicateOneOf matches when the named header or query parameter equals
	// one of the predicate's configured values.
	PredicateOneOf PredicateOperator = "one_of"

	// BalancerWeightedRoundRobin selects endpoints according to their
	// configured positive weights using a deterministic schedule.
	BalancerWeightedRoundRobin BalancerType = "weighted_round_robin"
	// BalancerConsistentHash selects endpoints from a deterministic hash
	// continuum built from the configured positive weights.
	BalancerConsistentHash BalancerType = "consistent_hash"

	// HashSourceHeader appends the named request header value to a compound
	// hash key.
	HashSourceHeader HashSourceType = "header"
	// HashSourceCookie appends the named request cookie value to a compound
	// hash key.
	HashSourceCookie HashSourceType = "cookie"
	// HashSourceRemoteAddr appends the request's remote address to a compound
	// hash key.
	HashSourceRemoteAddr HashSourceType = "remote_addr"
	// HashSourceLiteral appends a configured literal value to a compound hash
	// key.
	HashSourceLiteral HashSourceType = "literal"

	// HealthCheckHTTP probes application-level HTTP health and classifies the
	// returned status.
	HealthCheckHTTP HealthCheckType = "http"
	// HealthCheckTCP probes raw TCP reachability without asserting application
	// health.
	HealthCheckTCP HealthCheckType = "tcp"
)

// ResourceSet contains the canonical routes, services, and upstreams for one
// configuration revision. References use resource IDs, and declaration order
// does not determine compiled route precedence.
type ResourceSet struct {
	// Routes contains the request-routing resources in the revision.
	Routes []Route
	// Services contains reusable plugin and upstream bindings referenced by
	// routes.
	Services []Service
	// Upstreams contains endpoint, balancing, transport, health, and retry
	// resources referenced directly or through services.
	Upstreams []Upstream
}

// Route binds one HTTP match expression to exactly one service or upstream.
// Its plugins override same-named service plugins, and a disabled route
// attachment removes an inherited service plugin.
type Route struct {
	// ID uniquely identifies the route within a resource set.
	ID string
	// Priority is the explicit route precedence used before compiled
	// specificity.
	Priority int
	// Match defines the HTTP request conditions for the route.
	Match RouteMatch
	// ServiceRef identifies the route's service when UpstreamRef is empty.
	ServiceRef string
	// UpstreamRef identifies the route's upstream when ServiceRef is empty.
	UpstreamRef string
	// Plugins contains route-scoped plugin attachments.
	Plugins []PluginAttachment
	// Resilience optionally replaces selected retry and total-timeout fields
	// inherited from the resolved upstream.
	Resilience RouteResiliencePolicy
}

// RouteMatch describes the conditions that must all match an HTTP request.
// Predicates within Headers and Queries use AND semantics.
type RouteMatch struct {
	// Hosts lists accepted request hosts; an empty list does not restrict the
	// host.
	Hosts []string
	// Path is the absolute route pattern matched against the request path.
	Path string
	// Methods lists accepted HTTP methods.
	Methods []string
	// Headers lists header predicates that must all match.
	Headers []Predicate
	// Queries lists query predicates that must all match.
	Queries []Predicate
}

// Predicate compares one named header or query parameter using an operator and
// its operator-specific values.
type Predicate struct {
	// Name identifies the header or query parameter to inspect.
	Name string
	// Operator selects the comparison semantics.
	Operator PredicateOperator
	// Values contains no entries for existence operators, one entry for
	// equality operators, and one or more entries for PredicateOneOf.
	Values []string
}

// Service groups reusable plugin attachments with one referenced upstream.
type Service struct {
	// ID uniquely identifies the service within a resource set.
	ID string
	// UpstreamRef identifies the upstream used by routes that reference this
	// service.
	UpstreamRef string
	// Plugins contains service-scoped plugin attachments inherited by routes.
	Plugins []PluginAttachment
}

// PluginAttachment configures one named plugin at service or route scope.
type PluginAttachment struct {
	// Name identifies a plugin registered with the compiler.
	Name string
	// Enabled includes the attachment when true; a false route attachment also
	// disables an inherited same-named service plugin.
	Enabled bool
	// RawConfig contains the plugin's JSON configuration and remains owned by
	// the resource set.
	RawConfig json.RawMessage
}

// Upstream defines endpoint selection, transport, health, and retry policy for
// one reusable upstream.
type Upstream struct {
	// ID uniquely identifies the upstream within a resource set.
	ID string
	// Endpoints contains the upstream targets and their selection weights.
	Endpoints []Endpoint
	// Balancer selects the endpoint-selection policy.
	Balancer BalancerPolicy
	// Transport configures connection pooling and transport timeouts.
	Transport TransportConfig
	// Health configures active and passive endpoint health tracking.
	Health HealthPolicy
	// Retry configures replay-safe attempts, retry classification, budgets, and
	// a gateway-owned total deadline.
	Retry RetryPolicy
}

// HealthPolicy groups optional active and passive endpoint health policies.
// Passive health requires an active policy so an unhealthy endpoint can
// recover without request traffic.
type HealthPolicy struct {
	// Active configures scheduled HTTP or TCP probes; nil disables active
	// health checks.
	Active *ActiveHealthPolicy
	// Passive configures classification of proxied request outcomes; nil
	// disables passive observations.
	Passive *PassiveHealthPolicy
}

// ActiveHealthPolicy configures scheduled endpoint probes and state-transition
// thresholds. Duration fields use time.Duration.
type ActiveHealthPolicy struct {
	// Type selects HTTP status probing or TCP reachability probing.
	Type HealthCheckType
	// Timeout bounds one probe.
	Timeout time.Duration
	// HealthyInterval is the delay between probes while an endpoint is unknown
	// or healthy.
	HealthyInterval time.Duration
	// UnhealthyInterval is the delay between probes while an endpoint is
	// unhealthy.
	UnhealthyInterval time.Duration
	// HealthySuccesses is the consecutive active-success threshold for marking
	// an endpoint healthy.
	HealthySuccesses uint8
	// HTTPFailures is the active HTTP-failure threshold for marking an endpoint
	// unhealthy.
	HTTPFailures uint8
	// TransportFailures is the active transport-failure threshold for marking
	// an endpoint unhealthy.
	TransportFailures uint8
	// Timeouts is the active timeout threshold for marking an endpoint
	// unhealthy.
	Timeouts uint8
	// HealthyStatuses lists HTTP response status codes classified as active
	// successes.
	HealthyStatuses []uint16
	// UnhealthyStatuses lists HTTP response status codes classified as active
	// failures.
	UnhealthyStatuses []uint16
	// Path is the absolute request path used by HTTP probes.
	Path string
	// Host optionally overrides the Host header used by HTTP probes.
	Host string
}

// PassiveHealthPolicy classifies proxied request outcomes and supplies
// thresholds for marking an endpoint unhealthy.
type PassiveHealthPolicy struct {
	// HTTPFailures is the passive HTTP-failure threshold.
	HTTPFailures uint8
	// TransportFailures is the passive transport-failure threshold.
	TransportFailures uint8
	// Timeouts is the passive timeout threshold.
	Timeouts uint8
	// UnhealthyStatuses lists proxied HTTP response status codes classified as
	// passive failures.
	UnhealthyStatuses []uint16
}

// RetryOnPolicy identifies transport outcomes and HTTP statuses that permit a
// retry when all other retry-safety conditions hold.
type RetryOnPolicy struct {
	// ConnectFailure permits retrying a failure to establish a connection.
	ConnectFailure bool
	// ConnectionFailure permits retrying an established connection that fails
	// before valid response headers arrive.
	ConnectionFailure bool
	// ResponseHeaderTimeout permits retrying a timeout while waiting for
	// response headers.
	ResponseHeaderTimeout bool
	// Statuses lists HTTP response status codes that permit a retry.
	Statuses []uint16
}

// RetryBudgetPolicy bounds retry amplification for one upstream. A zero value
// disables retry-budget acquisition.
type RetryBudgetPolicy struct {
	// RatioPer1000 adds this many fixed-point credits per primary request; 1000
	// credits permit one retry.
	RatioPer1000 uint16
	// Burst is the maximum whole-retry capacity accumulated by the token
	// bucket; a new budget starts empty.
	Burst uint16
	// MaxInflight is the maximum number of concurrent retry attempts.
	MaxInflight uint16
}

// RetryPolicy configures the effective attempt count, replay-safe method set,
// retry classification, budget, and total request timeout for an upstream.
type RetryPolicy struct {
	// MaxAttempts includes the primary attempt; a value of one disables retry.
	MaxAttempts uint8
	// Methods lists HTTP methods eligible for retry after replayability checks.
	Methods []string
	// RetryOn identifies outcomes that permit another attempt.
	RetryOn RetryOnPolicy
	// Budget limits retry rate and concurrency.
	Budget RetryBudgetPolicy
	// TotalTimeout bounds the complete upstream transaction; zero disables the
	// gateway-owned total deadline.
	TotalTimeout time.Duration
}

// RouteResiliencePolicy contains optional route-level replacements for
// selected fields of an upstream retry policy. A nil pointer inherits the
// upstream field; pointed-to slices replace rather than merge.
type RouteResiliencePolicy struct {
	// TotalTimeout replaces the upstream total timeout when non-nil; a zero
	// value behind the pointer disables the gateway-owned total deadline.
	TotalTimeout *time.Duration
	// MaxAttempts replaces the upstream attempt count when non-nil and includes
	// the primary attempt.
	MaxAttempts *uint8
	// Methods replaces the upstream retry-eligible method list when non-nil.
	Methods *[]string
	// RetryOn replaces the upstream retry classification when non-nil.
	RetryOn *RetryOnPolicy
}

// Endpoint identifies one upstream target and its relative selection weight.
type Endpoint struct {
	// URL is the endpoint's canonical HTTP URL after normalization.
	URL string
	// Weight is the relative balancer weight; zero preserves endpoint identity
	// while disabling selection.
	Weight uint32
}

// BalancerPolicy selects an algorithm and its optional consistent-hash key.
type BalancerPolicy struct {
	// Type identifies the endpoint-selection algorithm.
	Type BalancerType
	// HashKey configures compound-key extraction for consistent hashing.
	HashKey HashKeyPolicy
}

// HashKeyPolicy defines an ordered compound key for consistent hashing.
type HashKeyPolicy struct {
	// Sources is evaluated in order; source order participates in key
	// formation.
	Sources []HashKeySource
}

// HashKeySource describes one ordered component of a consistent-hash key.
type HashKeySource struct {
	// Type identifies how the component is obtained.
	Type HashSourceType
	// Name identifies the header or cookie for named dynamic sources.
	Name string
	// Value supplies the component for a literal source.
	Value string
}

// TransportConfig defines connection-pool identity and HTTP transport
// timeouts for an upstream. Duration fields use time.Duration.
type TransportConfig struct {
	// DialTimeout bounds connection establishment.
	DialTimeout time.Duration
	// ResponseHeaderTimeout bounds the wait for upstream response headers.
	ResponseHeaderTimeout time.Duration
	// IdleConnectionTimeout is the maximum duration an idle pooled connection
	// remains reusable.
	IdleConnectionTimeout time.Duration
	// MaxIdleConnections is the maximum number of idle pooled connections
	// across all endpoint hosts.
	MaxIdleConnections int
	// MaxIdleConnectionsPerHost is the maximum number of idle pooled
	// connections retained for one endpoint host.
	MaxIdleConnectionsPerHost int
}

// CloneResourceSet returns a deep, independently mutable copy of in. It clones
// nested slices, pointer policies, and plugin configuration bytes without
// validating or normalizing their contents.
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
