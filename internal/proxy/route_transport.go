package proxy

import (
	"errors"
	"net/http"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

type routeTransport struct{}

// RoundTrip executes one bounded gateway attempt transaction. It activates
// health, credits one primary request, selects distinct healthy or unknown
// endpoints, reconstructs replayable bodies for retries, acquires and releases
// retry permits, drains at most 32 KiB plus one detection byte from retryable
// responses, observes every transport attempt, and returns the final response
// or transport error. Client
// cancellation, total-deadline expiry, non-replayable bodies, budget
// exhaustion, and lack of an untried endpoint suppress further retries.
func (routeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	state, ok := requestctx.From(request.Context())
	if !ok || state.Runtime == nil {
		return nil, errors.New("proxy request missing compiled runtime route")
	}
	result, err := executeAttempts(request, state,
		func(selection upstream.Selection, attempt *http.Request) (*http.Response, error) {
			return selection.RoundTrip(attempt)
		}, classifyAttempt)
	return result.Response, err
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
