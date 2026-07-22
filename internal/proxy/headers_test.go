package proxy

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRebuildsForwardingHeadersFromTrustedRequestState(t *testing.T) {
	var outbound *http.Request
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		outbound = request.Clone(request.Context())
		outbound.Header = request.Header.Clone()
		return response(http.StatusNoContent, ""), nil
	}))
	request := httptest.NewRequest(http.MethodGet, "http://gateway/hello?raw=%2F", nil)
	request.Host = "api.example.test:8443"
	request.RemoteAddr = "203.0.113.10:54321"
	request.Header.Set("Forwarded", "for=attacker")
	request.Header.Set("X-Forwarded-For", "198.51.100.1")
	request.Header.Set("X-Forwarded-Host", "attacker.example")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Port", "1")

	handler.ServeHTTP(httptest.NewRecorder(), request)

	if outbound == nil {
		t.Fatal("upstream request was not observed")
	}
	if outbound.Host != "api.example.test:8443" {
		t.Fatalf("Host = %q", outbound.Host)
	}
	assertHeader(t, outbound.Header, "Forwarded", "")
	assertHeader(t, outbound.Header, "X-Forwarded-For", "203.0.113.10")
	assertHeader(t, outbound.Header, "X-Forwarded-Host", "api.example.test:8443")
	assertHeader(t, outbound.Header, "X-Forwarded-Proto", "http")
	assertHeader(t, outbound.Header, "X-Forwarded-Port", "8443")
	if outbound.URL.RawQuery != "raw=%2F" {
		t.Fatalf("RawQuery = %q", outbound.URL.RawQuery)
	}
}

func TestRebuildsTLSForwardingHeaders(t *testing.T) {
	var outbound http.Header
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		outbound = request.Header.Clone()
		return response(http.StatusNoContent, ""), nil
	}))
	request := httptest.NewRequest(http.MethodGet, "https://gateway/hello", nil)
	request.Host = "secure.example.test"
	request.RemoteAddr = "[2001:db8::10]:54321"
	request.TLS = &tls.ConnectionState{}

	handler.ServeHTTP(httptest.NewRecorder(), request)

	assertHeader(t, outbound, "X-Forwarded-For", "2001:db8::10")
	assertHeader(t, outbound, "X-Forwarded-Proto", "https")
	assertHeader(t, outbound, "X-Forwarded-Port", "443")
}

func TestRemovesHopByHopHeadersInBothDirections(t *testing.T) {
	var outbound http.Header
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		outbound = request.Header.Clone()
		upstreamResponse := response(http.StatusOK, "ok")
		upstreamResponse.Header.Set("Connection", "X-Upstream-Internal")
		upstreamResponse.Header.Set("X-Upstream-Internal", "secret")
		upstreamResponse.Header.Set("Keep-Alive", "timeout=5")
		upstreamResponse.Header.Set("Proxy-Connection", "keep-alive")
		return upstreamResponse, nil
	}))
	request := httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil)
	request.Header.Set("Connection", "X-Client-Internal, keep-alive")
	request.Header.Set("X-Client-Internal", "secret")
	request.Header.Set("Keep-Alive", "timeout=5")
	request.Header.Set("Proxy-Connection", "keep-alive")
	request.Header.Set("TE", "gzip")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	for _, name := range []string{"Connection", "X-Client-Internal", "Keep-Alive", "Proxy-Connection", "TE"} {
		assertHeader(t, outbound, name, "")
	}
	for _, name := range []string{"Connection", "X-Upstream-Internal", "Keep-Alive", "Proxy-Connection"} {
		assertHeader(t, recorder.Header(), name, "")
	}
}

func assertHeader(t *testing.T, header http.Header, name, want string) {
	t.Helper()
	if got := header.Get(name); got != want {
		t.Fatalf("%s = %q, want %q; headers=%v", name, got, want, header)
	}
}
