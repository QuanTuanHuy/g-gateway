package integration_test

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/downstreamtls"
	"github.com/QuanTuanHuy/g-gateway/internal/gateway"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	gatewayruntime "github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

type testCertificate struct {
	Material *tlsmaterial.Certificate
	Pool     *x509.CertPool
	Serial   *big.Int
}

func TestDownstreamTLSSelectsDefaultExactAndWildcardCertificates(t *testing.T) {
	now := time.Now()
	defaultCertificate := newTestCertificate(t, "default", 1, []string{"default.example", "unknown.example", "a.b.example.com"}, now)
	exactCertificate := newTestCertificate(t, "exact", 2, []string{"api.example.com"}, now)
	wildcardCertificate := newTestCertificate(t, "wildcard", 3, []string{"*.example.com"}, now)
	instance, addresses, _ := startDownstreamTLSGateway(t, []testCertificate{defaultCertificate, exactCertificate, wildcardCertificate})
	_ = instance
	pool := certificatePool(defaultCertificate, exactCertificate, wildcardCertificate)

	tests := []struct {
		serverName string
		wantSerial int64
	}{
		{serverName: "unknown.example", wantSerial: 1},
		{serverName: "api.example.com", wantSerial: 2},
		{serverName: "shop.example.com", wantSerial: 3},
		{serverName: "a.b.example.com", wantSerial: 1},
	}
	for _, test := range tests {
		t.Run(test.serverName, func(t *testing.T) {
			if got := dialDownstreamTLSSerial(t, addresses.HTTPS, test.serverName, pool, nil); got != test.wantSerial {
				t.Fatalf("peer serial = %d, want %d", got, test.wantSerial)
			}
		})
	}
}

func TestDownstreamTLSRotationAffectsNewConnections(t *testing.T) {
	now := time.Now()
	defaultCertificate := newTestCertificate(t, "default", 1, []string{"default.example"}, now)
	exactCertificate := newTestCertificate(t, "exact", 2, []string{"api.example.com"}, now)
	instance, addresses, resources := startDownstreamTLSGateway(t, []testCertificate{defaultCertificate, exactCertificate})
	pool := certificatePool(defaultCertificate, exactCertificate)
	if got := dialDownstreamTLSSerial(t, addresses.HTTPS, "api.example.com", pool, nil); got != 2 {
		t.Fatalf("initial peer serial = %d, want 2", got)
	}

	rotated := newTestCertificate(t, "exact", 4, []string{"api.example.com"}, now)
	resources.Certificates[1] = rotated.Material
	if err := instance.Apply(2, resources); err != nil {
		t.Fatal(err)
	}
	pool = certificatePool(defaultCertificate, rotated)
	if got := dialDownstreamTLSSerial(t, addresses.HTTPS, "api.example.com", pool, nil); got != 4 {
		t.Fatalf("rotated peer serial = %d, want 4", got)
	}
}

func TestRejectedDownstreamTLSRotationKeepsLastGood(t *testing.T) {
	now := time.Now()
	defaultCertificate := newTestCertificate(t, "default", 1, []string{"default.example"}, now)
	exactCertificate := newTestCertificate(t, "exact", 2, []string{"api.example.com"}, now)
	instance, addresses, resources := startDownstreamTLSGateway(t, []testCertificate{defaultCertificate, exactCertificate})
	pool := certificatePool(defaultCertificate, exactCertificate)

	invalid := newTestCertificate(t, "exact", 5, []string{"other.example.com"}, now)
	resources.Certificates[1] = invalid.Material
	err := instance.Apply(2, resources)
	var buildErr *gatewayruntime.BuildError
	if !errors.As(err, &buildErr) || buildErr.Code != downstreamtls.CodeCertificateHostnameMismatch {
		t.Fatalf("Apply() error = %v, want %s", err, downstreamtls.CodeCertificateHostnameMismatch)
	}
	if got := dialDownstreamTLSSerial(t, addresses.HTTPS, "api.example.com", pool, nil); got != 2 {
		t.Fatalf("peer serial after rejected update = %d, want 2", got)
	}
	response, err := http.Get("http://" + loopback(t, addresses.HTTP) + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTP status after rejected update = %d, want 204", response.StatusCode)
	}
}

func TestRejectedTrailingDotWildcardSANRotationKeepsLastGood(t *testing.T) {
	now := time.Now()
	defaultCertificate := newTestCertificate(t, "default", 1, []string{"default.example"}, now)
	wildcardCertificate := newTestCertificate(t, "wildcard", 3, []string{"*.example.com"}, now)
	instance, addresses, resources := startDownstreamTLSGateway(t, []testCertificate{defaultCertificate, wildcardCertificate})
	pool := certificatePool(defaultCertificate, wildcardCertificate)
	if got := dialDownstreamTLSSerial(t, addresses.HTTPS, "shop.example.com", pool, nil); got != 3 {
		t.Fatalf("initial peer serial = %d, want 3", got)
	}

	invalid := newTestCertificate(t, "wildcard", 5, []string{"*.example.com."}, now)
	resources.Certificates[1] = invalid.Material
	err := instance.Apply(2, resources)
	var buildErr *gatewayruntime.BuildError
	if !errors.As(err, &buildErr) || buildErr.Code != downstreamtls.CodeCertificateHostnameMismatch {
		t.Fatalf("Apply() error = %v, want %s", err, downstreamtls.CodeCertificateHostnameMismatch)
	}
	if got := dialDownstreamTLSSerial(t, addresses.HTTPS, "shop.example.com", pool, nil); got != 3 {
		t.Fatalf("peer serial after rejected update = %d, want 3", got)
	}
}

func TestDownstreamTLSSessionResumptionIsDisabled(t *testing.T) {
	now := time.Now()
	certificate := newTestCertificate(t, "default", 1, []string{"gateway.example"}, now)
	_, addresses, _ := startDownstreamTLSGateway(t, []testCertificate{certificate})
	clientTLS := &tls.Config{
		RootCAs:            certificatePool(certificate),
		ServerName:         "gateway.example",
		ClientSessionCache: tls.NewLRUClientSessionCache(8),
		MinVersion:         tls.VersionTLS12,
	}
	transport := &http.Transport{TLSClientConfig: clientTLS, DisableKeepAlives: true}
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()
	for attempt := 1; attempt <= 2; attempt++ {
		response, err := client.Get("https://" + loopback(t, addresses.HTTPS) + "/hello")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.TLS == nil || response.TLS.DidResume {
			t.Fatalf("connection %d DidResume = %v, want false", attempt, response.TLS != nil && response.TLS.DidResume)
		}
	}
}

func TestHTTP1ConnectionSurvivesCertificateRotation(t *testing.T) {
	now := time.Now()
	defaultCertificate := newTestCertificate(t, "default", 1, []string{"default.example"}, now)
	exactCertificate := newTestCertificate(t, "exact", 2, []string{"api.example.com"}, now)
	instance, addresses, resources := startDownstreamTLSGateway(t, []testCertificate{defaultCertificate, exactCertificate})
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: certificatePool(defaultCertificate, exactCertificate), ServerName: "api.example.com"},
		MaxConnsPerHost: 1,
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport.Protocols = protocols
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()

	request, _ := http.NewRequest(http.MethodGet, "https://"+loopback(t, addresses.HTTPS)+"/hello", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.TLS == nil || response.TLS.PeerCertificates[0].SerialNumber.Int64() != 2 {
		t.Fatalf("initial TLS state = %+v", response.TLS)
	}

	rotated := newTestCertificate(t, "exact", 4, []string{"api.example.com"}, now)
	resources.Certificates[1] = rotated.Material
	if err := instance.Apply(2, resources); err != nil {
		t.Fatal(err)
	}
	var reused bool
	request, _ = http.NewRequest(http.MethodGet, "https://"+loopback(t, addresses.HTTPS)+"/hello", nil)
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
	}))
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if !reused || response.TLS.PeerCertificates[0].SerialNumber.Int64() != 2 {
		t.Fatalf("established connection reused=%v serial=%v", reused, response.TLS.PeerCertificates[0].SerialNumber)
	}
	if got := dialDownstreamTLSSerial(t, addresses.HTTPS, "api.example.com", certificatePool(defaultCertificate, rotated), nil); got != 4 {
		t.Fatalf("new connection serial = %d, want 4", got)
	}
}

func TestHTTP2StreamSurvivesCertificateRotation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	upstreamHandler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "before\n")
		writer.(http.Flusher).Flush()
		close(started)
		<-release
		_, _ = io.WriteString(writer, "after\n")
	})
	now := time.Now()
	defaultCertificate := newTestCertificate(t, "default", 1, []string{"default.example"}, now)
	exactCertificate := newTestCertificate(t, "exact", 2, []string{"api.example.com"}, now)
	instance, addresses, resources := startDownstreamTLSGatewayWithHandler(t, []testCertificate{defaultCertificate, exactCertificate}, upstreamHandler)
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: certificatePool(defaultCertificate, exactCertificate), ServerName: "api.example.com"},
		ForceAttemptHTTP2: true,
	}
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()
	response, err := client.Get("https://" + loopback(t, addresses.HTTPS) + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "before\n" || response.ProtoMajor != 2 {
		t.Fatalf("first stream chunk=%q protocol=%s err=%v", line, response.Proto, err)
	}
	<-started

	rotated := newTestCertificate(t, "exact", 4, []string{"api.example.com"}, now)
	resources.Certificates[1] = rotated.Material
	if err := instance.Apply(2, resources); err != nil {
		t.Fatal(err)
	}
	if got := dialDownstreamTLSSerial(t, addresses.HTTPS, "api.example.com", certificatePool(defaultCertificate, rotated), nil); got != 4 {
		t.Fatalf("new connection serial = %d, want 4", got)
	}
	releaseOnce.Do(func() { close(release) })
	rest, err := io.ReadAll(reader)
	if err != nil || string(rest) != "after\n" {
		t.Fatalf("remaining stream = %q, error = %v", rest, err)
	}
}

func startDownstreamTLSGateway(t *testing.T, certificates []testCertificate) (*gateway.Gateway, gateway.Addresses, model.ResourceSet) {
	return startDownstreamTLSGatewayWithHandler(t, certificates, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
}

func startDownstreamTLSGatewayWithHandler(t *testing.T, certificates []testCertificate, handler http.Handler) (*gateway.Gateway, gateway.Addresses, model.ResourceSet) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	certificateFile, privateKeyFile := writeCertificatePair(t)
	bootstrap := config.BootstrapConfig{
		HTTP:  config.ListenerConfig{Address: "127.0.0.1:0"},
		HTTPS: config.TLSListenerConfig{Address: "127.0.0.1:0", CertificateFile: certificateFile, PrivateKeyFile: privateKeyFile},
		Admin: config.ListenerConfig{Address: "127.0.0.1:0"},
		Server: config.ServerConfig{
			ReadHeaderTimeout:   time.Second,
			IdleTimeout:         time.Minute,
			ShutdownTimeout:     3 * time.Second,
			MaxHeaderBytes:      1 << 20,
			MaxRequestBodyBytes: 1 << 20,
		},
	}
	resources := model.ResourceSet{
		Routes: []model.Route{{
			ID:          "downstream-tls",
			Match:       model.RouteMatch{Path: "/hello", Methods: []string{http.MethodGet}},
			UpstreamRef: "downstream-tls",
		}},
		Upstreams: []model.Upstream{{
			ID:        "downstream-tls",
			Endpoints: []model.Endpoint{{URL: upstream.URL, Weight: 1}},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				DialTimeout: time.Second, ResponseHeaderTimeout: time.Second,
				IdleConnectionTimeout: time.Minute, MaxIdleConnections: 8, MaxIdleConnectionsPerHost: 8,
			},
		}},
		DownstreamTLS: &model.DownstreamTLSPolicy{DefaultCertificateRef: "default"},
	}
	for _, certificate := range certificates {
		resources.Certificates = append(resources.Certificates, certificate.Material)
		switch certificate.Material.ID() {
		case "exact":
			resources.DownstreamTLS.SNIBindings = append(resources.DownstreamTLS.SNIBindings,
				model.SNIBinding{CertificateRef: "exact", Hosts: []string{"api.example.com"}})
		case "wildcard":
			resources.DownstreamTLS.SNIBindings = append(resources.DownstreamTLS.SNIBindings,
				model.SNIBinding{CertificateRef: "wildcard", Hosts: []string{"*.example.com"}})
		}
	}
	instance, err := gateway.New(bootstrap, resources, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := instance.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := instance.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	return instance, addresses, resources
}

func newTestCertificate(t *testing.T, id string, serial int64, dnsNames []string, now time.Time) testCertificate {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: id},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     append([]string(nil), dnsNames...),
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	material, err := tlsmaterial.NewCertificate(
		id,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(material.TLSCertificate().Leaf)
	return testCertificate{Material: material, Pool: pool, Serial: big.NewInt(serial)}
}

func certificatePool(certificates ...testCertificate) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, certificate := range certificates {
		pool.AddCert(certificate.Material.TLSCertificate().Leaf)
	}
	return pool
}

func dialDownstreamTLSSerial(t *testing.T, address, serverName string, roots *x509.CertPool, sessionCache tls.ClientSessionCache) int64 {
	t.Helper()
	dialer := &net.Dialer{Timeout: time.Second}
	connection, err := tls.DialWithDialer(dialer, "tcp", loopback(t, address), &tls.Config{
		RootCAs: roots, ServerName: serverName, ClientSessionCache: sessionCache, MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	state := connection.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("TLS peer returned no certificate")
	}
	return state.PeerCertificates[0].SerialNumber.Int64()
}
