package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

func TestRetryEligibilityRequiresConfiguredMethodAndReplayableBody(t *testing.T) {
	policy := model.RetryPolicy{MaxAttempts: 3, Methods: []string{"GET", "POST"}}
	tests := []struct {
		name    string
		request *http.Request
		want    bool
	}{
		{name: "bodyless configured", request: mustRetryRequest(t, http.MethodGet, nil), want: true},
		{name: "unconfigured method", request: mustRetryRequest(t, http.MethodDelete, nil), want: false},
		{name: "get body replayable", request: mustRetryRequest(t, http.MethodPost, bytes.NewReader([]byte("payload"))), want: true},
		{name: "unknown body not replayable", request: mustRetryRequest(t, http.MethodPost, io.NopCloser(strings.NewReader("payload"))), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := retryEligible(test.request, policy); got != test.want {
				t.Fatalf("retryEligible() = %v, want %v", got, test.want)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := mustRetryRequest(t, http.MethodGet, nil).WithContext(ctx)
	if retryEligible(request, policy) {
		t.Fatal("canceled request is retry eligible")
	}
}

func TestClassifyAttemptUsesOnlyRetryPolicy(t *testing.T) {
	policy := model.RetryPolicy{
		RetryOn: model.RetryOnPolicy{
			ConnectFailure:        true,
			ConnectionFailure:     true,
			ResponseHeaderTimeout: true,
			Statuses:              []uint16{503},
		},
	}
	tests := []struct {
		name     string
		response *http.Response
		err      error
		want     bool
		kind     upstream.OutcomeKind
	}{
		{name: "connect", err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, want: true, kind: upstream.OutcomeTransportFailure},
		{name: "connection reset", err: syscall.ECONNRESET, want: true, kind: upstream.OutcomeTransportFailure},
		{name: "timeout", err: timeoutError{}, want: true, kind: upstream.OutcomeTimeout},
		{name: "configured status", response: &http.Response{StatusCode: 503}, want: true, kind: upstream.OutcomeSuccess},
		{name: "unconfigured status", response: &http.Response{StatusCode: 409}, want: false, kind: upstream.OutcomeSuccess},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := classifyAttempt(policy, test.response, test.err)
			if decision.Retry != test.want || decision.Observation.Kind != test.kind {
				t.Fatalf("decision = %+v", decision)
			}
		})
	}
}

func TestClassifyAttemptTreatsTypedTLSFailuresAsConnectionFailures(t *testing.T) {
	policy := model.RetryPolicy{
		RetryOn: model.RetryOnPolicy{ConnectionFailure: true},
	}
	classes := []upstream.TLSFailureClass{
		upstream.TLSFailureTrust,
		upstream.TLSFailureHostname,
		upstream.TLSFailureClientIdentity,
		upstream.TLSFailureProtocol,
		upstream.TLSFailureHandshake,
	}
	for _, class := range classes {
		t.Run(string(class), func(t *testing.T) {
			tlsErr := &upstream.TLSFailureError{Class: class, Err: errors.New("sensitive")}
			decision := classifyAttempt(policy, nil, tlsErr)
			if !decision.Retry ||
				decision.Reason != retryReasonConnectionFailure ||
				decision.Observation.Kind != upstream.OutcomeTransportFailure {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}

	tlsTimeout := &upstream.TLSFailureError{
		Class: upstream.TLSFailureHandshake,
		Err:   timeoutError{},
	}
	decision := classifyAttempt(policy, nil, tlsTimeout)
	if decision.Retry ||
		decision.Reason != retryReasonResponseHeaderTimeout ||
		decision.Observation.Kind != upstream.OutcomeTimeout {
		t.Fatalf("TLS timeout decision=%+v", decision)
	}
}

func TestClassifyAttemptDoesNotInspectGRPCStatusTrailers(t *testing.T) {
	policy := model.RetryPolicy{
		RetryOn: model.RetryOnPolicy{
			ConnectionFailure: true,
			Statuses:          []uint16{503},
		},
	}
	response := &http.Response{
		StatusCode: http.StatusOK,
		Trailer:    http.Header{"Grpc-Status": []string{"14"}},
	}
	decision := classifyAttempt(policy, response, nil)
	if decision.Retry ||
		decision.Reason != retryReasonNone ||
		decision.Observation.Kind != upstream.OutcomeSuccess {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestDrainRetryResponseIsBounded(t *testing.T) {
	small := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 1024)))}
	if !drainRetryResponse(small) {
		t.Fatal("small response was not fully drained")
	}
	large := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 33<<10)))}
	if drainRetryResponse(large) {
		t.Fatal("oversized response reported fully drained")
	}
}

func mustRetryRequest(t *testing.T, method string, body io.Reader) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, "http://gateway.test/resource", body)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}
