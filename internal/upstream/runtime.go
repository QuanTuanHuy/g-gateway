package upstream

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type Runtime struct {
	endpoint  *endpointRuntime
	transport *transportRuntime
}

func New(resource model.Upstream) (*Runtime, error) {
	if len(resource.Endpoints) != 1 {
		return nil, fmt.Errorf("upstream %q: expected exactly one endpoint, got %d", resource.ID, len(resource.Endpoints))
	}
	endpoint, err := newEndpointRuntime(resource.ID, resource.Endpoints[0])
	if err != nil {
		return nil, err
	}
	return &Runtime{
		endpoint:  endpoint,
		transport: newTransportRuntime(resource.Transport),
	}, nil
}

func (r *Runtime) Target() *url.URL {
	copy := *r.endpoint.target
	return &copy
}

func (r *Runtime) RoundTripper() http.RoundTripper {
	return r.transport.transport
}

func (r *Runtime) CloseIdleConnections() {
	r.transport.CloseIdleConnections()
}
