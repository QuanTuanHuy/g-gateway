package upstream

import (
	"context"
	"net"
	"time"
)

type tcpProber struct {
	dialer net.Dialer
}

func newTCPProber() *tcpProber {
	return &tcpProber{}
}

func (p *tcpProber) Probe(parent context.Context, target ProbeTarget) (result ProbeResult) {
	started := time.Now()
	result = ProbeResult{
		Target:      target,
		Observation: Observation{Source: SourceActive, Kind: OutcomeTransportFailure},
	}
	defer func() {
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
	connection, err := p.dialer.DialContext(ctx, "tcp", target.URL.Host)
	if err != nil {
		result.Observation.Kind = classifyProbeError(ctx, err)
		return result
	}
	_ = connection.Close()
	result.Observation.Kind = OutcomeSuccess
	return result
}

func (p *tcpProber) CloseIdleConnections() {}
