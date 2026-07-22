package integration_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/testupstream"
)

func TestHTTP2TLSDownstreamUsesHTTP1Upstream(t *testing.T) {
	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	addresses := startGateway(t, upstream.URL, "/headers", 1<<20, time.Second)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // Test certificate is generated per test.
		ForceAttemptHTTP2: true,
		Protocols:         protocols,
	}
	defer transport.CloseIdleConnections()

	response, err := (&http.Client{Transport: transport}).Get("https://" + loopback(t, addresses.HTTPS) + "/headers")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatalf("downstream protocol=%s, want HTTP/2", response.Proto)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" || !strings.Contains(string(body), "HTTP/1.1") {
		t.Fatalf("upstream response=%q", body)
	}
}
