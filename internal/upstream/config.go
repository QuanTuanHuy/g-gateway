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
	"unicode"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

const (
	MaxUpstreams          = 10_000
	MaxSnapshotEndpoints  = 100_000
	MaxUpstreamEndpoints  = 1_000
	MaxEndpointWeight     = 1_000
	MaxHashKeySources     = 8
	MaxWRRSchedule        = 8_192
	MaxSnapshotWRRSlots   = 8_000_000
	MaxContinuumPoints    = 65_536
	MaxSnapshotHashPoints = 8_000_000

	maxHashSourceNameBytes  = 256
	maxHashSourceValueBytes = 4_096
)

type ConfigError struct {
	Code       string
	Field      string
	UpstreamID string
	Cause      error
}

func (e *ConfigError) Error() string {
	if e.Cause == nil {
		return e.Code + ": " + e.Field
	}
	return e.Code + ": " + e.Field + ": " + e.Cause.Error()
}

func (e *ConfigError) Unwrap() error {
	return e.Cause
}

type endpointConfig struct {
	endpoint model.Endpoint
	identity string
	index    int
}

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
		if err := validateTransport(resource, upstreamField); err != nil {
			return nil, err
		}
		normalized[upstreamIndex] = resource
	}
	return normalized, nil
}

func normalizeEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" {
		return "", fmt.Errorf("scheme http is required")
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
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", fmt.Errorf("port must be in 1..65535")
	}

	normalizedHost, err := normalizeHost(host)
	if err != nil {
		return "", err
	}
	return "http://" + net.JoinHostPort(normalizedHost, strconv.FormatUint(portNumber, 10)), nil
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

func validateTransport(resource model.Upstream, upstreamField string) error {
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
