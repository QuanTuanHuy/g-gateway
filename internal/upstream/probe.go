package upstream

import (
	"context"
	"net/url"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

// ProbeTarget identifies one immutable endpoint generation and its active
// health policy. URL and policy slices must not be mutated while probing.
type ProbeTarget struct {
	// EndpointID is the canonical endpoint identity.
	EndpointID string
	// URL is the canonical endpoint URL used as the probe authority.
	URL *url.URL
	// Generation identifies the health runtime eligible to consume the result.
	Generation uint64
	// Policy contains the normalized active-health settings for this probe.
	Policy model.ActiveHealthPolicy
}

// ProbeResult contains the target, classified observation, and elapsed probe
// duration.
type ProbeResult struct {
	// Target is copied from the probe request.
	Target ProbeTarget
	// Observation is the active-health outcome.
	Observation Observation
	// Duration is the elapsed wall-clock time spent probing.
	Duration time.Duration
}

// Prober performs cancellable active health checks and owns any idle
// connections used by those checks.
type Prober interface {
	// Probe returns one classified result and must honor context cancellation.
	Probe(context.Context, ProbeTarget) ProbeResult
	// CloseIdleConnections idempotently releases idle probe connections.
	CloseIdleConnections()
}
