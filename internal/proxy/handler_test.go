package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestRouteMatchesExactPathAndPreservesRawQuery(t *testing.T) {
	var seenQuery string
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		seenQuery = request.URL.RawQuery
		return response(http.StatusOK, "proxied"), nil
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/hello?x=1&x=%2F", nil))

	if recorder.Code != http.StatusOK || recorder.Body.String() != "proxied" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if seenQuery != "x=1&x=%2F" {
		t.Fatalf("upstream raw query = %q", seenQuery)
	}

	notExact := httptest.NewRecorder()
	handler.ServeHTTP(notExact, httptest.NewRequest(http.MethodGet, "http://gateway/hello/", nil))
	assertErrorResponse(t, notExact, http.StatusNotFound, "ROUTE_NOT_FOUND", "route not found")
}

func TestRouteNotFound(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(unexpectedRoundTrip(t)))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/missing", nil))

	assertErrorResponse(t, recorder, http.StatusNotFound, "ROUTE_NOT_FOUND", "route not found")
}

func TestMethodNotAllowedIncludesSortedAllow(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodPost, http.MethodGet}, 1024, roundTripFunc(unexpectedRoundTrip(t)))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "http://gateway/hello", nil))

	assertErrorResponse(t, recorder, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	if got := recorder.Header().Get("Allow"); got != "GET, POST" {
		t.Fatalf("Allow = %q, want GET, POST", got)
	}
}

func TestInvalidRequestTarget(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(unexpectedRoundTrip(t)))
	request := httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil)
	request.URL.Path = "relative"
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	assertErrorResponse(t, recorder, http.StatusBadRequest, "INVALID_REQUEST", "invalid request")
}

func TestBodyOverLimit(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodPost}, 4, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		_, err := io.Copy(io.Discard, request.Body)
		if err != nil {
			return nil, err
		}
		return response(http.StatusOK, "unexpected"), nil
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://gateway/hello", bytes.NewBufferString("12345")))

	assertErrorResponse(t, recorder, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large")
}

func TestUpgradeNotSupported(t *testing.T) {
	handler := newTestHandler(t, []string{http.MethodGet}, 1024, roundTripFunc(unexpectedRoundTrip(t)))
	tests := []struct {
		name       string
		connection string
		upgrade    string
	}{
		{name: "Upgrade header", upgrade: "websocket"},
		{name: "Connection token", connection: "keep-alive, Upgrade"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil)
			request.Header.Set("Connection", tt.connection)
			request.Header.Set("Upgrade", tt.upgrade)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			assertErrorResponse(t, recorder, http.StatusNotImplemented, "UPGRADE_NOT_SUPPORTED", "upgrade not supported")
		})
	}
}

func newTestHandler(t *testing.T, methods []string, maxBody int64, transport http.RoundTripper) http.Handler {
	t.Helper()
	target, err := url.Parse("http://upstream:8080")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{
		Route: model.Route{
			ID: "baseline",
			Match: model.RouteMatch{
				Path:    "/hello",
				Methods: methods,
			},
			UpstreamRef: "baseline",
		},
		Target:              target,
		Transport:           transport,
		MaxRequestBodyBytes: maxBody,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

func assertErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body=%q", recorder.Code, status, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body %q: %v", recorder.Body.String(), err)
	}
	if len(body) != 2 || body["code"] != code || body["message"] != message {
		t.Fatalf("error body = %#v, want code=%q message=%q", body, code, message)
	}
	if strings.Contains(recorder.Body.String(), "upstream:8080") {
		t.Fatalf("error leaked upstream address: %q", recorder.Body.String())
	}
}

func unexpectedRoundTrip(t *testing.T) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(*http.Request) (*http.Response, error) {
		t.Error("RoundTrip was called unexpectedly")
		return nil, errors.New("unexpected RoundTrip")
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
