package config

import (
	"errors"
	"fmt"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type documentV8 struct {
	APIVersion    string                   `yaml:"api_version"`
	Runtime       runtimeDocumentV4        `yaml:"runtime"`
	Listeners     listenersDocument        `yaml:"listeners"`
	Server        serverDocument           `yaml:"server"`
	Telemetry     telemetryDocumentV8      `yaml:"telemetry"`
	TrustBundles  []trustBundleDocumentV5  `yaml:"trust_bundles"`
	Certificates  []certificateDocumentV5  `yaml:"certificates"`
	DownstreamTLS *downstreamTLSDocumentV7 `yaml:"downstream_tls"`
	Routes        []routeDocumentV6        `yaml:"routes"`
	Services      []serviceDocumentV6      `yaml:"services"`
	Upstreams     []upstreamDocumentV5     `yaml:"upstreams"`
}

type telemetryDocumentV8 struct {
	RequestMetricsEnabled bool                `yaml:"request_metrics_enabled"`
	ProfilingEnabled      bool                `yaml:"profiling_enabled"`
	AccessLog             accessLogDocumentV8 `yaml:"access_log"`
}

type accessLogDocumentV8 struct {
	Enabled       bool `yaml:"enabled"`
	QueueCapacity *int `yaml:"queue_capacity"`
}

func convertV8(wire documentV8) (BootstrapConfig, model.ResourceSet, error) {
	bootstrap, resources, err := convertV7(documentV7{
		APIVersion: apiVersionV1Alpha7,
		Runtime:    wire.Runtime,
		Listeners:  wire.Listeners,
		Server:     wire.Server,
		Telemetry: telemetryDocument{
			RequestMetricsEnabled: wire.Telemetry.RequestMetricsEnabled,
			ProfilingEnabled:      wire.Telemetry.ProfilingEnabled,
		},
		TrustBundles:  wire.TrustBundles,
		Certificates:  wire.Certificates,
		DownstreamTLS: wire.DownstreamTLS,
		Routes:        wire.Routes,
		Services:      wire.Services,
		Upstreams:     wire.Upstreams,
	})
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}
	accessLog, err := convertAccessLogV8(wire.Telemetry.AccessLog)
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}
	bootstrap.Telemetry.AccessLog = accessLog
	return bootstrap, resources, nil
}

func convertAccessLogV8(wire accessLogDocumentV8) (AccessLogConfig, error) {
	if !wire.Enabled {
		if wire.QueueCapacity != nil && *wire.QueueCapacity != 0 {
			return AccessLogConfig{}, errors.New("telemetry.access_log.queue_capacity: must be zero when disabled")
		}
		return AccessLogConfig{}, nil
	}
	capacity := DefaultAccessLogQueueCapacity
	if wire.QueueCapacity != nil && *wire.QueueCapacity != 0 {
		capacity = *wire.QueueCapacity
	}
	if capacity < 1 || capacity > MaxAccessLogQueueCapacity {
		return AccessLogConfig{}, fmt.Errorf(
			"telemetry.access_log.queue_capacity: must be between 1 and %d",
			MaxAccessLogQueueCapacity,
		)
	}
	return AccessLogConfig{Enabled: true, QueueCapacity: capacity}, nil
}
