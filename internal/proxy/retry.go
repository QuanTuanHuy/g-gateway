package proxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"syscall"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

type retryReason uint8

const (
	retryReasonNone retryReason = iota
	retryReasonConnectFailure
	retryReasonConnectionFailure
	retryReasonResponseHeaderTimeout
	retryReasonStatus
)

type attemptDecision struct {
	Retry       bool
	Reason      retryReason
	Observation upstream.Observation
}

func retryEligible(request *http.Request, policy model.RetryPolicy) bool {
	if request == nil || policy.MaxAttempts <= 1 || request.Context().Err() != nil {
		return false
	}
	method := strings.ToUpper(request.Method)
	if !slices.Contains(policy.Methods, method) {
		return false
	}
	return request.Body == nil || request.Body == http.NoBody || request.GetBody != nil
}

func classifyAttempt(policy model.RetryPolicy, response *http.Response, err error) attemptDecision {
	decision := attemptDecision{
		Observation: upstream.Observation{Source: upstream.SourcePassive, Kind: upstream.OutcomeNeutral},
	}
	if err != nil {
		decision.Observation.Kind = upstream.OutcomeTransportFailure
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			decision.Observation.Kind = upstream.OutcomeTimeout
			decision.Retry = policy.RetryOn.ResponseHeaderTimeout
			decision.Reason = retryReasonResponseHeaderTimeout
			return decision
		}
		var operationError *net.OpError
		if errors.As(err, &operationError) && operationError.Op == "dial" {
			decision.Retry = policy.RetryOn.ConnectFailure
			decision.Reason = retryReasonConnectFailure
			return decision
		}
		if errors.Is(err, io.EOF) ||
			errors.Is(err, io.ErrUnexpectedEOF) ||
			errors.Is(err, syscall.ECONNRESET) ||
			errors.Is(err, syscall.EPIPE) {
			decision.Retry = policy.RetryOn.ConnectionFailure
			decision.Reason = retryReasonConnectionFailure
		}
		return decision
	}
	if response == nil {
		return decision
	}
	decision.Observation.Kind = upstream.OutcomeSuccess
	decision.Observation.Status = response.StatusCode
	if slices.Contains(policy.RetryOn.Statuses, uint16(response.StatusCode)) {
		decision.Retry = true
		decision.Reason = retryReasonStatus
	}
	return decision
}

func cloneAttemptRequest(request *http.Request, selection upstream.Selection, attempt int) (*http.Request, error) {
	if request == nil || selection.Target() == nil {
		return nil, fmt.Errorf("clone attempt request: request and selection target are required")
	}
	cloned := request.Clone(request.Context())
	if attempt > 1 && request.Body != nil && request.Body != http.NoBody {
		if request.GetBody == nil {
			return nil, fmt.Errorf("clone attempt request: body is not replayable")
		}
		body, err := request.GetBody()
		if err != nil {
			return nil, fmt.Errorf("clone attempt request body: %w", err)
		}
		cloned.Body = body
	}
	target := selection.Target()
	urlCopy := *cloned.URL
	urlCopy.Scheme = target.Scheme
	urlCopy.Host = target.Host
	cloned.URL = &urlCopy
	return cloned, nil
}

func drainRetryResponse(response *http.Response) bool {
	if response == nil || response.Body == nil {
		return true
	}
	const limit = int64(32 << 10)
	read, _ := io.Copy(io.Discard, io.LimitReader(response.Body, limit+1))
	_ = response.Body.Close()
	return read <= limit
}
