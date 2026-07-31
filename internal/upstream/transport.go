package upstream

import (
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

const tlsTransportPolicyVersion uint8 = 1

type transportKey struct {
	scheme                    string
	serverName                string
	protocol                  model.TransportProtocol
	dialTimeout               time.Duration
	responseHeaderTimeout     time.Duration
	idleConnectionTimeout     time.Duration
	maxIdleConnections        int
	maxIdleConnectionsPerHost int
	tlsEnabled                bool
	tlsPolicyVersion          uint8
	trustSystem               bool
	trustFingerprint          tlsmaterial.Fingerprint
	clientFingerprint         tlsmaterial.Fingerprint
	minTLSVersion             uint16
	disableCompression        bool
}

func makeTransportKey(profile transportProfile) transportKey {
	key := transportKey{
		scheme:                    profile.scheme,
		serverName:                profile.serverName,
		protocol:                  profile.protocol,
		dialTimeout:               profile.transport.DialTimeout,
		responseHeaderTimeout:     profile.transport.ResponseHeaderTimeout,
		idleConnectionTimeout:     profile.transport.IdleConnectionTimeout,
		maxIdleConnections:        profile.transport.MaxIdleConnections,
		maxIdleConnectionsPerHost: profile.transport.MaxIdleConnectionsPerHost,
		disableCompression:        true,
	}
	if profile.scheme == "https" {
		key.tlsEnabled = true
		key.tlsPolicyVersion = tlsTransportPolicyVersion
		key.trustSystem = profile.trustBundle == nil
		key.minTLSVersion = tls.VersionTLS12
		if profile.trustBundle != nil {
			key.trustFingerprint = profile.trustBundle.Fingerprint()
		}
		if profile.clientCertificate != nil {
			key.clientFingerprint = profile.clientCertificate.Fingerprint()
		}
	}
	return key
}

type transportRuntime struct {
	key                 transportKey
	production          *http.Transport
	probe               *http.Transport
	closeOnce           sync.Once
	closeProductionIdle func()
	closeProbeIdle      func()
}

func newTransportRuntime(profile transportProfile) *transportRuntime {
	key := makeTransportKey(profile)
	production := newHTTPTransport(profile, key)
	probe := newHTTPTransport(profile, key)
	return &transportRuntime{
		key:                 key,
		production:          production,
		probe:               probe,
		closeProductionIdle: production.CloseIdleConnections,
		closeProbeIdle:      probe.CloseIdleConnections,
	}
}

func newHTTPTransport(profile transportProfile, key transportKey) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   key.dialTimeout,
		KeepAlive: 30 * time.Second,
	}
	protocols := new(http.Protocols)
	switch {
	case profile.scheme == "http" && profile.protocol == model.TransportProtocolHTTP2:
		protocols.SetUnencryptedHTTP2(true)
	case profile.scheme == "http":
		protocols.SetHTTP1(true)
	case profile.protocol == model.TransportProtocolAuto:
		protocols.SetHTTP1(true)
		protocols.SetHTTP2(true)
	case profile.protocol == model.TransportProtocolHTTP1:
		protocols.SetHTTP1(true)
	case profile.protocol == model.TransportProtocolHTTP2:
		protocols.SetHTTP2(true)
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     profile.scheme == "https" && profile.protocol != model.TransportProtocolHTTP1,
		Protocols:             protocols,
		DisableCompression:    key.disableCompression,
		MaxIdleConns:          key.maxIdleConnections,
		MaxIdleConnsPerHost:   key.maxIdleConnectionsPerHost,
		IdleConnTimeout:       key.idleConnectionTimeout,
		ResponseHeaderTimeout: key.responseHeaderTimeout,
	}
	if profile.scheme != "https" {
		return transport
	}
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ClientSessionCache: tls.NewLRUClientSessionCache(64),
		ServerName:         profile.serverName,
	}
	switch profile.protocol {
	case model.TransportProtocolAuto:
		tlsConfig.NextProtos = []string{"h2", "http/1.1"}
	case model.TransportProtocolHTTP1:
		tlsConfig.NextProtos = []string{"http/1.1"}
	case model.TransportProtocolHTTP2:
		tlsConfig.NextProtos = []string{"h2"}
	}
	if profile.trustBundle != nil {
		tlsConfig.RootCAs = profile.trustBundle.CertPool()
	}
	if profile.clientCertificate != nil {
		tlsConfig.Certificates = []tls.Certificate{
			profile.clientCertificate.TLSCertificate(),
		}
	}
	transport.TLSClientConfig = tlsConfig
	return transport
}

// RoundTrip sends one request through the production upstream connection pool.
func (r *transportRuntime) RoundTrip(request *http.Request) (*http.Response, error) {
	return r.production.RoundTrip(request)
}

// ProbeTransport returns the independently pooled transport reserved for
// active HTTP health checks.
func (r *transportRuntime) ProbeTransport() http.RoundTripper {
	return r.probe
}

// CloseIdleConnections idempotently closes idle connections owned by the
// transport runtime.
func (r *transportRuntime) CloseIdleConnections() {
	r.closeOnce.Do(func() {
		if r.closeProductionIdle != nil {
			r.closeProductionIdle()
		}
		if r.closeProbeIdle != nil {
			r.closeProbeIdle()
		}
	})
}
