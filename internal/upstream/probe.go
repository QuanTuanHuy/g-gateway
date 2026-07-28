package upstream

import (
	"context"
	"net/url"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type ProbeTarget struct {
	EndpointID string
	URL        *url.URL
	Generation uint64
	Policy     model.ActiveHealthPolicy
}

type ProbeResult struct {
	Target      ProbeTarget
	Observation Observation
	Duration    time.Duration
}

type Prober interface {
	Probe(context.Context, ProbeTarget) ProbeResult
	CloseIdleConnections()
}
