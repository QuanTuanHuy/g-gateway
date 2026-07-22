package proxy

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type Options struct {
	Route               model.Route
	Target              *url.URL
	Transport           http.RoundTripper
	MaxRequestBodyBytes int64
	Logger              *slog.Logger
}

type handler struct {
	routePath           string
	methods             map[string]struct{}
	allow               string
	maxRequestBodyBytes int64
	proxy               *httputil.ReverseProxy
}

func New(options Options) (http.Handler, error) {
	if options.Target == nil || options.Target.Scheme == "" || options.Target.Host == "" {
		return nil, fmt.Errorf("proxy target must include scheme and host")
	}
	if options.Transport == nil {
		return nil, fmt.Errorf("proxy transport must not be nil")
	}
	if options.MaxRequestBodyBytes <= 0 {
		return nil, fmt.Errorf("max request body bytes must be greater than zero")
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	methods := make(map[string]struct{}, len(options.Route.Match.Methods))
	allowed := append([]string(nil), options.Route.Match.Methods...)
	sort.Strings(allowed)
	for _, method := range allowed {
		methods[method] = struct{}{}
	}

	target := *options.Target
	h := &handler{
		routePath:           options.Route.Match.Path,
		methods:             methods,
		allow:               strings.Join(allowed, ", "),
		maxRequestBodyBytes: options.MaxRequestBodyBytes,
	}
	h.proxy = &httputil.ReverseProxy{
		Transport: options.Transport,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(&target)
			request.Out.Host = request.In.Host
		},
		ErrorHandler: func(response http.ResponseWriter, request *http.Request, err error) {
			if request.Context().Err() != nil {
				return
			}
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				writeError(response, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large")
				return
			}
			options.Logger.Error("upstream request failed", "error", err)
			writeError(response, http.StatusBadGateway, "UPSTREAM_CONNECTION_FAILED", "upstream connection failed")
		},
	}
	return h, nil
}

func (h *handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL == nil || !strings.HasPrefix(request.URL.Path, "/") || request.Method == "" {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid request")
		return
	}
	if request.URL.Path != h.routePath {
		writeError(response, http.StatusNotFound, "ROUTE_NOT_FOUND", "route not found")
		return
	}
	if _, ok := h.methods[request.Method]; !ok {
		response.Header().Set("Allow", h.allow)
		writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if request.Header.Get("Upgrade") != "" || headerHasToken(request.Header.Values("Connection"), "upgrade") {
		writeError(response, http.StatusNotImplemented, "UPGRADE_NOT_SUPPORTED", "upgrade not supported")
		return
	}
	if request.ContentLength > h.maxRequestBodyBytes {
		writeError(response, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large")
		return
	}
	if request.Body != nil {
		request.Body = http.MaxBytesReader(response, request.Body, h.maxRequestBodyBytes)
	}
	h.proxy.ServeHTTP(response, request)
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
