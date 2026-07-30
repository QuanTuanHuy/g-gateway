package upstream

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"time"
)

type httpProber struct {
	client    *http.Client
	transport *http.Transport
}

func newHTTPProber() *httpProber {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &httpProber{
		transport: transport,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Probe performs one HTTP GET using a dedicated probe transport, does not
// follow redirects, classifies configured status sets and timeout/transport
// failures, and drains at most 4 KiB plus one byte before closing the body.
func (p *httpProber) Probe(parent context.Context, target ProbeTarget) (result ProbeResult) {
	started := time.Now()
	observation := Observation{Source: SourceActive, Kind: OutcomeTransportFailure}
	result = ProbeResult{Target: target}
	defer func() {
		result.Observation = observation
		result.Duration = time.Since(started)
	}()

	if target.URL == nil {
		return result
	}
	ctx := parent
	cancel := func() {}
	if target.Policy.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, target.Policy.Timeout)
	}
	defer cancel()

	probeURL := *target.URL
	probeURL.Path = target.Policy.Path
	probeURL.RawPath = ""
	probeURL.RawQuery = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL.String(), nil)
	if err != nil {
		return result
	}
	if target.Policy.Host != "" {
		request.Host = target.Policy.Host
	}
	response, err := p.client.Do(request)
	if err != nil {
		observation.Kind = classifyProbeError(ctx, err)
		return result
	}
	observation.Status = response.StatusCode
	switch {
	case slices.Contains(target.Policy.HealthyStatuses, uint16(response.StatusCode)):
		observation.Kind = OutcomeSuccess
	case slices.Contains(target.Policy.UnhealthyStatuses, uint16(response.StatusCode)):
		observation.Kind = OutcomeHTTPFailure
	default:
		observation.Kind = OutcomeNeutral
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10+1))
	_ = response.Body.Close()
	return result
}

// CloseIdleConnections idempotently closes idle connections owned by the HTTP
// probe transport.
func (p *httpProber) CloseIdleConnections() {
	p.transport.CloseIdleConnections()
}

func classifyProbeError(ctx context.Context, err error) OutcomeKind {
	var networkError net.Error
	if ctx.Err() != nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &networkError) && networkError.Timeout()) {
		return OutcomeTimeout
	}
	return OutcomeTransportFailure
}
