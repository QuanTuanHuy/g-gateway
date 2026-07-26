package upstream

import (
	"fmt"
	"net/url"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type endpointRuntime struct {
	identity string
	target   *url.URL
}

func newEndpointRuntime(upstreamID string, endpoint model.Endpoint) (*endpointRuntime, error) {
	target, err := url.Parse(endpoint.URL)
	if err != nil {
		return nil, fmt.Errorf("upstream %q endpoint: %w", upstreamID, err)
	}
	if target.Scheme != "http" || target.Host == "" {
		return nil, fmt.Errorf("upstream %q endpoint: expected HTTP URL with host", upstreamID)
	}
	targetCopy := *target
	return &endpointRuntime{
		identity: endpointIdentity(upstreamID, endpoint.URL),
		target:   &targetCopy,
	}, nil
}
