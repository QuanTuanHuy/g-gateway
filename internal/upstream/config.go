package upstream

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

const (
	// MaxUpstreams is the maximum number of upstream resources accepted in one
	// normalized snapshot.
	MaxUpstreams = 10_000
	// MaxSnapshotEndpoints is the maximum total endpoint count across one
	// normalized snapshot.
	MaxSnapshotEndpoints = 100_000
	// MaxUpstreamEndpoints is the maximum endpoint count in one upstream.
	MaxUpstreamEndpoints = 1_000
	// MaxEndpointWeight is the maximum relative weight of one endpoint.
	MaxEndpointWeight = 1_000
	// MaxHashKeySources is the maximum number of ordered components in one
	// consistent-hash key.
	MaxHashKeySources = 8
	// MaxWRRSchedule is the maximum number of slots in one weighted
	// round-robin schedule.
	MaxWRRSchedule = 8_192
	// MaxSnapshotWRRSlots is the maximum aggregate weighted round-robin slot
	// budget reserved for one snapshot.
	MaxSnapshotWRRSlots = 8_000_000
	// MaxContinuumPoints is the maximum number of virtual hash points in one
	// consistent-hash continuum.
	MaxContinuumPoints = 65_536
	// MaxSnapshotHashPoints is the maximum aggregate consistent-hash point
	// budget reserved for one snapshot.
	MaxSnapshotHashPoints = 8_000_000

	maxHashSourceNameBytes  = 256
	maxHashSourceValueBytes = 4_096
)

// ConfigError is a stable coded configuration error associated with one field
// and, when available, one upstream resource.
type ConfigError struct {
	// Code is the stable machine-readable error category.
	Code string
	// Field is the canonical path of the invalid configuration value.
	Field string
	// UpstreamID identifies the affected upstream, or is empty for a
	// collection-wide failure.
	UpstreamID string
	// Cause contains the underlying validation detail, when one exists.
	Cause error
}

// Error returns the stable code, field path, and optional cause text.
func (e *ConfigError) Error() string {
	if e.Cause == nil {
		return e.Code + ": " + e.Field
	}
	return e.Code + ": " + e.Field + ": " + e.Cause.Error()
}

// Unwrap returns the underlying validation cause.
func (e *ConfigError) Unwrap() error {
	return e.Cause
}

type endpointConfig struct {
	endpoint model.Endpoint
	identity string
	index    int
}

// Normalize validates resources and returns a canonical top-level slice. It
// canonicalizes endpoint URLs, applies balancer and legacy retry defaults,
// normalizes ordered sets, sorts endpoints deterministically, rejects duplicate
// identities, and enforces per-upstream and snapshot-wide budgets. Normalize
// may reorder or rewrite nested slices and pointed-to policies reachable from
// resources; callers that must preserve the input should clone it first. On
// error, the returned slice is nil and no partial result is usable.
func Normalize(resources []model.Upstream) ([]model.Upstream, error) {
	if len(resources) > MaxUpstreams {
		return nil, configError("UPSTREAM_ENDPOINT_LIMIT", "upstreams", "", fmt.Errorf("got %d upstreams, maximum is %d", len(resources), MaxUpstreams))
	}

	normalized := make([]model.Upstream, len(resources))
	seenUpstreamIDs := make(map[string]struct{}, len(resources))
	totalEndpoints := 0
	for upstreamIndex := range resources {
		resource := resources[upstreamIndex]
		upstreamField := fmt.Sprintf("upstreams[%d]", upstreamIndex)
		if resource.ID == "" || strings.TrimSpace(resource.ID) != resource.ID {
			return nil, configError("UPSTREAM_ID_INVALID", upstreamField+".id", resource.ID, fmt.Errorf("must be non-empty without surrounding whitespace"))
		}
		if _, duplicate := seenUpstreamIDs[resource.ID]; duplicate {
			return nil, configError("UPSTREAM_ID_DUPLICATE", upstreamField+".id", resource.ID, fmt.Errorf("duplicate upstream ID"))
		}
		seenUpstreamIDs[resource.ID] = struct{}{}

		if len(resource.Endpoints) == 0 {
			return nil, configError("UPSTREAM_ENDPOINTS_EMPTY", upstreamField+".endpoints", resource.ID, nil)
		}
		if len(resource.Endpoints) > MaxUpstreamEndpoints {
			return nil, configError("UPSTREAM_ENDPOINT_LIMIT", upstreamField+".endpoints", resource.ID, fmt.Errorf("got %d endpoints, maximum is %d", len(resource.Endpoints), MaxUpstreamEndpoints))
		}
		totalEndpoints += len(resource.Endpoints)
		if totalEndpoints > MaxSnapshotEndpoints {
			return nil, configError("UPSTREAM_ENDPOINT_LIMIT", upstreamField+".endpoints", resource.ID, fmt.Errorf("snapshot endpoints exceed %d", MaxSnapshotEndpoints))
		}

		endpoints := make([]endpointConfig, len(resource.Endpoints))
		activeEndpoints := 0
		activeScheme := ""
		seenEndpointIDs := make(map[string]struct{}, len(resource.Endpoints))
		for endpointIndex, endpoint := range resource.Endpoints {
			field := fmt.Sprintf("%s.endpoints[%d]", upstreamField, endpointIndex)
			if endpoint.Weight > MaxEndpointWeight {
				return nil, configError("UPSTREAM_WEIGHT_INVALID", field+".weight", resource.ID, fmt.Errorf("got %d, maximum is %d", endpoint.Weight, MaxEndpointWeight))
			}
			if endpoint.Weight > 0 {
				activeEndpoints++
			}
			canonicalURL, err := normalizeEndpoint(endpoint.URL)
			if err != nil {
				return nil, configError("UPSTREAM_ENDPOINT_INVALID", field+".url", resource.ID, err)
			}
			if endpoint.Weight > 0 {
				parsed, parseErr := url.Parse(canonicalURL)
				if parseErr != nil {
					return nil, configError("UPSTREAM_ENDPOINT_INVALID", field+".url", resource.ID, parseErr)
				}
				if activeScheme == "" {
					activeScheme = parsed.Scheme
				} else if activeScheme != parsed.Scheme {
					return nil, configError("UPSTREAM_SCHEME_MIXED", upstreamField+".endpoints", resource.ID, fmt.Errorf("positive-weight endpoints must use one scheme"))
				}
			}
			identity := endpointIdentity(resource.ID, canonicalURL)
			if _, duplicate := seenEndpointIDs[identity]; duplicate {
				return nil, configError("UPSTREAM_ENDPOINT_DUPLICATE", field+".url", resource.ID, nil)
			}
			seenEndpointIDs[identity] = struct{}{}
			endpoint.URL = canonicalURL
			endpoints[endpointIndex] = endpointConfig{
				endpoint: endpoint,
				identity: identity,
				index:    endpointIndex,
			}
		}
		if activeEndpoints == 0 {
			return nil, configError("UPSTREAM_NO_ACTIVE_ENDPOINT", upstreamField+".endpoints", resource.ID, nil)
		}
		sort.Slice(endpoints, func(i, j int) bool {
			return endpoints[i].identity < endpoints[j].identity
		})

		resource.Endpoints = make([]model.Endpoint, len(endpoints))
		for endpointIndex := range endpoints {
			resource.Endpoints[endpointIndex] = endpoints[endpointIndex].endpoint
		}
		if err := normalizeBalancer(&resource, upstreamField); err != nil {
			return nil, err
		}
		if err := validateTransport(&resource, activeScheme, upstreamField); err != nil {
			return nil, err
		}
		if err := normalizeHealth(&resource, upstreamField+".health"); err != nil {
			return nil, err
		}
		if err := normalizeRetry(&resource, upstreamField+".retry"); err != nil {
			return nil, err
		}
		normalized[upstreamIndex] = resource
	}
	return normalized, nil
}

func normalizeHealth(resource *model.Upstream, field string) error {
	policy := &resource.Health
	if policy.Active == nil {
		if policy.Passive != nil {
			return configError("PASSIVE_HEALTH_REQUIRES_ACTIVE", field+".passive", resource.ID, nil)
		}
		return nil
	}

	active := policy.Active
	if active.Timeout < 10*time.Millisecond || active.Timeout > 30*time.Second ||
		active.HealthyInterval < 100*time.Millisecond || active.HealthyInterval > time.Hour ||
		active.UnhealthyInterval < 100*time.Millisecond || active.UnhealthyInterval > time.Hour ||
		!validThreshold(active.HealthySuccesses) ||
		!validThreshold(active.TransportFailures) ||
		!validThreshold(active.Timeouts) {
		return configError("ACTIVE_HEALTH_INVALID", field+".active", resource.ID, fmt.Errorf("timeout, intervals, or thresholds are outside allowed bounds"))
	}

	switch active.Type {
	case model.HealthCheckHTTP:
		if !validThreshold(active.HTTPFailures) ||
			len(active.HealthyStatuses) == 0 || len(active.UnhealthyStatuses) == 0 ||
			active.Path == "" || !strings.HasPrefix(active.Path, "/") {
			return configError("ACTIVE_HEALTH_INVALID", field+".active", resource.ID, fmt.Errorf("HTTP checker requires thresholds, statuses, and an absolute path"))
		}
		active.HealthyStatuses = normalizeStatuses(active.HealthyStatuses)
		active.UnhealthyStatuses = normalizeStatuses(active.UnhealthyStatuses)
		if statusesOverlap(active.HealthyStatuses, active.UnhealthyStatuses) {
			return configError("ACTIVE_HEALTH_INVALID", field+".active", resource.ID, fmt.Errorf("healthy and unhealthy statuses must be disjoint"))
		}
	case model.HealthCheckTCP:
		if active.HTTPFailures != 0 || active.Path != "" || active.Host != "" ||
			len(active.HealthyStatuses) != 0 || len(active.UnhealthyStatuses) != 0 {
			return configError("ACTIVE_HEALTH_INVALID", field+".active", resource.ID, fmt.Errorf("TCP checker does not accept HTTP fields"))
		}
	default:
		return configError("ACTIVE_HEALTH_INVALID", field+".active.type", resource.ID, fmt.Errorf("unsupported type %q", active.Type))
	}

	if policy.Passive != nil {
		passive := policy.Passive
		if passive.HTTPFailures == 0 && passive.TransportFailures == 0 && passive.Timeouts == 0 {
			return configError("PASSIVE_HEALTH_INVALID", field+".passive", resource.ID, fmt.Errorf("at least one threshold is required"))
		}
		if !validOptionalThreshold(passive.HTTPFailures) ||
			!validOptionalThreshold(passive.TransportFailures) ||
			!validOptionalThreshold(passive.Timeouts) {
			return configError("PASSIVE_HEALTH_INVALID", field+".passive", resource.ID, fmt.Errorf("thresholds must be in 1..254 when enabled"))
		}
		passive.UnhealthyStatuses = normalizeStatuses(passive.UnhealthyStatuses)
		if passive.HTTPFailures > 0 && len(passive.UnhealthyStatuses) == 0 {
			return configError("PASSIVE_HEALTH_INVALID", field+".passive.unhealthy_statuses", resource.ID, fmt.Errorf("HTTP failures require statuses"))
		}
	}
	return nil
}

func normalizeRetry(resource *model.Upstream, field string) error {
	policy := &resource.Retry
	if retryPolicyIsLegacy(*policy) {
		*policy = model.RetryPolicy{MaxAttempts: 1}
		return nil
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > 5 ||
		(policy.TotalTimeout != 0 && (policy.TotalTimeout < time.Millisecond || policy.TotalTimeout > 10*time.Minute)) ||
		policy.Budget.RatioPer1000 > 1000 ||
		policy.Budget.Burst < 1 || policy.Budget.Burst > 1000 ||
		policy.Budget.MaxInflight < 1 || policy.Budget.MaxInflight > 1000 {
		return configError("RETRY_POLICY_INVALID", field, resource.ID, fmt.Errorf("retry policy is outside allowed bounds"))
	}
	for i := range policy.Methods {
		method := strings.ToUpper(policy.Methods[i])
		if !validToken(method) {
			return configError("RETRY_POLICY_INVALID", fmt.Sprintf("%s.methods[%d]", field, i), resource.ID, fmt.Errorf("invalid HTTP method"))
		}
		policy.Methods[i] = method
	}
	sort.Strings(policy.Methods)
	policy.Methods = compactStrings(policy.Methods)
	policy.RetryOn.Statuses = normalizeStatuses(policy.RetryOn.Statuses)
	for i, status := range policy.RetryOn.Statuses {
		if status != 408 && status != 425 && status != 429 && (status < 500 || status > 599) {
			return configError("RETRY_STATUS_INVALID", fmt.Sprintf("%s.retry_on.statuses[%d]", field, i), resource.ID, fmt.Errorf("status %d is not retryable", status))
		}
	}
	return nil
}

func retryPolicyIsLegacy(policy model.RetryPolicy) bool {
	return (policy.MaxAttempts == 0 || policy.MaxAttempts == 1) &&
		len(policy.Methods) == 0 &&
		!policy.RetryOn.ConnectFailure &&
		!policy.RetryOn.ConnectionFailure &&
		!policy.RetryOn.ResponseHeaderTimeout &&
		len(policy.RetryOn.Statuses) == 0 &&
		policy.Budget == (model.RetryBudgetPolicy{}) && policy.TotalTimeout == 0
}

func validThreshold(value uint8) bool {
	return value >= 1 && value <= 254
}

func validOptionalThreshold(value uint8) bool {
	return value == 0 || validThreshold(value)
}

func normalizeStatuses(values []uint16) []uint16 {
	if values == nil {
		return nil
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return compactUint16s(values)
}

func compactUint16s(values []uint16) []uint16 {
	if len(values) < 2 {
		return values
	}
	write := 1
	for read := 1; read < len(values); read++ {
		if values[read] != values[write-1] {
			values[write] = values[read]
			write++
		}
	}
	return values[:write]
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	write := 1
	for read := 1; read < len(values); read++ {
		if values[read] != values[write-1] {
			values[write] = values[read]
			write++
		}
	}
	return values[:write]
}

func statusesOverlap(left, right []uint16) bool {
	i, j := 0, 0
	for i < len(left) && j < len(right) {
		if left[i] == right[j] {
			return true
		}
		if left[i] < right[j] {
			i++
		} else {
			j++
		}
	}
	return false
}

func normalizeEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("scheme http or https is required")
	}
	if parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("URL may contain only scheme and host")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("path must be empty or /")
	}
	if parsed.RawPath != "" {
		return "", fmt.Errorf("escaped path is not allowed")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("host is required")
	}

	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("host is required")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return "", fmt.Errorf("port is required after colon")
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
		if parsed.Scheme == "https" {
			port = "443"
		}
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", fmt.Errorf("port must be in 1..65535")
	}

	normalizedHost, err := normalizeHost(host)
	if err != nil {
		return "", err
	}
	return parsed.Scheme + "://" + net.JoinHostPort(normalizedHost, strconv.FormatUint(portNumber, 10)), nil
}

func normalizeHost(host string) (string, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		if address.Zone() != "" {
			return "", fmt.Errorf("scoped IP addresses are not supported")
		}
		return address.Unmap().String(), nil
	}
	for _, character := range host {
		if character > unicode.MaxASCII {
			return "", fmt.Errorf("DNS host must be ASCII")
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(host) == 0 || len(host) > 253 {
		return "", fmt.Errorf("DNS host length is invalid")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("DNS label is invalid")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') &&
				character != '-' {
				return "", fmt.Errorf("DNS label is invalid")
			}
		}
	}
	return host, nil
}

func endpointIdentity(upstreamID, canonicalURL string) string {
	return upstreamID + "\x00" + canonicalURL
}

func normalizeBalancer(resource *model.Upstream, upstreamField string) error {
	if resource.Balancer.Type == "" {
		resource.Balancer.Type = model.BalancerWeightedRoundRobin
	}
	sourcesField := upstreamField + ".balancer.hash_key.sources"
	switch resource.Balancer.Type {
	case model.BalancerWeightedRoundRobin:
		if len(resource.Balancer.HashKey.Sources) != 0 {
			return configError("HASH_KEY_INVALID", sourcesField, resource.ID, fmt.Errorf("weighted round-robin does not accept a hash key"))
		}
	case model.BalancerConsistentHash:
		if len(resource.Balancer.HashKey.Sources) == 0 || len(resource.Balancer.HashKey.Sources) > MaxHashKeySources {
			return configError("HASH_KEY_INVALID", sourcesField, resource.ID, fmt.Errorf("consistent hash requires 1..%d sources", MaxHashKeySources))
		}
		for sourceIndex := range resource.Balancer.HashKey.Sources {
			if err := normalizeHashSource(&resource.Balancer.HashKey.Sources[sourceIndex], fmt.Sprintf("%s[%d]", sourcesField, sourceIndex), resource.ID); err != nil {
				return err
			}
		}
	default:
		return configError("BALANCER_TYPE_INVALID", upstreamField+".balancer.type", resource.ID, fmt.Errorf("unsupported type %q", resource.Balancer.Type))
	}
	return nil
}

func normalizeHashSource(source *model.HashKeySource, field, upstreamID string) error {
	if len(source.Name) > maxHashSourceNameBytes || len(source.Value) > maxHashSourceValueBytes {
		return configError("HASH_KEY_INVALID", field, upstreamID, fmt.Errorf("source exceeds byte limit"))
	}
	switch source.Type {
	case model.HashSourceHeader:
		if !validToken(source.Name) || source.Value != "" {
			return configError("HASH_KEY_INVALID", field, upstreamID, fmt.Errorf("header requires only a valid name"))
		}
		source.Name = http.CanonicalHeaderKey(source.Name)
	case model.HashSourceCookie:
		if !validToken(source.Name) || source.Value != "" {
			return configError("HASH_KEY_INVALID", field, upstreamID, fmt.Errorf("cookie requires only a valid name"))
		}
	case model.HashSourceRemoteAddr:
		if source.Name != "" || source.Value != "" {
			return configError("HASH_KEY_INVALID", field, upstreamID, fmt.Errorf("remote_addr accepts neither name nor value"))
		}
	case model.HashSourceLiteral:
		if source.Name != "" || source.Value == "" {
			return configError("HASH_KEY_INVALID", field, upstreamID, fmt.Errorf("literal requires only a non-empty value"))
		}
	default:
		return configError("HASH_KEY_INVALID", field+".type", upstreamID, fmt.Errorf("unsupported source type %q", source.Type))
	}
	return nil
}

func validToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character > unicode.MaxASCII || unicode.IsControl(character) || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", character) {
			return false
		}
	}
	return true
}

func validateTransport(resource *model.Upstream, scheme, upstreamField string) error {
	checks := []struct {
		field string
		valid bool
	}{
		{field: "dial_timeout", valid: resource.Transport.DialTimeout > 0},
		{field: "response_header_timeout", valid: resource.Transport.ResponseHeaderTimeout > 0},
		{field: "idle_connection_timeout", valid: resource.Transport.IdleConnectionTimeout > 0},
		{field: "max_idle_connections", valid: resource.Transport.MaxIdleConnections > 0},
		{field: "max_idle_connections_per_host", valid: resource.Transport.MaxIdleConnectionsPerHost > 0},
	}
	for _, check := range checks {
		if !check.valid {
			return configError("TRANSPORT_PROFILE_INVALID", upstreamField+".transport."+check.field, resource.ID, fmt.Errorf("must be greater than zero"))
		}
	}
	if resource.Transport.Protocol == "" {
		resource.Transport.Protocol = model.TransportProtocolHTTP1
	}
	switch resource.Transport.Protocol {
	case model.TransportProtocolAuto, model.TransportProtocolHTTP1, model.TransportProtocolHTTP2:
	default:
		return configError(
			"TRANSPORT_PROTOCOL_INVALID",
			upstreamField+".transport.protocol",
			resource.ID,
			fmt.Errorf("unsupported protocol %q", resource.Transport.Protocol),
		)
	}
	if scheme == "http" && resource.Transport.TLS != nil {
		return configError(
			"TRANSPORT_TLS_INVALID",
			upstreamField+".transport.tls",
			resource.ID,
			fmt.Errorf("TLS policy requires HTTPS endpoints"),
		)
	}
	if resource.Transport.TLS == nil {
		return nil
	}
	policy := resource.Transport.TLS
	for _, reference := range []struct {
		field string
		value string
	}{
		{field: "trust_bundle_ref", value: policy.TrustBundleRef},
		{field: "client_certificate_ref", value: policy.ClientCertificateRef},
	} {
		if reference.value != "" && strings.TrimSpace(reference.value) != reference.value {
			return configError(
				"TRANSPORT_TLS_INVALID",
				upstreamField+".transport.tls."+reference.field,
				resource.ID,
				fmt.Errorf("reference must not contain surrounding whitespace"),
			)
		}
	}
	if policy.ServerName != "" {
		normalized, err := normalizeHost(policy.ServerName)
		if err != nil {
			return configError(
				"TRANSPORT_TLS_INVALID",
				upstreamField+".transport.tls.server_name",
				resource.ID,
				err,
			)
		}
		policy.ServerName = normalized
	}
	return nil
}

func configError(code, field, upstreamID string, cause error) *ConfigError {
	return &ConfigError{
		Code:       code,
		Field:      field,
		UpstreamID: upstreamID,
		Cause:      cause,
	}
}
