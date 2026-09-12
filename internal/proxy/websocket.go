package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	gatewayruntime "github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/tunnel"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
	websocketpkg "github.com/QuanTuanHuy/g-gateway/internal/websocket"
)

func (h *handler) serveWebSocket(
	writer http.ResponseWriter,
	request *http.Request,
	state *requestctx.Context,
	route *gatewayruntime.CompiledRoute,
	policy model.WebSocketPolicy,
) {
	captured, err := websocketpkg.ValidateRequest(request)
	if err != nil {
		h.observeWebSocket("invalid_request")
		h.writeMatchedResponse(writer, request, state, http.StatusBadRequest, "INVALID_WEBSOCKET_HANDSHAKE", "invalid WebSocket handshake", nil)
		return
	}
	pluginResult := route.RunRequest(state, request)
	if pluginResult.Err != nil {
		h.observeWebSocket("plugin_failure")
		h.writeMatchedResponse(writer, request, state, http.StatusInternalServerError, "PLUGIN_REQUEST_FAILED", "request plugin failed", nil)
		return
	}
	if pluginResult.Action == plugin.ShortCircuit {
		h.observeWebSocket("plugin_failure")
		h.writeShortCircuit(writer, request, state, pluginResult.Response)
		return
	}
	mutated, validateErr := websocketpkg.ValidateRequest(request)
	if validateErr != nil || !captured.Equal(mutated) {
		h.observeWebSocket("plugin_failure")
		h.writeMatchedResponse(writer, request, state, http.StatusInternalServerError, "PLUGIN_REQUEST_FAILED", "request plugin failed", nil)
		return
	}

	tunnelCtx, cancel := context.WithCancel(context.Background())
	stopClientLink := context.AfterFunc(request.Context(), cancel)
	totalTimeout := state.Runtime.RetryPolicy().TotalTimeout
	var timer *time.Timer
	if totalTimeout > 0 {
		timer = time.AfterFunc(totalTimeout, cancel)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		stopClientLink()
		if timer != nil {
			timer.Stop()
		}
		cancel()
	}()

	outbound := request.Clone(tunnelCtx)
	outbound.Header = request.Header.Clone()
	outbound.RequestURI = ""
	outbound.Body = http.NoBody
	outbound.ContentLength = 0
	rebuildForwardingHeaders(outbound.Header, request)
	websocketpkg.CanonicalizeRequestHeaders(outbound.Header, captured)
	result, err := executeAttempts(outbound, state,
		func(selection upstream.Selection, attempt *http.Request) (*http.Response, error) {
			return selection.RoundTripUpgrade(attempt)
		}, classifyWebSocketAttempt(captured))
	if err != nil {
		h.observeWebSocket("upstream_failure")
		h.writeWebSocketUpstreamError(writer, request, state, err)
		return
	}
	response := result.Response
	if response == nil {
		h.observeWebSocket("upstream_failure")
		h.writeMatchedResponse(writer, request, state, http.StatusBadGateway, "UPSTREAM_CONNECTION_FAILED", "upstream connection failed", nil)
		return
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		h.observeWebSocket("upstream_rejected")
		if err := h.forwardResponse(writer, request, state, response); err != nil {
			h.handleForwardResponseError(writer, request, state, err)
		}
		return
	}
	negotiated, err := websocketpkg.ValidateResponse(captured, response)
	if err != nil {
		_ = response.Body.Close()
		h.observeWebSocket("upstream_failure")
		h.writeMatchedResponse(writer, request, state, http.StatusBadGateway, "UPSTREAM_WEBSOCKET_HANDSHAKE_INVALID", "upstream WebSocket handshake invalid", nil)
		return
	}
	if err := state.Runtime.RunResponse(state, response); err != nil {
		_ = response.Body.Close()
		h.observeWebSocket("plugin_failure")
		h.writeMatchedErrorWithoutHooks(writer, state, http.StatusInternalServerError, "PLUGIN_RESPONSE_FAILED", "response plugin failed")
		return
	}
	if err := websocketpkg.ValidateFinalResponse(negotiated, response); err != nil {
		_ = response.Body.Close()
		h.observeWebSocket("plugin_failure")
		h.writeMatchedErrorWithoutHooks(writer, state, http.StatusInternalServerError, "PLUGIN_RESPONSE_FAILED", "response plugin failed")
		return
	}
	lease, err := result.Selection.AcquireTunnelLease()
	if err != nil {
		_ = response.Body.Close()
		h.observeWebSocket("upstream_failure")
		h.writeMatchedErrorWithoutHooks(writer, state, http.StatusBadGateway, "UPSTREAM_CONNECTION_FAILED", "upstream connection failed")
		return
	}
	connection, buffered, err := http.NewResponseController(writer).Hijack()
	if err != nil {
		lease.Release()
		_ = response.Body.Close()
		h.observeWebSocket("upstream_failure")
		h.writeMatchedErrorWithoutHooks(writer, state, http.StatusInternalServerError, "DOWNSTREAM_HIJACK_UNSUPPORTED", "downstream hijack unsupported")
		return
	}
	upstreamStream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		lease.Release()
		_ = response.Body.Close()
		_ = writeHijackedError(buffered, state, http.StatusBadGateway, "UPSTREAM_WEBSOCKET_HANDSHAKE_INVALID", "upstream WebSocket handshake invalid")
		_ = connection.Close()
		h.observeWebSocket("upstream_failure")
		return
	}
	session, err := tunnel.NewSession(downstreamEndpoint(connection, buffered), upstreamEndpoint(upstreamStream), policy.IdleTimeout)
	if err != nil {
		lease.Release()
		_ = upstreamStream.Close()
		_ = writeHijackedError(buffered, state, http.StatusInternalServerError, "DOWNSTREAM_HIJACK_UNSUPPORTED", "downstream hijack unsupported")
		_ = connection.Close()
		h.observeWebSocket("upstream_failure")
		return
	}
	if h.tunnels == nil {
		h.observeWebSocket("draining")
		_ = writeHijackedError(buffered, state, http.StatusServiceUnavailable, "GATEWAY_DRAINING", "gateway draining")
		session.ForceClose(tunnel.ReasonShutdown)
		lease.Release()
		return
	}
	registration, err := h.tunnels.Register(session, func() {
		lease.Release()
		cancel()
	})
	if err != nil {
		h.observeWebSocket("draining")
		_ = writeHijackedError(buffered, state, http.StatusServiceUnavailable, "GATEWAY_DRAINING", "gateway draining")
		session.ForceClose(tunnel.ReasonShutdown)
		lease.Release()
		return
	}
	handshakeResponse := *response
	handshakeResponse.Body = nil
	handshakeResponse.ContentLength = 0
	handshakeResponse.TransferEncoding = nil
	if err := handshakeResponse.Write(buffered); err != nil {
		session.ForceClose(tunnel.ReasonIOError)
		registration.Rollback()
		return
	}
	if err := buffered.Flush(); err != nil {
		session.ForceClose(tunnel.ReasonIOError)
		registration.Rollback()
		return
	}
	stopClientLink()
	if timer != nil {
		timer.Stop()
	}
	state.ResponseCode = http.StatusSwitchingProtocols
	registration.Activate(tunnelCtx)
	h.observeWebSocket("success")
	committed = true
}

func classifyWebSocketAttempt(request websocketpkg.RequestHandshake) attemptClassifier {
	return func(policy model.RetryPolicy, response *http.Response, err error) attemptDecision {
		decision := classifyAttempt(policy, response, err)
		if err == nil && response != nil && response.StatusCode == http.StatusSwitchingProtocols {
			if _, validateErr := websocketpkg.ValidateResponse(request, response); validateErr != nil {
				decision.Retry = policy.RetryOn.ConnectionFailure
				decision.Reason = retryReasonConnectionFailure
				decision.Observation = upstream.Observation{Source: upstream.SourcePassive, Kind: upstream.OutcomeTransportFailure}
				if decision.Retry && response.Body != nil {
					_ = response.Body.Close()
				}
			}
		}
		return decision
	}
}

func (h *handler) writeWebSocketUpstreamError(writer http.ResponseWriter, request *http.Request, state *requestctx.Context, err error) {
	status, code, message := http.StatusBadGateway, "UPSTREAM_CONNECTION_FAILED", "upstream connection failed"
	if errors.Is(err, upstream.ErrNoHealthyEndpoint) {
		status, code, message = http.StatusServiceUnavailable, "UPSTREAM_UNHEALTHY", "upstream unhealthy"
	} else if isUpstreamTimeout(err) {
		status, code, message = http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT", "upstream timeout"
	} else if upstream.IsTLSFailure(err) {
		code, message = "UPSTREAM_TLS_FAILED", "upstream TLS failed"
	}
	h.writeMatchedResponse(writer, request, state, status, code, message, nil)
}

func (h *handler) handleForwardResponseError(writer http.ResponseWriter, request *http.Request, state *requestctx.Context, err error) {
	var pluginErr *responsePluginError
	if errors.As(err, &pluginErr) {
		h.observeWebSocket("plugin_failure")
		h.writeMatchedErrorWithoutHooks(writer, state, http.StatusInternalServerError, "PLUGIN_RESPONSE_FAILED", "response plugin failed")
		return
	}
	h.handleProxyError(writer, request, err)
}

func (h *handler) writeMatchedErrorWithoutHooks(writer http.ResponseWriter, state *requestctx.Context, status int, code, message string) {
	h.writeMatchedHTTPResponse(writer, nil, state, status, code, http.Header{"Content-Type": {"application/json"}}, mustErrorBody(code, message), false)
}

func (h *handler) observeWebSocket(result string) {
	if h.websockets == nil {
		return
	}
	defer func() { _ = recover() }()
	h.websockets.ObserveWebSocketHandshake(result)
}

func downstreamEndpoint(connection net.Conn, buffered *bufio.ReadWriter) tunnel.Endpoint {
	endpoint := tunnel.Endpoint{Reader: buffered, Writer: connection, Closer: connection}
	if closeWriter, ok := connection.(interface{ CloseWrite() error }); ok {
		endpoint.CloseWriter = closeWriter.CloseWrite
	}
	return endpoint
}

func upstreamEndpoint(stream io.ReadWriteCloser) tunnel.Endpoint {
	endpoint := tunnel.Endpoint{Reader: stream, Writer: stream, Closer: stream}
	if closeWriter, ok := stream.(interface{ CloseWrite() error }); ok {
		endpoint.CloseWriter = closeWriter.CloseWrite
	}
	return endpoint
}

func writeHijackedError(buffered *bufio.ReadWriter, state *requestctx.Context, status int, code, message string) error {
	body := mustErrorBody(code, message)
	response := &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json"}, "Connection": {"close"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	state.ResponseCode = status
	state.ResponseError = code
	if err := response.Write(buffered); err != nil {
		return err
	}
	return buffered.Flush()
}
