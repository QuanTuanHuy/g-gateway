package upstream

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type transportKey struct {
	dialTimeout               time.Duration
	responseHeaderTimeout     time.Duration
	idleConnectionTimeout     time.Duration
	maxIdleConnections        int
	maxIdleConnectionsPerHost int
	disableCompression        bool
	http1Only                 bool
}

func makeTransportKey(config model.TransportConfig) transportKey {
	return transportKey{
		dialTimeout:               config.DialTimeout,
		responseHeaderTimeout:     config.ResponseHeaderTimeout,
		idleConnectionTimeout:     config.IdleConnectionTimeout,
		maxIdleConnections:        config.MaxIdleConnections,
		maxIdleConnectionsPerHost: config.MaxIdleConnectionsPerHost,
		disableCompression:        true,
		http1Only:                 true,
	}
}

type transportRuntime struct {
	key                  transportKey
	transport            *http.Transport
	closeOnce            sync.Once
	closeIdleConnections func()
}

func newTransportRuntime(config model.TransportConfig) *transportRuntime {
	key := makeTransportKey(config)
	dialer := &net.Dialer{
		Timeout:   key.dialTimeout,
		KeepAlive: 30 * time.Second,
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     false,
		Protocols:             protocols,
		DisableCompression:    key.disableCompression,
		MaxIdleConns:          key.maxIdleConnections,
		MaxIdleConnsPerHost:   key.maxIdleConnectionsPerHost,
		IdleConnTimeout:       key.idleConnectionTimeout,
		ResponseHeaderTimeout: key.responseHeaderTimeout,
	}
	return &transportRuntime{
		key:                  key,
		transport:            transport,
		closeIdleConnections: transport.CloseIdleConnections,
	}
}

func (r *transportRuntime) RoundTrip(request *http.Request) (*http.Response, error) {
	return r.transport.RoundTrip(request)
}

func (r *transportRuntime) CloseIdleConnections() {
	r.closeOnce.Do(func() {
		if r.closeIdleConnections != nil {
			r.closeIdleConnections()
		}
	})
}
