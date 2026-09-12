package proxy

import (
	"errors"
	"net/http"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

type attemptResult struct {
	Response  *http.Response
	Selection upstream.Selection
}

type attemptRoundTrip func(upstream.Selection, *http.Request) (*http.Response, error)
type attemptClassifier func(model.RetryPolicy, *http.Response, error) attemptDecision

func executeAttempts(
	request *http.Request,
	state *requestctx.Context,
	roundTrip attemptRoundTrip,
	classify attemptClassifier,
) (attemptResult, error) {
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
		return attemptResult{}, err
	}
	var permit *upstream.RetryPermit
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if !attempted.Add(selection.Ordinal()) {
			if permit != nil {
				permit.Release()
			}
			return attemptResult{Selection: selection}, upstream.ErrNoHealthyEndpoint
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
			return attemptResult{Selection: selection}, cloneErr
		}
		response, roundTripErr := roundTrip(selection, attemptRequest)
		if permit != nil {
			permit.Release()
			permit = nil
		}
		decision := classify(policy, response, roundTripErr)
		selection.Observe(decision.Observation)
		state.UpstreamOutcome = attemptOutcome(decision, roundTripErr)

		result := attemptResult{Response: response, Selection: selection}
		if !eligible || !decision.Retry || attempt == maxAttempts || request.Context().Err() != nil {
			if !eligible && decision.Retry {
				state.RetrySuppressed = "request_not_replayable"
			} else if attempt == maxAttempts && decision.Retry {
				state.RetrySuppressed = "attempt_limit"
			}
			return result, roundTripErr
		}
		nextPermit, allowed := state.Runtime.AcquireRetry()
		if !allowed {
			state.RetrySuppressed = "budget"
			return result, roundTripErr
		}
		nextSelection, selectErr := state.Runtime.SelectNext(request, &attempted)
		if selectErr != nil {
			nextPermit.Release()
			state.RetrySuppressed = "no_untried_endpoint"
			return result, roundTripErr
		}
		if response != nil {
			_ = drainRetryResponse(response)
		}
		permit = &nextPermit
		selection = nextSelection
	}
	return attemptResult{}, errors.New("proxy retry loop exhausted")
}
