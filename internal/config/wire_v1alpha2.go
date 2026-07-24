package config

import (
	"encoding/json"
	"fmt"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type documentV2 struct {
	APIVersion string              `yaml:"api_version"`
	Listeners  listenersDocument   `yaml:"listeners"`
	Server     serverDocument      `yaml:"server"`
	Telemetry  telemetryDocument   `yaml:"telemetry"`
	Routes     []routeDocumentV2   `yaml:"routes"`
	Services   []serviceDocumentV2 `yaml:"services"`
	Upstreams  []upstreamDocument  `yaml:"upstreams"`
}

type routeDocumentV2 struct {
	ID          string               `yaml:"id"`
	Priority    int                  `yaml:"priority"`
	Match       routeMatchDocumentV2 `yaml:"match"`
	ServiceRef  string               `yaml:"service_ref"`
	UpstreamRef string               `yaml:"upstream_ref"`
	Plugins     []pluginDocumentV2   `yaml:"plugins"`
}

type routeMatchDocumentV2 struct {
	Hosts   []string              `yaml:"hosts"`
	Path    string                `yaml:"path"`
	Methods []string              `yaml:"methods"`
	Headers []predicateDocumentV2 `yaml:"headers"`
	Queries []predicateDocumentV2 `yaml:"queries"`
}

type predicateDocumentV2 struct {
	Name     string   `yaml:"name"`
	Operator string   `yaml:"operator"`
	Values   []string `yaml:"values"`
}

type serviceDocumentV2 struct {
	ID          string             `yaml:"id"`
	UpstreamRef string             `yaml:"upstream_ref"`
	Plugins     []pluginDocumentV2 `yaml:"plugins"`
}

type pluginDocumentV2 struct {
	Name    string         `yaml:"name"`
	Enabled *bool          `yaml:"enabled"`
	Config  map[string]any `yaml:"config"`
}

func convertV2(wire documentV2) (BootstrapConfig, model.ResourceSet, error) {
	bootstrap, resources, err := convert(document{
		APIVersion: wire.APIVersion,
		Listeners:  wire.Listeners,
		Server:     wire.Server,
		Telemetry:  wire.Telemetry,
		Upstreams:  wire.Upstreams,
	})
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}

	resources.Routes = make([]model.Route, 0, len(wire.Routes))
	for i, route := range wire.Routes {
		plugins, err := convertPlugins(route.Plugins)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("routes[%d]: %w", i, err)
		}
		resources.Routes = append(resources.Routes, model.Route{
			ID:       route.ID,
			Priority: route.Priority,
			Match: model.RouteMatch{
				Hosts:   append([]string(nil), route.Match.Hosts...),
				Path:    route.Match.Path,
				Methods: append([]string(nil), route.Match.Methods...),
				Headers: convertPredicates(route.Match.Headers),
				Queries: convertPredicates(route.Match.Queries),
			},
			ServiceRef:  route.ServiceRef,
			UpstreamRef: route.UpstreamRef,
			Plugins:     plugins,
		})
	}

	resources.Services = make([]model.Service, 0, len(wire.Services))
	for i, service := range wire.Services {
		plugins, err := convertPlugins(service.Plugins)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("services[%d]: %w", i, err)
		}
		resources.Services = append(resources.Services, model.Service{
			ID:          service.ID,
			UpstreamRef: service.UpstreamRef,
			Plugins:     plugins,
		})
	}

	return bootstrap, resources, nil
}

func convertPredicates(in []predicateDocumentV2) []model.Predicate {
	out := make([]model.Predicate, len(in))
	for i := range in {
		out[i] = model.Predicate{
			Name:     in[i].Name,
			Operator: model.PredicateOperator(in[i].Operator),
			Values:   append([]string(nil), in[i].Values...),
		}
	}
	return out
}

func convertPlugins(in []pluginDocumentV2) ([]model.PluginAttachment, error) {
	out := make([]model.PluginAttachment, 0, len(in))
	for _, plugin := range in {
		converted, err := convertPlugin(plugin)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

func convertPlugin(raw pluginDocumentV2) (model.PluginAttachment, error) {
	enabled := true
	if raw.Enabled != nil {
		enabled = *raw.Enabled
	}
	encoded, err := json.Marshal(raw.Config)
	if err != nil {
		return model.PluginAttachment{}, fmt.Errorf("plugin %q config: %w", raw.Name, err)
	}
	return model.PluginAttachment{
		Name:      raw.Name,
		Enabled:   enabled,
		RawConfig: append(json.RawMessage(nil), encoded...),
	}, nil
}
