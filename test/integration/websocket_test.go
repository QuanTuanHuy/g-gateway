package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/gateway"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/testupstream"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

const rawWebSocketKey = "dGhlIHNhbXBsZSBub25jZQ=="

type rawWebSocketClient struct {
	connection net.Conn
	buffered   *bufio.ReadWriter
	response   *http.Response
}

func dialRawWebSocket(t *testing.T, networkAddress, host, path string, tlsConfig *tls.Config) *rawWebSocketClient {
	t.Helper()
	return dialRawWebSocketHeaders(t, networkAddress, host, path, tlsConfig, nil)
}

func dialRawWebSocketHeaders(t *testing.T, networkAddress, host, path string, tlsConfig *tls.Config, headers http.Header) *rawWebSocketClient {
	t.Helper()
	dialer := &net.Dialer{Timeout: time.Second}
	var connection net.Conn
	var err error
	if tlsConfig == nil {
		connection, err = dialer.Dial("tcp", networkAddress)
	} else {
		connection, err = tls.DialWithDialer(dialer, "tcp", networkAddress, tlsConfig)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	buffered := bufio.NewReadWriter(bufio.NewReader(connection), bufio.NewWriter(connection))
	requestHeaders := make(http.Header)
	requestHeaders.Set("Connection", "Upgrade")
	requestHeaders.Set("Upgrade", "websocket")
	requestHeaders.Set("Sec-WebSocket-Version", "13")
	requestHeaders.Set("Sec-WebSocket-Key", rawWebSocketKey)
	for name, values := range headers {
		requestHeaders[name] = append([]string(nil), values...)
	}
	request := &http.Request{
		Method:     http.MethodGet,
		URL:        &url.URL{Path: path},
		Host:       host,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     requestHeaders,
	}
	if err := request.Write(buffered); err != nil {
		t.Fatal(err)
	}
	if err := buffered.Flush(); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(buffered.Reader, request)
	if err != nil {
		t.Fatal(err)
	}
	client := &rawWebSocketClient{connection: connection, buffered: buffered, response: response}
	if response.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("websocket response=%d body=%q", response.StatusCode, body)
	}
	if response.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("Sec-WebSocket-Accept=%q", response.Header.Get("Sec-WebSocket-Accept"))
	}
	return client
}

func (c *rawWebSocketClient) WriteFrame(t *testing.T, fin bool, opcode byte, payload []byte) {
	t.Helper()
	first := opcode & 0x0f
	if fin {
		first |= 0x80
	}
	if err := c.buffered.WriteByte(first); err != nil {
		t.Fatal(err)
	}
	mask := [4]byte{0x11, 0x22, 0x33, 0x44}
	switch length := len(payload); {
	case length < 126:
		_ = c.buffered.WriteByte(0x80 | byte(length))
	case length <= 0xffff:
		_ = c.buffered.WriteByte(0x80 | 126)
		var encoded [2]byte
		binary.BigEndian.PutUint16(encoded[:], uint16(length))
		_, _ = c.buffered.Write(encoded[:])
	default:
		_ = c.buffered.WriteByte(0x80 | 127)
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(length))
		_, _ = c.buffered.Write(encoded[:])
	}
	_, _ = c.buffered.Write(mask[:])
	masked := append([]byte(nil), payload...)
	for index := range masked {
		masked[index] ^= mask[index%len(mask)]
	}
	if _, err := c.buffered.Write(masked); err != nil {
		t.Fatal(err)
	}
	if err := c.buffered.Flush(); err != nil {
		t.Fatal(err)
	}
}

func (c *rawWebSocketClient) ReadFrame(t *testing.T) (bool, byte, []byte) {
	t.Helper()
	_ = c.connection.SetReadDeadline(time.Now().Add(time.Second))
	defer c.connection.SetReadDeadline(time.Time{})
	var prefix [2]byte
	if _, err := io.ReadFull(c.buffered, prefix[:]); err != nil {
		t.Fatal(err)
	}
	if prefix[1]&0x80 != 0 {
		t.Fatal("server frame is masked")
	}
	length := uint64(prefix[1] & 0x7f)
	switch length {
	case 126:
		var encoded [2]byte
		if _, err := io.ReadFull(c.buffered, encoded[:]); err != nil {
			t.Fatal(err)
		}
		length = uint64(binary.BigEndian.Uint16(encoded[:]))
	case 127:
		var encoded [8]byte
		if _, err := io.ReadFull(c.buffered, encoded[:]); err != nil {
			t.Fatal(err)
		}
		length = binary.BigEndian.Uint64(encoded[:])
	}
	if length > 1<<20 {
		t.Fatalf("server frame length=%d", length)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(c.buffered, payload); err != nil {
		t.Fatal(err)
	}
	return prefix[0]&0x80 != 0, prefix[0] & 0x0f, payload
}

func TestWebSocketClearAndTLSMatrix(t *testing.T) {
	for _, downstreamTLS := range []bool{false, true} {
		for _, upstreamTLS := range []bool{false, true} {
			name := fmt.Sprintf("downstream_tls_%t_upstream_tls_%t", downstreamTLS, upstreamTLS)
			t.Run(name, func(t *testing.T) {
				upstream := httptest.NewServer(testupstream.New(discardLogger()))
				if upstreamTLS {
					upstream.Close()
					upstream = httptest.NewTLSServer(testupstream.New(discardLogger()))
				}
				defer upstream.Close()
				resources := websocketResources(upstream.URL, true)
				if upstreamTLS {
					rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw})
					bundle, err := tlsmaterial.NewTrustBundle("websocket-root", rootPEM)
					if err != nil {
						t.Fatal(err)
					}
					resources.TrustBundles = []*tlsmaterial.TrustBundle{bundle}
					resources.Upstreams[0].Transport.TLS = &model.UpstreamTLSPolicy{TrustBundleRef: "websocket-root", ServerName: "example.com"}
				}
				_, addresses := startWebSocketGateway(t, resources, 3*time.Second)
				address := addresses.HTTP
				var downstreamConfig *tls.Config
				if downstreamTLS {
					address = addresses.HTTPS
					downstreamConfig = &tls.Config{InsecureSkipVerify: true} // Test certificate is generated per test.
				}
				client := dialRawWebSocket(t, loopback(t, address), "gateway.test", "/websocket/echo", downstreamConfig)
				client.WriteFrame(t, true, 1, []byte("hello"))
				fin, opcode, payload := client.ReadFrame(t)
				if !fin || opcode != 1 || string(payload) != "hello" {
					t.Fatalf("echo fin=%t opcode=%d payload=%q", fin, opcode, payload)
				}
			})
		}
	}
}

func TestWebSocketPreservesNegotiationAndOpaqueFrames(t *testing.T) {
	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	_, addresses := startWebSocketGateway(t, websocketResources(upstream.URL, true), 3*time.Second)
	headers := make(http.Header)
	headers.Set("Sec-WebSocket-Protocol", "chat, superchat")
	headers.Set("Sec-WebSocket-Extensions", "x-test")
	client := dialRawWebSocketHeaders(t, loopback(t, addresses.HTTP), "gateway.test", "/websocket/echo", nil, headers)
	if client.response.Header.Get("Sec-WebSocket-Protocol") != "chat" || client.response.Header.Get("Sec-WebSocket-Extensions") != "x-test" {
		t.Fatalf("negotiation headers=%v", client.response.Header)
	}
	frames := []struct {
		fin     bool
		opcode  byte
		payload []byte
	}{
		{false, 2, []byte{0, 1, 2, 255}},
		{true, 0, []byte("tail")},
		{true, 2, bytes.Repeat([]byte{0xa5}, 256*1024)},
	}
	for _, frame := range frames {
		client.WriteFrame(t, frame.fin, frame.opcode, frame.payload)
		fin, opcode, payload := client.ReadFrame(t)
		if fin != frame.fin || opcode != frame.opcode || !bytes.Equal(payload, frame.payload) {
			t.Fatalf("echo fin=%t opcode=%d bytes=%d", fin, opcode, len(payload))
		}
	}
}

func TestWebSocketDisabledAndMalformedRemainHTTP(t *testing.T) {
	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	_, disabled := startWebSocketGateway(t, websocketResources(upstream.URL, false), 3*time.Second)
	request, _ := http.NewRequest(http.MethodGet, "http://"+loopback(t, disabled.HTTP)+"/websocket/echo", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", rawWebSocketKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("disabled status=%d, want ordinary upstream 400", response.StatusCode)
	}

	_, enabled := startWebSocketGateway(t, websocketResources(upstream.URL, true), 3*time.Second)
	malformed, _ := http.NewRequest(http.MethodGet, "http://"+loopback(t, enabled.HTTP)+"/websocket/echo", nil)
	malformed.Header.Set("Connection", "Upgrade")
	malformed.Header.Set("Upgrade", "websocket")
	malformedResponse, err := http.DefaultClient.Do(malformed)
	if err != nil {
		t.Fatal(err)
	}
	defer malformedResponse.Body.Close()
	if malformedResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed status=%d, want 400", malformedResponse.StatusCode)
	}
}

func TestWebSocketStrictHTTP2IsRejected(t *testing.T) {
	resources := websocketResources("https://example.test", true)
	resources.Upstreams[0].Transport.Protocol = model.TransportProtocolHTTP2
	certificateFile, privateKeyFile := writeCertificatePair(t)
	_, err := gateway.New(websocketBootstrap(certificateFile, privateKeyFile, 3*time.Second), resources, discardLogger())
	if err == nil {
		t.Fatal("gateway.New() error=nil, want strict HTTP/2 WebSocket rejection")
	}
}

func TestWebSocketRetriesConnectionFailureAndInvalid101(t *testing.T) {
	good := httptest.NewServer(testupstream.New(discardLogger()))
	defer good.Close()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddress := closed.Addr().String()
	_ = closed.Close()
	resources := websocketResources("http://"+closedAddress, true)
	resources.Upstreams[0].Endpoints[0].Weight = 2
	resources.Upstreams[0].Endpoints = append(resources.Upstreams[0].Endpoints, model.Endpoint{URL: good.URL, Weight: 1})
	configureWebSocketRetry(&resources.Upstreams[0])
	_, addresses := startWebSocketGateway(t, resources, 3*time.Second)
	client := dialRawWebSocket(t, loopback(t, addresses.HTTP), "gateway.test", "/websocket/echo", nil)
	client.WriteFrame(t, true, 1, []byte("retried"))
	_, _, payload := client.ReadFrame(t)
	if string(payload) != "retried" {
		t.Fatalf("echo=%q", payload)
	}

	var invalidCalls atomic.Int64
	invalid := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		invalidCalls.Add(1)
		writer.Header().Set("Connection", "Upgrade")
		writer.Header().Set("Upgrade", "websocket")
		writer.Header().Set("Sec-WebSocket-Accept", "invalid")
		writer.WriteHeader(http.StatusSwitchingProtocols)
	}))
	defer invalid.Close()
	resources = websocketResources(invalid.URL, true)
	resources.Upstreams[0].Endpoints[0].Weight = 2
	resources.Upstreams[0].Endpoints = append(resources.Upstreams[0].Endpoints, model.Endpoint{URL: good.URL, Weight: 1})
	configureWebSocketRetry(&resources.Upstreams[0])
	_, addresses = startWebSocketGateway(t, resources, 3*time.Second)
	client = dialRawWebSocket(t, loopback(t, addresses.HTTP), "gateway.test", "/websocket/echo", nil)
	if invalidCalls.Load() != 1 {
		t.Fatalf("invalid upstream calls=%d, want 1", invalidCalls.Load())
	}

	resources = websocketResources(invalid.URL, true)
	configureWebSocketRetry(&resources.Upstreams[0])
	_, addresses = startWebSocketGateway(t, resources, 3*time.Second)
	response := performUpgradeRequest(t, addresses.HTTP)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("final invalid 101 status=%d, want 502", response.StatusCode)
	}
}

func TestWebSocketReloadKeepsExistingTunnel(t *testing.T) {
	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	instance, addresses := startWebSocketGateway(t, websocketResources(upstream.URL, true), 3*time.Second)
	client := dialRawWebSocket(t, loopback(t, addresses.HTTP), "gateway.test", "/websocket/echo", nil)
	client.WriteFrame(t, true, 1, []byte("before"))
	_, _, payload := client.ReadFrame(t)
	if string(payload) != "before" {
		t.Fatalf("echo before reload=%q", payload)
	}
	replacement := httptest.NewServer(testupstream.New(discardLogger()))
	defer replacement.Close()
	rotated := websocketResources(replacement.URL, false)
	rotated.Routes[0].Match.Path = "/replacement"
	if err := instance.Apply(2, rotated); err != nil {
		t.Fatalf("Apply() error=%v", err)
	}
	response := performUpgradeRequest(t, addresses.HTTP)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("new handshake status=%d, want 404", response.StatusCode)
	}
	client.WriteFrame(t, true, 2, []byte("after"))
	_, _, payload = client.ReadFrame(t)
	if string(payload) != "after" {
		t.Fatalf("echo after reload=%q", payload)
	}
	_ = client.connection.Close()
	waitForMetric(t, addresses.Admin, "gateway_websocket_active_tunnels 0")
}

func TestWebSocketShutdownDrainsNaturally(t *testing.T) {
	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	instance, addresses := startWebSocketGateway(t, websocketResources(upstream.URL, true), 3*time.Second)
	client := dialRawWebSocket(t, loopback(t, addresses.HTTP), "gateway.test", "/websocket/echo", nil)
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		done <- instance.Shutdown(ctx)
	}()
	select {
	case err := <-done:
		t.Fatalf("Shutdown() returned with active tunnel: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	client.WriteFrame(t, true, 1, []byte("during-drain"))
	_, _, payload := client.ReadFrame(t)
	if string(payload) != "during-drain" {
		t.Fatalf("echo during drain=%q", payload)
	}
	_ = client.connection.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown() error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown() did not finish after natural tunnel close")
	}
}

func TestWebSocketShutdownDeadlineForceCloses(t *testing.T) {
	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	certificateFile, privateKeyFile := writeCertificatePair(t)
	instance, err := gateway.New(websocketBootstrap(certificateFile, privateKeyFile, time.Second), websocketResources(upstream.URL, true), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := instance.Start()
	if err != nil {
		t.Fatal(err)
	}
	client := dialRawWebSocket(t, loopback(t, addresses.HTTP), "gateway.test", "/websocket/echo", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := instance.Shutdown(ctx); err == nil {
		t.Fatal("Shutdown() error=nil, want deadline error")
	}
	_ = client.connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.buffered.ReadByte(); err == nil {
		t.Fatal("tunnel remained open after shutdown deadline")
	}
}

func configureWebSocketRetry(upstream *model.Upstream) {
	upstream.Retry = model.RetryPolicy{
		MaxAttempts: 2,
		Methods:     []string{http.MethodGet},
		RetryOn: model.RetryOnPolicy{
			ConnectFailure:    true,
			ConnectionFailure: true,
		},
		Budget: model.RetryBudgetPolicy{
			RatioPer1000: 1000,
			Burst:        2,
			MaxInflight:  1,
		},
	}
}

func performUpgradeRequest(t *testing.T, address string) *http.Response {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, "http://"+loopback(t, address)+"/websocket/echo", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", rawWebSocketKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitForMetric(t *testing.T, adminAddress, fragment string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + loopback(t, adminAddress) + "/metrics")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if strings.Contains(string(body), fragment) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("metric %q was not observed", fragment)
}

func websocketResources(endpoint string, enabled bool) model.ResourceSet {
	return model.ResourceSet{
		Routes: []model.Route{{
			ID:          "websocket",
			Match:       model.RouteMatch{Path: "/websocket/echo", Methods: []string{http.MethodGet}},
			UpstreamRef: "websocket",
			WebSocket:   model.WebSocketPolicyOverride{Enabled: &enabled},
		}},
		Upstreams: []model.Upstream{{
			ID:        "websocket",
			Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				Protocol:                  model.TransportProtocolAuto,
				DialTimeout:               time.Second,
				ResponseHeaderTimeout:     time.Second,
				IdleConnectionTimeout:     time.Minute,
				MaxIdleConnections:        32,
				MaxIdleConnectionsPerHost: 32,
			},
		}},
	}
}

func startWebSocketGateway(t *testing.T, resources model.ResourceSet, shutdownTimeout time.Duration) (*gateway.Gateway, gateway.Addresses) {
	t.Helper()
	certificateFile, privateKeyFile := writeCertificatePair(t)
	instance, err := gateway.New(websocketBootstrap(certificateFile, privateKeyFile, shutdownTimeout), resources, discardLogger())
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
		if err := instance.Shutdown(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("Shutdown() error=%v", err)
		}
	})
	return instance, addresses
}

func websocketBootstrap(certificateFile, privateKeyFile string, shutdownTimeout time.Duration) config.BootstrapConfig {
	return config.BootstrapConfig{
		HTTP: config.ListenerConfig{Address: "127.0.0.1:0"},
		HTTPS: config.TLSListenerConfig{
			Address:         "127.0.0.1:0",
			CertificateFile: certificateFile,
			PrivateKeyFile:  privateKeyFile,
		},
		Admin: config.ListenerConfig{Address: "127.0.0.1:0"},
		Server: config.ServerConfig{
			ReadHeaderTimeout:   time.Second,
			IdleTimeout:         time.Minute,
			ShutdownTimeout:     shutdownTimeout,
			MaxHeaderBytes:      1 << 20,
			MaxRequestBodyBytes: 1 << 20,
		},
	}
}
