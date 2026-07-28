package config

import (
	"fmt"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type documentV4 struct {
	APIVersion string               `yaml:"api_version"`
	Runtime    runtimeDocumentV4    `yaml:"runtime"`
	Listeners  listenersDocument    `yaml:"listeners"`
	Server     serverDocument       `yaml:"server"`
	Telemetry  telemetryDocument    `yaml:"telemetry"`
	Routes     []routeDocumentV4    `yaml:"routes"`
	Services   []serviceDocumentV2  `yaml:"services"`
	Upstreams  []upstreamDocumentV4 `yaml:"upstreams"`
}

type runtimeDocumentV4 struct {
	MaxRetiredSnapshots *int `yaml:"max_retired_snapshots"`
	Health              struct {
		Workers            *int `yaml:"workers"`
		ReadyQueueCapacity *int `yaml:"ready_queue_capacity"`
	} `yaml:"health"`
}

type routeDocumentV4 struct {
	routeDocumentV2 `yaml:",inline"`
	Resilience      struct {
		TotalTimeout *string            `yaml:"total_timeout"`
		MaxAttempts  *uint8             `yaml:"max_attempts"`
		Methods      *[]string          `yaml:"methods"`
		RetryOn      *retryOnDocumentV4 `yaml:"retry_on"`
	} `yaml:"resilience"`
}

type upstreamDocumentV4 struct {
	upstreamDocumentV3 `yaml:",inline"`
	Health             healthDocumentV4 `yaml:"health"`
	Retry              retryDocumentV4  `yaml:"retry"`
}

type healthDocumentV4 struct {
	Active  *activeHealthDocumentV4  `yaml:"active"`
	Passive *passiveHealthDocumentV4 `yaml:"passive"`
}

type activeHealthDocumentV4 struct {
	Type              *string   `yaml:"type"`
	Timeout           *string   `yaml:"timeout"`
	HealthyInterval   *string   `yaml:"healthy_interval"`
	UnhealthyInterval *string   `yaml:"unhealthy_interval"`
	HealthySuccesses  *uint8    `yaml:"healthy_successes"`
	HTTPFailures      *uint8    `yaml:"http_failures"`
	TransportFailures *uint8    `yaml:"transport_failures"`
	Timeouts          *uint8    `yaml:"timeouts"`
	HealthyStatuses   *[]uint16 `yaml:"healthy_statuses"`
	UnhealthyStatuses *[]uint16 `yaml:"unhealthy_statuses"`
	Path              *string   `yaml:"path"`
	Host              *string   `yaml:"host"`
}

type passiveHealthDocumentV4 struct {
	HTTPFailures      *uint8    `yaml:"http_failures"`
	TransportFailures *uint8    `yaml:"transport_failures"`
	Timeouts          *uint8    `yaml:"timeouts"`
	UnhealthyStatuses *[]uint16 `yaml:"unhealthy_statuses"`
}

type retryDocumentV4 struct {
	MaxAttempts  *uint8                 `yaml:"max_attempts"`
	Methods      *[]string              `yaml:"methods"`
	RetryOn      *retryOnDocumentV4     `yaml:"retry_on"`
	Budget       *retryBudgetDocumentV4 `yaml:"budget"`
	TotalTimeout *string                `yaml:"total_timeout"`
}

type retryOnDocumentV4 struct {
	ConnectFailure        *bool     `yaml:"connect_failure"`
	ConnectionFailure     *bool     `yaml:"connection_failure"`
	ResponseHeaderTimeout *bool     `yaml:"response_header_timeout"`
	Statuses              *[]uint16 `yaml:"statuses"`
}

type retryBudgetDocumentV4 struct {
	RatioPer1000 *uint16 `yaml:"ratio_per_1000"`
	Burst        *uint16 `yaml:"burst"`
	MaxInflight  *uint16 `yaml:"max_inflight"`
}

func convertV4(wire documentV4) (BootstrapConfig, model.ResourceSet, error) {
	routes := make([]routeDocumentV2, len(wire.Routes))
	for i := range wire.Routes {
		routes[i] = wire.Routes[i].routeDocumentV2
	}
	upstreams := make([]upstreamDocumentV3, len(wire.Upstreams))
	for i := range wire.Upstreams {
		upstreams[i] = wire.Upstreams[i].upstreamDocumentV3
	}
	bootstrap, resources, err := convertV3(documentV3{
		APIVersion: apiVersionV1Alpha3,
		Runtime:    runtimeDocumentV3{MaxRetiredSnapshots: wire.Runtime.MaxRetiredSnapshots},
		Listeners:  wire.Listeners, Server: wire.Server, Telemetry: wire.Telemetry,
		Routes: routes, Services: wire.Services, Upstreams: upstreams,
	})
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}
	if wire.Runtime.Health.Workers != nil {
		bootstrap.Runtime.Health.Workers = *wire.Runtime.Health.Workers
	}
	if wire.Runtime.Health.ReadyQueueCapacity != nil {
		bootstrap.Runtime.Health.ReadyQueueCapacity = *wire.Runtime.Health.ReadyQueueCapacity
	}
	for i := range resources.Routes {
		override, err := convertRouteResilienceV4(i, wire.Routes[i].Resilience)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		resources.Routes[i].Resilience = override
	}
	for i := range resources.Upstreams {
		health, err := convertHealthV4(i, wire.Upstreams[i].Health)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		retry, err := convertRetryV4(i, wire.Upstreams[i].Retry)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		resources.Upstreams[i].Health, resources.Upstreams[i].Retry = health, retry
	}
	return bootstrap, resources, nil
}

func convertRetryV4(index int, wire retryDocumentV4) (model.RetryPolicy, error) {
	out := model.RetryPolicy{
		MaxAttempts: 1, Methods: []string{"GET", "HEAD", "OPTIONS"},
		RetryOn:      model.RetryOnPolicy{ConnectFailure: true, ConnectionFailure: true, ResponseHeaderTimeout: true},
		Budget:       model.RetryBudgetPolicy{RatioPer1000: 100, Burst: 10, MaxInflight: 32},
		TotalTimeout: 30 * time.Second,
	}
	if wire.MaxAttempts != nil {
		out.MaxAttempts = *wire.MaxAttempts
	}
	if wire.Methods != nil {
		out.Methods = append([]string{}, (*wire.Methods)...)
	}
	if wire.RetryOn != nil {
		out.RetryOn = convertRetryOnV4(*wire.RetryOn, out.RetryOn)
	}
	if wire.Budget != nil {
		if wire.Budget.RatioPer1000 != nil {
			out.Budget.RatioPer1000 = *wire.Budget.RatioPer1000
		}
		if wire.Budget.Burst != nil {
			out.Budget.Burst = *wire.Budget.Burst
		}
		if wire.Budget.MaxInflight != nil {
			out.Budget.MaxInflight = *wire.Budget.MaxInflight
		}
	}
	if wire.TotalTimeout != nil {
		value, err := parseDuration(fmt.Sprintf("upstreams[%d].retry.total_timeout", index), *wire.TotalTimeout)
		if err != nil {
			return model.RetryPolicy{}, err
		}
		out.TotalTimeout = value
	}
	return out, nil
}

func convertRetryOnV4(w retryOnDocumentV4, out model.RetryOnPolicy) model.RetryOnPolicy {
	if w.ConnectFailure != nil {
		out.ConnectFailure = *w.ConnectFailure
	}
	if w.ConnectionFailure != nil {
		out.ConnectionFailure = *w.ConnectionFailure
	}
	if w.ResponseHeaderTimeout != nil {
		out.ResponseHeaderTimeout = *w.ResponseHeaderTimeout
	}
	if w.Statuses != nil {
		out.Statuses = append([]uint16{}, (*w.Statuses)...)
	}
	return out
}

func convertRouteResilienceV4(index int, wire struct {
	TotalTimeout *string            `yaml:"total_timeout"`
	MaxAttempts  *uint8             `yaml:"max_attempts"`
	Methods      *[]string          `yaml:"methods"`
	RetryOn      *retryOnDocumentV4 `yaml:"retry_on"`
}) (model.RouteResiliencePolicy, error) {
	out := model.RouteResiliencePolicy{MaxAttempts: wire.MaxAttempts, Methods: wire.Methods}
	if wire.TotalTimeout != nil {
		value, err := parseDuration(fmt.Sprintf("routes[%d].resilience.total_timeout", index), *wire.TotalTimeout)
		if err != nil {
			return out, err
		}
		out.TotalTimeout = &value
	}
	if wire.RetryOn != nil {
		value := convertRetryOnV4(*wire.RetryOn, model.RetryOnPolicy{})
		out.RetryOn = &value
	}
	return out, nil
}

func convertHealthV4(index int, wire healthDocumentV4) (model.HealthPolicy, error) {
	var out model.HealthPolicy
	if wire.Active != nil {
		active, err := convertActiveHealthV4(index, *wire.Active)
		if err != nil {
			return out, err
		}
		out.Active = &active
	}
	if wire.Passive != nil {
		p := model.PassiveHealthPolicy{HTTPFailures: 5, TransportFailures: 2, Timeouts: 2, UnhealthyStatuses: []uint16{429, 500, 502, 503, 504}}
		if wire.Passive.HTTPFailures != nil {
			p.HTTPFailures = *wire.Passive.HTTPFailures
		}
		if wire.Passive.TransportFailures != nil {
			p.TransportFailures = *wire.Passive.TransportFailures
		}
		if wire.Passive.Timeouts != nil {
			p.Timeouts = *wire.Passive.Timeouts
		}
		if wire.Passive.UnhealthyStatuses != nil {
			p.UnhealthyStatuses = append([]uint16{}, (*wire.Passive.UnhealthyStatuses)...)
		}
		out.Passive = &p
	}
	return out, nil
}

func convertActiveHealthV4(index int, wire activeHealthDocumentV4) (model.ActiveHealthPolicy, error) {
	kind := model.HealthCheckHTTP
	if wire.Type != nil {
		kind = model.HealthCheckType(*wire.Type)
	}
	out := model.ActiveHealthPolicy{Type: kind, Timeout: time.Second, HealthyInterval: 5 * time.Second, UnhealthyInterval: 2 * time.Second, HealthySuccesses: 2, TransportFailures: 2, Timeouts: 2}
	if kind == model.HealthCheckHTTP {
		out.HTTPFailures, out.HealthyStatuses, out.UnhealthyStatuses, out.Path = 3, []uint16{200, 204}, []uint16{429, 500, 502, 503, 504}, "/"
	}
	durations := []struct {
		name string
		raw  *string
		dst  *time.Duration
	}{
		{"timeout", wire.Timeout, &out.Timeout}, {"healthy_interval", wire.HealthyInterval, &out.HealthyInterval}, {"unhealthy_interval", wire.UnhealthyInterval, &out.UnhealthyInterval},
	}
	for _, d := range durations {
		if d.raw != nil {
			value, err := parseDuration(fmt.Sprintf("upstreams[%d].health.active.%s", index, d.name), *d.raw)
			if err != nil {
				return out, err
			}
			*d.dst = value
		}
	}
	if wire.HealthySuccesses != nil {
		out.HealthySuccesses = *wire.HealthySuccesses
	}
	if wire.HTTPFailures != nil {
		out.HTTPFailures = *wire.HTTPFailures
	}
	if wire.TransportFailures != nil {
		out.TransportFailures = *wire.TransportFailures
	}
	if wire.Timeouts != nil {
		out.Timeouts = *wire.Timeouts
	}
	if wire.HealthyStatuses != nil {
		out.HealthyStatuses = append([]uint16{}, (*wire.HealthyStatuses)...)
	}
	if wire.UnhealthyStatuses != nil {
		out.UnhealthyStatuses = append([]uint16{}, (*wire.UnhealthyStatuses)...)
	}
	if wire.Path != nil {
		out.Path = *wire.Path
	}
	if wire.Host != nil {
		out.Host = *wire.Host
	}
	return out, nil
}
