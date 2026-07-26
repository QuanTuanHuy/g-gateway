package upstream

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type Runtime struct {
	target               *url.URL
	transport            *http.Transport
	closeIdleConnections func()
}

func New(resource model.Upstream) (*Runtime, error) {
	if len(resource.Endpoints) != 1 {
		return nil, fmt.Errorf("upstream %q: expected exactly one endpoint, got %d", resource.ID, len(resource.Endpoints))
	}
	target, err := url.Parse(resource.Endpoints[0].URL)
	if err != nil {
		return nil, fmt.Errorf("upstream %q endpoint: %w", resource.ID, err)
	}
	if target.Scheme != "http" || target.Host == "" {
		return nil, fmt.Errorf("upstream %q endpoint: expected HTTP URL with host", resource.ID)
	}

	dialer := &net.Dialer{
		Timeout:   resource.Transport.DialTimeout,
		KeepAlive: 30 * time.Second,
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     false,
		Protocols:             protocols,
		DisableCompression:    true,
		MaxIdleConns:          resource.Transport.MaxIdleConnections,
		MaxIdleConnsPerHost:   resource.Transport.MaxIdleConnectionsPerHost,
		IdleConnTimeout:       resource.Transport.IdleConnectionTimeout,
		ResponseHeaderTimeout: resource.Transport.ResponseHeaderTimeout,
	}

	return &Runtime{
		target:               target,
		transport:            transport,
		closeIdleConnections: transport.CloseIdleConnections,
	}, nil
}

func (r *Runtime) Target() *url.URL {
	copy := *r.target
	return &copy
}

func (r *Runtime) RoundTripper() http.RoundTripper {
	return r.transport
}

func (r *Runtime) CloseIdleConnections() {
	if r.closeIdleConnections != nil {
		r.closeIdleConnections()
	}
}
