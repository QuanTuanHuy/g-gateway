package proxy

import (
	"errors"
	"net/http"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

type routeTransport struct{}

func (routeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	state, ok := requestctx.From(request.Context())
	if !ok || state.Runtime == nil {
		return nil, errors.New("proxy request missing compiled runtime route")
	}
	policy := state.Runtime.RetryPolicy()
	maxAttempts := int(policy.MaxAttempts)
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if maxAttempts > 5 {
		maxAttempts = 5
	}
	eligible := retryEligible(request, policy)
	state.Runtime.ActivateUpstream()
	state.Runtime.CreditPrimary()

	var attempted upstream.AttemptSet
	selection, err := state.Runtime.SelectNext(request, &attempted)
	if err != nil {
		return nil, err
	}
	var permit *upstream.RetryPermit
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if !attempted.Add(selection.Ordinal()) {
			if permit != nil {
				permit.Release()
			}
			return nil, upstream.ErrNoHealthyEndpoint
		}
		state.Attempt = attempt
		state.Attempts = attempt
		state.Selection = selection
		attemptRequest, cloneErr := cloneAttemptRequest(request, selection, attempt)
		if cloneErr != nil {
			if permit != nil {
				permit.Release()
			}
			state.RetrySuppressed = "body_not_replayable"
			return nil, cloneErr
		}
		response, roundTripErr := selection.RoundTrip(attemptRequest)
		if permit != nil {
			permit.Release()
			permit = nil
		}
		decision := classifyAttempt(policy, response, roundTripErr)
		selection.Observe(decision.Observation)
		state.UpstreamOutcome = attemptOutcome(decision, roundTripErr)

		if !eligible || !decision.Retry || attempt == maxAttempts || request.Context().Err() != nil {
			if !eligible && decision.Retry {
				state.RetrySuppressed = "request_not_replayable"
			} else if attempt == maxAttempts && decision.Retry {
				state.RetrySuppressed = "attempt_limit"
			}
			return response, roundTripErr
		}
		nextPermit, allowed := state.Runtime.AcquireRetry()
		if !allowed {
			state.RetrySuppressed = "budget"
			return response, roundTripErr
		}
		nextSelection, selectErr := state.Runtime.SelectNext(request, &attempted)
		if selectErr != nil {
			nextPermit.Release()
			state.RetrySuppressed = "no_untried_endpoint"
			return response, roundTripErr
		}
		if response != nil {
			_ = drainRetryResponse(response)
		}
		permit = &nextPermit
		selection = nextSelection
	}
	return nil, errors.New("proxy retry loop exhausted")
}

func attemptOutcome(decision attemptDecision, err error) string {
	if err != nil {
		if decision.Observation.Kind == upstream.OutcomeTimeout {
			return "timeout"
		}
		return "transport_failure"
	}
	if decision.Retry {
		return "retryable_status"
	}
	return "success"
}
