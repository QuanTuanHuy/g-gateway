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
)

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

func orderedRetryServers(t *testing.T, failedCalls, successfulCalls *atomic.Int32) (string, string) {
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
