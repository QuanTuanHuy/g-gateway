package config

import (
	"fmt"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type documentV6 struct {
	APIVersion   string                  `yaml:"api_version"`
	Runtime      runtimeDocumentV4       `yaml:"runtime"`
	Listeners    listenersDocument       `yaml:"listeners"`
	Server       serverDocument          `yaml:"server"`
	Telemetry    telemetryDocument       `yaml:"telemetry"`
	TrustBundles []trustBundleDocumentV5 `yaml:"trust_bundles"`
	Certificates []certificateDocumentV5 `yaml:"certificates"`
	Routes       []routeDocumentV6       `yaml:"routes"`
	Services     []serviceDocumentV6     `yaml:"services"`
	Upstreams    []upstreamDocumentV5    `yaml:"upstreams"`
}

type websocketDocumentV6 struct {
	Enabled     *bool   `yaml:"enabled"`
	IdleTimeout *string `yaml:"idle_timeout"`
}

type routeDocumentV6 struct {
	routeDocumentV4 `yaml:",inline"`
	WebSocket       websocketDocumentV6 `yaml:"websocket"`
}

type serviceDocumentV6 struct {
	serviceDocumentV2 `yaml:",inline"`
	WebSocket         websocketDocumentV6 `yaml:"websocket"`
}

func convertV6(wire documentV6) (BootstrapConfig, model.ResourceSet, error) {
	routes := make([]routeDocumentV4, len(wire.Routes))
	for index := range wire.Routes {
		routes[index] = wire.Routes[index].routeDocumentV4
	}
	services := make([]serviceDocumentV2, len(wire.Services))
	for index := range wire.Services {
		services[index] = wire.Services[index].serviceDocumentV2
	}
	bootstrap, resources, err := convertV5(documentV5{
		APIVersion:   apiVersionV1Alpha5,
		Runtime:      wire.Runtime,
		Listeners:    wire.Listeners,
		Server:       wire.Server,
		Telemetry:    wire.Telemetry,
		TrustBundles: wire.TrustBundles,
		Certificates: wire.Certificates,
		Routes:       routes,
		Services:     services,
		Upstreams:    wire.Upstreams,
	})
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}
	for index := range resources.Routes {
		policy, convertErr := convertWebSocketV6(fmt.Sprintf("routes[%d].websocket", index), wire.Routes[index].WebSocket)
		if convertErr != nil {
			return BootstrapConfig{}, model.ResourceSet{}, convertErr
		}
		resources.Routes[index].WebSocket = policy
	}
	for index := range resources.Services {
		policy, convertErr := convertWebSocketV6(fmt.Sprintf("services[%d].websocket", index), wire.Services[index].WebSocket)
		if convertErr != nil {
			return BootstrapConfig{}, model.ResourceSet{}, convertErr
		}
		resources.Services[index].WebSocket = policy
	}
	return bootstrap, resources, nil
}

func convertWebSocketV6(field string, wire websocketDocumentV6) (model.WebSocketPolicyOverride, error) {
	out := model.WebSocketPolicyOverride{Enabled: wire.Enabled}
	if wire.IdleTimeout == nil {
		return out, nil
	}
	duration, err := parseDuration(field+".idle_timeout", *wire.IdleTimeout)
	if err != nil {
		return model.WebSocketPolicyOverride{}, err
	}
	if duration < 0 {
		return model.WebSocketPolicyOverride{}, fmt.Errorf("%s.idle_timeout: must be non-negative", field)
	}
	out.IdleTimeout = &duration
	return out, nil
}
