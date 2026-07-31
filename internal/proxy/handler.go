package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/router"
	gatewayruntime "github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

// RuntimeOptions supplies the required snapshot manager, request-body limit,
// and optional structured logger for a proxy handler.
type RuntimeOptions struct {
	// Snapshots is the required source of leased runtime snapshots.
	Snapshots *gatewayruntime.Manager
	// MaxRequestBodyBytes is the positive downstream request-body limit in
	// bytes. Known oversized bodies are rejected before proxying; streaming
	// bodies are bounded while read.
	MaxRequestBodyBytes int64
	// Logger receives rate-limited upstream failures; nil selects a discard
	// logger.
	Logger *slog.Logger
}

type handler struct {
	snapshots           *gatewayruntime.Manager
	maxRequestBodyBytes int64
	proxy               *httputil.ReverseProxy
	logger              *slog.Logger
	logLimiter          errorLogLimiter
}

type responsePluginError struct {
	err error
}

// Error returns the wrapped response-plugin error text.
func (e *responsePluginError) Error() string {
	return e.err.Error()
}

// Unwrap returns the underlying response-plugin error for errors.Is and
// errors.As.
func (e *responsePluginError) Unwrap() error {
	return e.err
}

// NewRuntime validates options and returns an HTTP handler that leases one
// immutable snapshot per request. It rejects a nil snapshot manager and a
// non-positive request-body limit.
func NewRuntime(options RuntimeOptions) (http.Handler, error) {
	if options.Snapshots == nil {
		return nil, fmt.Errorf("snapshot manager must not be nil")
	}
	if options.MaxRequestBodyBytes <= 0 {
		return nil, fmt.Errorf("max request body bytes must be greater than zero")
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	handler := &handler{
		snapshots:           options.Snapshots,
		maxRequestBodyBytes: options.MaxRequestBodyBytes,
		logger:              options.Logger,
	}
	handler.proxy = &httputil.ReverseProxy{
		Transport: routeTransport{},
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			proxyRequest.Out.Host = proxyRequest.In.Host
			removeHopByHopHeaders(proxyRequest.Out.Header)
			rebuildForwardingHeaders(proxyRequest.Out.Header, proxyRequest.In)
		},
		ModifyResponse: func(response *http.Response) error {
			removeHopByHopHeaders(response.Header)
			state, ok := requestctx.From(response.Request.Context())
			if !ok || state.Runtime == nil {
				return errors.New("proxy response missing compiled runtime route")
			}
			if err := state.Runtime.RunResponse(state, response); err != nil {
				return &responsePluginError{err: err}
			}
			return nil
		},
		ErrorHandler: handler.handleProxyError,
	}
	return handler, nil
}

// ServeHTTP leases one snapshot, distinguishes 404 from sorted 405 Allow
// responses, runs request and final-response plugins, and streams proxy bodies.
// The effective total timeout is bounded by any earlier client deadline and
// covers selection, all attempts, and response streaming. Failures before a
// response is committed map to stable gateway status and error codes.
func (h *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	lease, ok := h.snapshots.Acquire()
	if !ok {
		writeError(writer, http.StatusServiceUnavailable, "GATEWAY_NOT_READY", "gateway not ready")
		return
	}
	defer lease.Release()
	snapshot := lease.Snapshot()

	if request.URL == nil || !strings.HasPrefix(request.URL.Path, "/") || request.Method == "" {
		writeError(writer, http.StatusBadRequest, "INVALID_REQUEST", "invalid request")
		return
	}
	match, err := snapshot.Match(request)
	if err != nil {
		if errors.Is(err, router.ErrInvalidQuery) {
			writeError(writer, http.StatusBadRequest, "INVALID_QUERY", "invalid query")
			return
		}
		writeError(writer, http.StatusBadRequest, "INVALID_REQUEST", "invalid request")
		return
	}
	if !match.Found {
		if match.MethodNotAllowed {
			writer.Header().Set("Allow", strings.Join(match.Allow, ", "))
			writeError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		writeError(writer, http.StatusNotFound, "ROUTE_NOT_FOUND", "route not found")
		return
	}

	state, ok := requestctx.From(request.Context())
	if !ok {
		writeError(writer, http.StatusInternalServerError, "REQUEST_CONTEXT_MISSING", "request context missing")
		return
	}
	state.AllocateScratch(match.Route.ScratchSlots())
	state.Snapshot = snapshot
	state.Runtime = match.Route
	state.Revision = snapshot.Revision()
	state.Route = match.Route.Meta()
	state.Service = match.Route.ServiceMeta()
	state.Upstream = match.Route.UpstreamMeta()
	state.Path = request.URL.Path
	state.Params = match.Params

	if request.Header.Get("Upgrade") != "" || headerHasToken(request.Header.Values("Connection"), "upgrade") {
		h.writeMatchedResponse(
			writer,
			request,
			state,
			http.StatusNotImplemented,
			"UPGRADE_NOT_SUPPORTED",
			"upgrade not supported",
			nil,
		)
		return
	}
	if request.ContentLength > h.maxRequestBodyBytes {
		h.writeMatchedResponse(
			writer,
			request,
			state,
			http.StatusRequestEntityTooLarge,
			"REQUEST_BODY_TOO_LARGE",
			"request body too large",
			nil,
		)
		return
	}

	result := match.Route.RunRequest(state, request)
	if result.Err != nil {
		h.writeMatchedResponse(
			writer,
			request,
			state,
			http.StatusInternalServerError,
			"PLUGIN_REQUEST_FAILED",
			"request plugin failed",
			nil,
		)
		return
	}
	if result.Action == plugin.ShortCircuit {
		h.writeShortCircuit(writer, request, state, result.Response)
		return
	}

	policy := state.Runtime.RetryPolicy()
	if policy.TotalTimeout > 0 {
		ctx, cancel := context.WithTimeout(request.Context(), policy.TotalTimeout)
		defer cancel()
		request = request.WithContext(ctx)
	}

	if request.Body != nil {
		request.Body = http.MaxBytesReader(writer, request.Body, h.maxRequestBodyBytes)
	}
	_ = http.NewResponseController(writer).EnableFullDuplex()
	h.proxy.ServeHTTP(writer, request)
}

func (h *handler) handleProxyError(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(request.Context().Err(), context.Canceled) {
		return
	}
	if request.ProtoMajor == 1 && request.ContentLength != 0 {
		writer.Header().Set("Connection", "close")
	}
	state, matched := requestctx.From(request.Context())
	var responseHookError *responsePluginError
	if errors.As(err, &responseHookError) {
		h.writeMatchedHTTPResponse(
			writer,
			request,
			state,
			http.StatusInternalServerError,
			"PLUGIN_RESPONSE_FAILED",
			http.Header{"Content-Type": []string{"application/json"}},
			mustErrorBody("PLUGIN_RESPONSE_FAILED", "response plugin failed"),
			false,
		)
		return
	}
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		if matched && state.Runtime != nil {
			h.writeMatchedResponse(writer, request, state, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large", nil)
		} else {
			writeError(writer, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large")
		}
		return
	}
	if h.logLimiter.Allow(time.Now()) {
		h.logger.Error(
			"upstream request failed",
			"class",
			publicUpstreamErrorClass(err),
		)
	}
	status := http.StatusBadGateway
	code := "UPSTREAM_CONNECTION_FAILED"
	message := "upstream connection failed"
	if errors.Is(err, upstream.ErrNoHealthyEndpoint) {
		status = http.StatusServiceUnavailable
		code = "UPSTREAM_UNHEALTHY"
		message = "upstream unhealthy"
	} else if isUpstreamTimeout(err) {
		status = http.StatusGatewayTimeout
		code = "UPSTREAM_TIMEOUT"
		message = "upstream timeout"
	} else if upstream.IsTLSFailure(err) {
		code = "UPSTREAM_TLS_FAILED"
		message = "upstream TLS failed"
	}
	if matched && state.Runtime != nil {
		h.writeMatchedResponse(writer, request, state, status, code, message, nil)
		return
	}
	writeError(writer, status, code, message)
}

func publicUpstreamErrorClass(err error) string {
	if errors.Is(err, upstream.ErrNoHealthyEndpoint) {
		return "unhealthy"
	}
	if isUpstreamTimeout(err) {
		return "timeout"
	}
	if upstream.IsTLSFailure(err) {
		return "tls"
	}
	return "connection"
}

func isUpstreamTimeout(err error) bool {
	var networkError net.Error
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &networkError) && networkError.Timeout()
}

func (h *handler) writeMatchedResponse(
	writer http.ResponseWriter,
	request *http.Request,
	state *requestctx.Context,
	status int,
	code string,
	message string,
	headers http.Header,
) {
	if headers == nil {
		headers = make(http.Header)
	} else {
		headers = headers.Clone()
	}
	headers.Set("Content-Type", "application/json")
	h.writeMatchedHTTPResponse(writer, request, state, status, code, headers, mustErrorBody(code, message), true)
}

func (h *handler) writeShortCircuit(
	writer http.ResponseWriter,
	request *http.Request,
	state *requestctx.Context,
	response *plugin.ShortCircuitResponse,
) {
	h.writeMatchedHTTPResponse(
		writer,
		request,
		state,
		response.Status,
		response.Code,
		response.Headers.Clone(),
		append([]byte(nil), response.Body...),
		true,
	)
}

func (h *handler) writeMatchedHTTPResponse(
	writer http.ResponseWriter,
	_ *http.Request,
	state *requestctx.Context,
	status int,
	code string,
	headers http.Header,
	body []byte,
	runHooks bool,
) {
	synthetic := &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	if runHooks {
		if err := state.Runtime.RunResponse(state, synthetic); err != nil {
			status = http.StatusInternalServerError
			code = "PLUGIN_RESPONSE_FAILED"
			synthetic = &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(mustErrorBody(code, "response plugin failed"))),
			}
		}
	}
	state.ResponseCode = synthetic.StatusCode
	state.ResponseError = code
	for name, values := range synthetic.Header {
		writer.Header()[name] = append([]string(nil), values...)
	}
	writer.WriteHeader(synthetic.StatusCode)
	_, _ = io.Copy(writer, synthetic.Body)
}

func mustErrorBody(code, message string) []byte {
	body, _ := json.Marshal(errorBody{Code: code, Message: message})
	return append(body, '\n')
}

type errorLogLimiter struct {
	mu      sync.Mutex
	nextLog time.Time
}

// Allow permits at most one upstream error log per one-second window and is
// safe for concurrent use.
func (l *errorLogLimiter) Allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.nextLog) {
		return false
	}
	l.nextLog = now.Add(time.Second)
	return true
}

func headerHasToken(values []string, wanted string) bool {
	for _, value := range values {
		for token := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), wanted) {
				return true
			}
		}
	}
	return false
}
