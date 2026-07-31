package upstream

import (
	"fmt"
	"net/url"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type endpointRuntime struct {
	// identity deliberately excludes endpoint weight so weight-only updates
	// can reuse connection and resilience runtime state.
	identity string
	target   *url.URL
}

func newEndpointRuntime(upstreamID string, endpoint model.Endpoint) (*endpointRuntime, error) {
	target, err := url.Parse(endpoint.URL)
	if err != nil {
		return nil, fmt.Errorf("upstream %q endpoint: %w", upstreamID, err)
	}
	if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, fmt.Errorf("upstream %q endpoint: expected HTTP or HTTPS URL with host", upstreamID)
	}
	targetCopy := *target
	return &endpointRuntime{
		// Endpoint identity combines the upstream ID and canonical URL; the
		// upstream boundary prevents equal URLs in different upstreams from
		// sharing mutable runtime state.
		identity: endpointIdentity(upstreamID, endpoint.URL),
		target:   &targetCopy,
	}, nil
}
