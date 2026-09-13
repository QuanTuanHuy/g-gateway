package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

func TestAttemptExecutorReturnsFinalSelectionAndReleasesRetryPermit(t *testing.T) {
	resources := retryProxyResources("http://127.0.0.1:18081", "http://127.0.0.1:18082", 1000)
	_, manager, registry := newRuntimeTestHandler(t, resources, true)
	lease, ok := manager.Acquire()
	if !ok {
		t.Fatal("manager has no active snapshot")
	}
	defer lease.Release()
	request := httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil)
	match, err := lease.Snapshot().Match(request)
	if err != nil || !match.Found {
		t.Fatalf("Match() = %+v, %v", match, err)
	}
	state := &requestctx.Context{
		Runtime:  match.Route,
		Revision: 17,
		Route:    &requestctx.RouteMeta{ID: "sentinel"},
	}
	retryBody := &countingReadCloser{remaining: 64 << 10}
	calls := 0
	result, err := executeAttempts(request, state,
		func(selection upstream.Selection, _ *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: retryBody}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		},
		func(_ model.RetryPolicy, response *http.Response, _ error) attemptDecision {
			decision := attemptDecision{Observation: upstream.Observation{Source: upstream.SourcePassive, Kind: upstream.OutcomeSuccess}}
			if response.StatusCode == http.StatusServiceUnavailable {
				decision.Retry = true
				decision.Reason = retryReasonStatus
			}
			return decision
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Response == nil || result.Response.StatusCode != http.StatusOK || !result.Selection.Valid() ||
		result.Selection.EndpointID() != state.Selection.EndpointID() {
		t.Fatalf("attempt result=%+v state selection=%q", result, state.Selection.EndpointID())
	}
	if calls != 2 || !retryBody.closed || retryBody.read != (32<<10)+1 {
		t.Fatalf("calls=%d retry body closed=%t read=%d", calls, retryBody.closed, retryBody.read)
	}
	if state.Attempts != 2 || state.Attempt != 2 || state.Revision != 17 || state.Route.ID != "sentinel" {
		t.Fatalf("state metadata=%+v", state)
	}
	for _, stats := range registry.ResilienceStats() {
		if stats.RetryInflight != 0 {
			t.Fatalf("retry permit remained live: %+v", stats)
		}
	}
}

type countingReadCloser struct {
	remaining int
	read      int
	closed    bool
}

func (reader *countingReadCloser) Read(buffer []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	count := len(buffer)
	if count > reader.remaining {
		count = reader.remaining
	}
	for index := 0; index < count; index++ {
		buffer[index] = 'x'
	}
	reader.remaining -= count
	reader.read += count
	return count, nil
}

func (reader *countingReadCloser) Close() error {
	reader.closed = true
	return nil
}

func TestAttemptTransportRetriesDifferentEndpointThenReturnsSuccess(t *testing.T) {
	var failedCalls atomic.Int32
	var successfulCalls atomic.Int32
	failedURL, successfulURL := orderedRetryServers(t, &failedCalls, &successfulCalls)
	resources := retryProxyResources(failedURL, successfulURL, 1000)
	handler, _, _ := newRuntimeTestHandler(t, resources, true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
	if response.Code != http.StatusOK || response.Header().Get("X-Upstream") != "success" {
		t.Fatalf("response = %d %#v %s", response.Code, response.Header(), response.Body.String())
	}
	if failedCalls.Load() != 1 || successfulCalls.Load() != 1 {
		t.Fatalf("calls = failed:%d successful:%d", failedCalls.Load(), successfulCalls.Load())
	}
}

func TestAttemptTransportDoesNotRetryNonReplayableBody(t *testing.T) {
	var calls atomic.Int32
	failedURL, successfulURL := orderedRetryServers(t, &calls, &calls)
	resources := retryProxyResources(failedURL, successfulURL, 1000)
	resources.Routes[0].Match.Methods = append(resources.Routes[0].Match.Methods, http.MethodPost)
	resources.Upstreams[0].Retry.Methods = append(resources.Upstreams[0].Retry.Methods, http.MethodPost)
	handler, _, _ := newRuntimeTestHandler(t, resources, true)
	request := httptest.NewRequest(http.MethodPost, "http://gateway/users/42?tenant=acme", nil)
	request.Body = io.NopCloser(strings.NewReader("payload"))
	request.ContentLength = -1
	request.GetBody = nil
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatalf("response = %d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
}

func TestAttemptTransportBudgetDenialKeepsFirstResponse(t *testing.T) {
	var calls atomic.Int32
	failedURL, successfulURL := orderedRetryServers(t, &calls, &calls)
	resources := retryProxyResources(failedURL, successfulURL, 0)
	handler, _, _ := newRuntimeTestHandler(t, resources, true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
	if response.Code != http.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatalf("response = %d calls=%d", response.Code, calls.Load())
	}
}

func orderedRetryServers(t testing.TB, failedCalls, successfulCalls *atomic.Int32) (string, string) {
	t.Helper()
	var firstFails bool
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if firstFails {
			failedCalls.Add(1)
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		successfulCalls.Add(1)
		writer.Header().Set("X-Upstream", "success")
		writer.WriteHeader(http.StatusOK)
	}))
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !firstFails {
			failedCalls.Add(1)
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		successfulCalls.Add(1)
		writer.Header().Set("X-Upstream", "success")
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	urls := []string{first.URL, second.URL}
	sort.Strings(urls)
	firstFails = first.URL == urls[0]
	return urls[0], urls[1]
}

func retryProxyResources(failedURL, successfulURL string, ratio uint16) model.ResourceSet {
	urls := []string{failedURL, successfulURL}
	sort.Strings(urls)
	resources := runtimeProxyResources(urls[0], urls[1])
	resources.Upstreams[0].Endpoints = []model.Endpoint{
		{URL: urls[0], Weight: 1},
		{URL: urls[1], Weight: 1},
	}
	resources.Upstreams[0].Retry = model.RetryPolicy{
		MaxAttempts: 3,
		Methods:     []string{http.MethodGet},
		RetryOn:     model.RetryOnPolicy{Statuses: []uint16{http.StatusServiceUnavailable}},
		Budget:      model.RetryBudgetPolicy{RatioPer1000: ratio, Burst: 10, MaxInflight: 32},
	}
	return resources
}
