package upstream

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

const tlsTransportPolicyVersion uint8 = 1

// TLSObserver receives bounded upstream TLS connection outcomes.
type TLSObserver interface {
	// ObserveTLSHandshake reports one terminal handshake result and its
	// authentication mode.
	ObserveTLSHandshake(result, mode string, protocol model.TransportProtocol)
	// ObserveTLSFailure reports a stable typed failure class.
	ObserveTLSFailure(class TLSFailureClass)
}

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

func newTransportRuntime(profile transportProfile, observer TLSObserver) *transportRuntime {
	key := makeTransportKey(profile)
	production := newHTTPTransport(profile, key, observer)
	probe := newHTTPTransport(profile, key, observer)
	return &transportRuntime{
		key:                 key,
		production:          production,
		probe:               probe,
		closeProductionIdle: production.CloseIdleConnections,
		closeProbeIdle:      probe.CloseIdleConnections,
	}
}

func newHTTPTransport(
	profile transportProfile,
	key transportKey,
	observer TLSObserver,
) *http.Transport {
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
	transport.DialTLSContext = newVerifiedTLSDialer(
		dialer,
		tlsConfig,
		profile,
		observer,
	)
	return transport
}

func newVerifiedTLSDialer(
	dialer *net.Dialer,
	baseTLS *tls.Config,
	profile transportProfile,
	observer TLSObserver,
) func(context.Context, string, string) (net.Conn, error) {
	mode := "server_auth"
	mtls := profile.clientCertificate != nil
	if mtls {
		mode = "mtls"
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		serverName := profile.serverName
		if serverName == "" {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			serverName = host
		}
		raw, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}

		attemptTLS := baseTLS.Clone()
		attemptTLS.ServerName = serverName
		connection := tls.Client(raw, attemptTLS)
		if err := connection.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			observeTLSHandshake(observer, "failure", mode, profile.protocol)
			if failure, classified := classifyTLSFailure(err, mtls); classified {
				observeTLSFailure(observer, failure.Class)
				return nil, failure
			}
			return nil, err
		}
		if profile.protocol == model.TransportProtocolHTTP2 &&
			connection.ConnectionState().NegotiatedProtocol != "h2" {
			_ = connection.Close()
			failure := &TLSFailureError{
				Class: TLSFailureProtocol,
				Err:   errHTTP2Required,
			}
			observeTLSHandshake(observer, "failure", mode, profile.protocol)
			observeTLSFailure(observer, failure.Class)
			return nil, failure
		}
		observeTLSHandshake(observer, "success", mode, profile.protocol)
		return connection, nil
	}
}

func observeTLSHandshake(
	observer TLSObserver,
	result, mode string,
	protocol model.TransportProtocol,
) {
	if observer != nil {
		observer.ObserveTLSHandshake(result, mode, protocol)
	}
}

func observeTLSFailure(observer TLSObserver, class TLSFailureClass) {
	if observer != nil {
		observer.ObserveTLSFailure(class)
	}
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
