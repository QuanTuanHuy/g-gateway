package upstream

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func TestUpstreamTLSVerificationMatrix(t *testing.T) {
	pki := newUpstreamTestPKI(t, "root")
	serverCertificatePEM, serverKeyPEM := pki.issue(t, certificateRequest{
		commonName: "orders.internal",
		dnsNames:   []string{"orders.internal", "localhost"},
		ipAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
		},
		usages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	server := startUpstreamTLSServer(t, upstreamTLSServerOptions{
		certificatePEM: serverCertificatePEM,
		privateKeyPEM:  serverKeyPEM,
		nextProtocols:  []string{"http/1.1"},
	}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "ok")
	}))
	trust := mustTrustBundle(t, "root", pki.rootPEM)

	tests := []struct {
		name       string
		profile    transportProfile
		wantClass  TLSFailureClass
		wantStatus int
	}{
		{
			name:       "custom CA",
			profile:    integrationTransportProfile(server, model.TransportProtocolHTTP1, trust, nil, ""),
			wantStatus: http.StatusOK,
		},
		{
			name:      "system roots reject private CA",
			profile:   integrationTransportProfile(server, model.TransportProtocolHTTP1, nil, nil, ""),
			wantClass: TLSFailureTrust,
		},
		{
			name: "replacement bundle rejects unrelated root",
			profile: integrationTransportProfile(
				server,
				model.TransportProtocolHTTP1,
				mustTrustBundle(t, "unrelated", newUpstreamTestPKI(t, "unrelated").rootPEM),
				nil,
				"",
			),
			wantClass: TLSFailureTrust,
		},
		{
			name:      "hostname mismatch",
			profile:   integrationTransportProfile(server, model.TransportProtocolHTTP1, trust, nil, "wrong.internal"),
			wantClass: TLSFailureHostname,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newTransportRuntime(test.profile, nil)
			defer runtime.CloseIdleConnections()
			response, err := runtime.RoundTrip(mustRequest(t, context.Background(), server))
			if test.wantClass != "" {
				class, ok := TLSFailureClassOf(err)
				if !ok || class != test.wantClass {
					t.Fatalf("RoundTrip error=%T %v, class=(%q,%v)", err, err, class, ok)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status=%d", response.StatusCode)
			}
		})
	}
}

func TestUpstreamTLSServerNameOverrideControlsSNIAndVerification(t *testing.T) {
	pki := newUpstreamTestPKI(t, "root")
	serverCertificatePEM, serverKeyPEM := pki.issue(t, certificateRequest{
		commonName: "orders.internal",
		dnsNames:   []string{"orders.internal"},
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	sni := make(chan string, 1)
	server := startUpstreamTLSServer(t, upstreamTLSServerOptions{
		certificatePEM: serverCertificatePEM,
		privateKeyPEM:  serverKeyPEM,
		nextProtocols:  []string{"http/1.1"},
		getConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			sni <- hello.ServerName
			return nil, nil
		},
	}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	profile := integrationTransportProfile(
		server,
		model.TransportProtocolHTTP1,
		mustTrustBundle(t, "root", pki.rootPEM),
		nil,
		"orders.internal",
	)
	runtime := newTransportRuntime(profile, nil)
	defer runtime.CloseIdleConnections()

	response, err := runtime.RoundTrip(mustRequest(t, context.Background(), server))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got := <-sni; got != "orders.internal" {
		t.Fatalf("SNI=%q", got)
	}
}

func TestUpstreamMTLSAndTLSVersionFailuresAreTyped(t *testing.T) {
	pki := newUpstreamTestPKI(t, "root")
	serverCertificatePEM, serverKeyPEM := pki.issue(t, certificateRequest{
		commonName: "orders.internal",
		dnsNames:   []string{"orders.internal"},
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	clientCertificatePEM, clientKeyPEM := pki.issue(t, certificateRequest{
		commonName: "gateway-client",
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	client := mustClientCertificate(t, "client", clientCertificatePEM, clientKeyPEM)
	trust := mustTrustBundle(t, "root", pki.rootPEM)
	server := startUpstreamTLSServer(t, upstreamTLSServerOptions{
		certificatePEM: serverCertificatePEM,
		privateKeyPEM:  serverKeyPEM,
		nextProtocols:  []string{"http/1.1"},
		clientAuth:     tls.RequireAndVerifyClientCert,
		clientCAs:      pki.pool(),
	}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	success := newTransportRuntime(
		integrationTransportProfile(server, model.TransportProtocolHTTP1, trust, client, "orders.internal"),
		nil,
	)
	response, err := success.RoundTrip(mustRequest(t, context.Background(), server))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	success.CloseIdleConnections()

	tests := []struct {
		name    string
		profile transportProfile
		class   TLSFailureClass
	}{
		{
			name:    "missing client identity",
			profile: integrationTransportProfile(server, model.TransportProtocolHTTP1, trust, nil, "orders.internal"),
			class:   TLSFailureHandshake,
		},
		{
			name: "untrusted client identity",
			profile: integrationTransportProfile(
				server,
				model.TransportProtocolHTTP1,
				trust,
				unrelatedClientCertificate(t),
				"orders.internal",
			),
			class: TLSFailureHandshake,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newTransportRuntime(test.profile, nil)
			defer runtime.CloseIdleConnections()
			_, err := runtime.RoundTrip(mustRequest(t, context.Background(), server))
			class, ok := TLSFailureClassOf(err)
			if !ok || class != test.class {
				t.Fatalf("RoundTrip error=%T %v, class=(%q,%v), want %q", err, err, class, ok, test.class)
			}
		})
	}

	tls11Server := startUpstreamTLSServer(t, upstreamTLSServerOptions{
		certificatePEM: serverCertificatePEM,
		privateKeyPEM:  serverKeyPEM,
		nextProtocols:  []string{"http/1.1"},
		minVersion:     tls.VersionTLS11,
		maxVersion:     tls.VersionTLS11,
	}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	runtime := newTransportRuntime(
		integrationTransportProfile(tls11Server, model.TransportProtocolHTTP1, trust, nil, "orders.internal"),
		nil,
	)
	defer runtime.CloseIdleConnections()
	_, err = runtime.RoundTrip(mustRequest(t, context.Background(), tls11Server))
	class, ok := TLSFailureClassOf(err)
	if !ok || class != TLSFailureHandshake {
		t.Fatalf("TLS 1.1 error=%T %v, class=(%q,%v)", err, err, class, ok)
	}
}

func TestTLSResumptionStaysWithinOneTransportGeneration(t *testing.T) {
	pki := newUpstreamTestPKI(t, "root")
	serverCertificatePEM, serverKeyPEM := pki.issue(t, certificateRequest{
		commonName: "orders.internal",
		dnsNames:   []string{"orders.internal"},
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	firstClientPEM, firstClientKey := pki.issue(t, certificateRequest{
		commonName: "client-one",
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	secondClientPEM, secondClientKey := pki.issue(t, certificateRequest{
		commonName: "client-two",
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	resumed := make(chan bool, 3)
	server := startUpstreamTLSServer(t, upstreamTLSServerOptions{
		certificatePEM: serverCertificatePEM,
		privateKeyPEM:  serverKeyPEM,
		nextProtocols:  []string{"http/1.1"},
		clientAuth:     tls.RequireAndVerifyClientCert,
		clientCAs:      pki.pool(),
		minVersion:     tls.VersionTLS12,
		maxVersion:     tls.VersionTLS12,
	}, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		resumed <- request.TLS.DidResume
		_, _ = io.WriteString(writer, "ok")
	}))
	trust := mustTrustBundle(t, "root", pki.rootPEM)
	first := newTransportRuntime(
		integrationTransportProfile(
			server,
			model.TransportProtocolHTTP1,
			trust,
			mustClientCertificate(t, "client-one", firstClientPEM, firstClientKey),
			"orders.internal",
		),
		nil,
	)
	defer first.CloseIdleConnections()
	doIntegrationRequest(t, first, server)
	first.production.CloseIdleConnections()
	doIntegrationRequest(t, first, server)
	if firstHandshake, secondHandshake := <-resumed, <-resumed; firstHandshake || !secondHandshake {
		t.Fatalf("same generation resumed sequence=(%v,%v)", firstHandshake, secondHandshake)
	}

	rotated := newTransportRuntime(
		integrationTransportProfile(
			server,
			model.TransportProtocolHTTP1,
			trust,
			mustClientCertificate(t, "client-two", secondClientPEM, secondClientKey),
			"orders.internal",
		),
		nil,
	)
	defer rotated.CloseIdleConnections()
	doIntegrationRequest(t, rotated, server)
	if newGenerationResumed := <-resumed; newGenerationResumed {
		t.Fatal("rotated generation reused the prior TLS session")
	}
}

func TestHTTPSActiveHealthUsesProfileAndSeparateProbePool(t *testing.T) {
	pki := newUpstreamTestPKI(t, "root")
	serverCertificatePEM, serverKeyPEM := pki.issue(t, certificateRequest{
		commonName: "orders.internal",
		dnsNames:   []string{"orders.internal"},
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	clientCertificatePEM, clientKeyPEM := pki.issue(t, certificateRequest{
		commonName: "health-client",
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	var connections atomic.Int32
	protocols := make(chan int, 8)
	server := startUpstreamTLSServer(t, upstreamTLSServerOptions{
		certificatePEM: serverCertificatePEM,
		privateKeyPEM:  serverKeyPEM,
		nextProtocols:  []string{"h2", "http/1.1"},
		clientAuth:     tls.RequireAndVerifyClientCert,
		clientCAs:      pki.pool(),
		http2:          true,
		connState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				connections.Add(1)
			}
		},
	}, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		protocols <- request.ProtoMajor
		writer.WriteHeader(http.StatusOK)
	}))
	certificate := mustClientCertificate(t, "client", clientCertificatePEM, clientKeyPEM)
	bundle := mustTrustBundle(t, "root", pki.rootPEM)
	resource := integrationTLSUpstream("orders", server, model.TransportProtocolHTTP2)
	resource.Transport.TLS = &model.UpstreamTLSPolicy{
		TrustBundleRef:       "root",
		ClientCertificateRef: "client",
		ServerName:           "orders.internal",
	}
	resource.Health.Active = &model.ActiveHealthPolicy{
		Type:              model.HealthCheckHTTP,
		Timeout:           time.Second,
		HealthyInterval:   100 * time.Millisecond,
		UnhealthyInterval: 100 * time.Millisecond,
		HealthySuccesses:  1,
		HTTPFailures:      1,
		TransportFailures: 1,
		Timeouts:          1,
		HealthyStatuses:   []uint16{200},
		UnhealthyStatuses: []uint16{500},
		Path:              "/health",
	}
	registry := mustRegistry(t, 64, nil)
	candidate, err := registry.Prepare(model.ResourceSet{
		Upstreams:    []model.Upstream{resource},
		Certificates: []*tlsmaterial.Certificate{certificate},
		TrustBundles: []*tlsmaterial.TrustBundle{bundle},
	})
	if err != nil {
		t.Fatal(err)
	}
	set := candidate.Commit()
	defer set.Retire()
	plan, _ := set.Plan("orders")
	plan.ActivateHealth()
	eventuallyUpstream(t, func() bool {
		return plan.endpoints[0].health.State() == HealthHealthy
	})
	if got := <-protocols; got != 2 {
		t.Fatalf("probe protocol=%d", got)
	}

	selection, err := plan.Select(mustRequest(t, context.Background(), server))
	if err != nil {
		t.Fatal(err)
	}
	response, err := selection.RoundTrip(mustRequest(t, context.Background(), server+"/traffic"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got := <-protocols; got != 2 {
		t.Fatalf("production protocol=%d", got)
	}
	if connections.Load() < 2 {
		t.Fatalf("TLS connections=%d, production and probe pools were not isolated", connections.Load())
	}
}

func TestTCPActiveHealthReportsRawReachabilityOnTLSPort(t *testing.T) {
	pki := newUpstreamTestPKI(t, "root")
	serverCertificatePEM, serverKeyPEM := pki.issue(t, certificateRequest{
		commonName: "orders.internal",
		dnsNames:   []string{"orders.internal"},
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	server := startUpstreamTLSServer(t, upstreamTLSServerOptions{
		certificatePEM: serverCertificatePEM,
		privateKeyPEM:  serverKeyPEM,
	}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	resource := integrationTLSUpstream("orders", server, model.TransportProtocolHTTP1)
	resource.Health.Active = &model.ActiveHealthPolicy{
		Type:              model.HealthCheckTCP,
		Timeout:           time.Second,
		HealthyInterval:   100 * time.Millisecond,
		UnhealthyInterval: 100 * time.Millisecond,
		HealthySuccesses:  1,
		TransportFailures: 1,
		Timeouts:          1,
	}
	registry := mustRegistry(t, 64, nil)
	candidate, err := registry.Prepare(model.ResourceSet{Upstreams: []model.Upstream{resource}})
	if err != nil {
		t.Fatal(err)
	}
	set := candidate.Commit()
	defer set.Retire()
	plan, _ := set.Plan("orders")
	plan.ActivateHealth()
	eventuallyUpstream(t, func() bool {
		return plan.endpoints[0].health.State() == HealthHealthy
	})
}

type certificateRequest struct {
	commonName  string
	dnsNames    []string
	ipAddresses []net.IP
	usages      []x509.ExtKeyUsage
}

type upstreamTestPKI struct {
	root       *x509.Certificate
	privateKey ed25519.PrivateKey
	rootPEM    []byte
	serial     atomic.Int64
}

func newUpstreamTestPKI(t testing.TB, commonName string) *upstreamTestPKI {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &upstreamTestPKI{
		root:       root,
		privateKey: privateKey,
		rootPEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

func (p *upstreamTestPKI) issue(
	t testing.TB,
	request certificateRequest,
) ([]byte, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(p.serial.Add(1) + 1),
		Subject:      pkix.Name{CommonName: request.commonName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  request.usages,
		DNSNames:     request.dnsNames,
		IPAddresses:  request.ipAddresses,
	}
	der, err := x509.CreateCertificate(
		rand.Reader,
		template,
		p.root,
		publicKey,
		p.privateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		p.rootPEM...,
	)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	return certificatePEM, privateKeyPEM
}

func (p *upstreamTestPKI) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(p.root)
	return pool
}

type upstreamTLSServerOptions struct {
	certificatePEM     []byte
	privateKeyPEM      []byte
	nextProtocols      []string
	clientAuth         tls.ClientAuthType
	clientCAs          *x509.CertPool
	minVersion         uint16
	maxVersion         uint16
	http2              bool
	getConfigForClient func(*tls.ClientHelloInfo) (*tls.Config, error)
	connState          func(net.Conn, http.ConnState)
}

func startUpstreamTLSServer(
	t testing.TB,
	options upstreamTLSServerOptions,
	handler http.Handler,
) string {
	t.Helper()
	certificate, err := tls.X509KeyPair(options.certificatePEM, options.privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{certificate},
		NextProtos:         append([]string(nil), options.nextProtocols...),
		ClientAuth:         options.clientAuth,
		ClientCAs:          options.clientCAs,
		MinVersion:         options.minVersion,
		MaxVersion:         options.maxVersion,
		GetConfigForClient: options.getConfigForClient,
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	if options.http2 {
		protocols.SetHTTP2(true)
	}
	server := &http.Server{
		Handler:   handler,
		Protocols: protocols,
		ConnState: options.connState,
	}
	tlsListener := tls.NewListener(listener, tlsConfig)
	go func() {
		_ = server.Serve(tlsListener)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return "https://" + listener.Addr().String()
}

func integrationTransportProfile(
	rawURL string,
	protocol model.TransportProtocol,
	trust *tlsmaterial.TrustBundle,
	client *tlsmaterial.Certificate,
	serverName string,
) transportProfile {
	parsed, _ := url.Parse(rawURL)
	config := validTransportConfig()
	config.Protocol = protocol
	return transportProfile{
		scheme:            parsed.Scheme,
		protocol:          protocol,
		transport:         config,
		trustBundle:       trust,
		clientCertificate: client,
		serverName:        serverName,
	}
}

func integrationTLSUpstream(
	id, rawURL string,
	protocol model.TransportProtocol,
) model.Upstream {
	config := validTransportConfig()
	config.Protocol = protocol
	return model.Upstream{
		ID:        id,
		Endpoints: []model.Endpoint{{URL: rawURL, Weight: 1}},
		Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
		Transport: config,
	}
}

func mustTrustBundle(t testing.TB, id string, certificatePEM []byte) *tlsmaterial.TrustBundle {
	t.Helper()
	bundle, err := tlsmaterial.NewTrustBundle(id, certificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func mustClientCertificate(
	t testing.TB,
	id string,
	certificatePEM, privateKeyPEM []byte,
) *tlsmaterial.Certificate {
	t.Helper()
	certificate, err := tlsmaterial.NewCertificate(id, certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func unrelatedClientCertificate(t *testing.T) *tlsmaterial.Certificate {
	t.Helper()
	pki := newUpstreamTestPKI(t, "unrelated-client-root")
	certificatePEM, privateKeyPEM := pki.issue(t, certificateRequest{
		commonName: "untrusted-client",
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	return mustClientCertificate(t, "untrusted-client", certificatePEM, privateKeyPEM)
}

func doIntegrationRequest(t *testing.T, runtime *transportRuntime, rawURL string) {
	t.Helper()
	response, err := runtime.RoundTrip(mustRequest(t, context.Background(), rawURL))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}

func eventuallyUpstream(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
