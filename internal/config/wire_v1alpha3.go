package config

import (
	"fmt"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type documentV3 struct {
	APIVersion string               `yaml:"api_version"`
	Runtime    runtimeDocumentV3    `yaml:"runtime"`
	Listeners  listenersDocument    `yaml:"listeners"`
	Server     serverDocument       `yaml:"server"`
	Telemetry  telemetryDocument    `yaml:"telemetry"`
	Routes     []routeDocumentV2    `yaml:"routes"`
	Services   []serviceDocumentV2  `yaml:"services"`
	Upstreams  []upstreamDocumentV3 `yaml:"upstreams"`
}

type endpointDocumentV3 struct {
	URL    string  `yaml:"url"`
	Weight *uint32 `yaml:"weight"`
}

type hashKeySourceDocumentV3 struct {
	Type  string `yaml:"type"`
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type balancerDocumentV3 struct {
	Type    string `yaml:"type"`
	HashKey struct {
		Sources []hashKeySourceDocumentV3 `yaml:"sources"`
	} `yaml:"hash_key"`
}

type runtimeDocumentV3 struct {
	MaxRetiredSnapshots *int `yaml:"max_retired_snapshots"`
}

type upstreamDocumentV3 struct {
	ID        string               `yaml:"id"`
	Endpoints []endpointDocumentV3 `yaml:"endpoints"`
	Balancer  balancerDocumentV3   `yaml:"balancer"`
	Transport transportDocument    `yaml:"transport"`
}

func convertV3(wire documentV3) (BootstrapConfig, model.ResourceSet, error) {
	bootstrap, resources, err := convertV2(documentV2{
		APIVersion: wire.APIVersion,
		Listeners:  wire.Listeners,
		Server:     wire.Server,
		Telemetry:  wire.Telemetry,
		Routes:     wire.Routes,
		Services:   wire.Services,
	})
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}
	if wire.Runtime.MaxRetiredSnapshots != nil {
		bootstrap.Runtime.MaxRetiredSnapshots = *wire.Runtime.MaxRetiredSnapshots
	}

	resources.Upstreams = make([]model.Upstream, 0, len(wire.Upstreams))
	for upstreamIndex, upstream := range wire.Upstreams {
		transport, err := convertTransport(upstreamIndex, upstream.Transport)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		endpoints := make([]model.Endpoint, len(upstream.Endpoints))
		for endpointIndex, endpoint := range upstream.Endpoints {
			weight := uint32(1)
			if endpoint.Weight != nil {
				weight = *endpoint.Weight
			}
			endpoints[endpointIndex] = model.Endpoint{
				URL:    endpoint.URL,
				Weight: weight,
			}
		}
		sources := make([]model.HashKeySource, len(upstream.Balancer.HashKey.Sources))
		for sourceIndex, source := range upstream.Balancer.HashKey.Sources {
			sources[sourceIndex] = model.HashKeySource{
				Type:  model.HashSourceType(source.Type),
				Name:  source.Name,
				Value: source.Value,
			}
		}
		resources.Upstreams = append(resources.Upstreams, model.Upstream{
			ID:        upstream.ID,
			Endpoints: endpoints,
			Balancer: model.BalancerPolicy{
				Type: model.BalancerType(upstream.Balancer.Type),
				HashKey: model.HashKeyPolicy{
					Sources: sources,
				},
			},
			Transport: transport,
			Retry:     model.RetryPolicy{MaxAttempts: 1},
		})
	}
	return bootstrap, resources, nil
}

func convertTransport(index int, wire transportDocument) (model.TransportConfig, error) {
	dialTimeout, err := parseDuration(fmt.Sprintf("upstreams[%d].transport.dial_timeout", index), wire.DialTimeout)
	if err != nil {
		return model.TransportConfig{}, err
	}
	responseHeaderTimeout, err := parseDuration(fmt.Sprintf("upstreams[%d].transport.response_header_timeout", index), wire.ResponseHeaderTimeout)
	if err != nil {
		return model.TransportConfig{}, err
	}
	idleConnectionTimeout, err := parseDuration(fmt.Sprintf("upstreams[%d].transport.idle_connection_timeout", index), wire.IdleConnectionTimeout)
	if err != nil {
		return model.TransportConfig{}, err
	}
	return model.TransportConfig{
		Protocol:                  model.TransportProtocolHTTP1,
		DialTimeout:               dialTimeout,
		ResponseHeaderTimeout:     responseHeaderTimeout,
		IdleConnectionTimeout:     idleConnectionTimeout,
		MaxIdleConnections:        wire.MaxIdleConnections,
		MaxIdleConnectionsPerHost: wire.MaxIdleConnectionsPerHost,
	}, nil
}
