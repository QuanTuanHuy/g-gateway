package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"go.yaml.in/yaml/v3"
)

func Load(path string) (BootstrapConfig, model.ResourceSet, error) {
	file, err := os.Open(path)
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	return Decode(file)
}

func Decode(r io.Reader) (BootstrapConfig, model.ResourceSet, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("read config: %w", err)
	}

	var header versionHeader
	if err := yaml.Unmarshal(data, &header); err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("decode config header: %w", err)
	}

	switch header.APIVersion {
	case apiVersionV1Alpha1:
		var wire document
		if err := decodeStrict(data, &wire); err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		bootstrap, resources, err := convert(wire)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		if err := validateV1(wire.APIVersion, &bootstrap, &resources); err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		return bootstrap, resources, nil
	case apiVersionV1Alpha2:
		var wire documentV2
		if err := decodeStrict(data, &wire); err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		bootstrap, resources, err := convertV2(wire)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		if err := validateV2(wire.APIVersion, &bootstrap, &resources); err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		return bootstrap, resources, nil
	default:
		return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("api_version: unsupported %q", header.APIVersion)
	}
}

type versionHeader struct {
	APIVersion string `yaml:"api_version"`
}

func decodeStrict(data []byte, dst any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return fmt.Errorf("decode trailing config: %w", err)
		}
		return fmt.Errorf("decode config: multiple YAML documents are not allowed")
	}
	return nil
}

func convert(wire document) (BootstrapConfig, model.ResourceSet, error) {
	readHeaderTimeout, err := parseDuration("server.read_header_timeout", wire.Server.ReadHeaderTimeout)
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}
	idleTimeout, err := parseDuration("server.idle_timeout", wire.Server.IdleTimeout)
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}
	shutdownTimeout, err := parseDuration("server.shutdown_timeout", wire.Server.ShutdownTimeout)
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}

	bootstrap := BootstrapConfig{
		HTTP: ListenerConfig{Address: wire.Listeners.HTTP.Address},
		HTTPS: TLSListenerConfig{
			Address:         wire.Listeners.HTTPS.Address,
			CertificateFile: wire.Listeners.HTTPS.CertificateFile,
			PrivateKeyFile:  wire.Listeners.HTTPS.PrivateKeyFile,
		},
		Admin: ListenerConfig{Address: wire.Listeners.Admin.Address},
		Server: ServerConfig{
			ReadHeaderTimeout:   readHeaderTimeout,
			IdleTimeout:         idleTimeout,
			ShutdownTimeout:     shutdownTimeout,
			MaxHeaderBytes:      wire.Server.MaxHeaderBytes,
			MaxRequestBodyBytes: wire.Server.MaxRequestBodyBytes,
		},
		Telemetry: TelemetryConfig{
			RequestMetricsEnabled: wire.Telemetry.RequestMetricsEnabled,
			ProfilingEnabled:      wire.Telemetry.ProfilingEnabled,
		},
	}

	resources := model.ResourceSet{
		Routes:    make([]model.Route, 0, len(wire.Routes)),
		Upstreams: make([]model.Upstream, 0, len(wire.Upstreams)),
	}
	for _, route := range wire.Routes {
		resources.Routes = append(resources.Routes, model.Route{
			ID: route.ID,
			Match: model.RouteMatch{
				Path:    route.Match.Path,
				Methods: append([]string(nil), route.Match.Methods...),
			},
			UpstreamRef: route.UpstreamRef,
		})
	}
	for i, upstream := range wire.Upstreams {
		dialTimeout, err := parseDuration(fmt.Sprintf("upstreams[%d].transport.dial_timeout", i), upstream.Transport.DialTimeout)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		responseHeaderTimeout, err := parseDuration(fmt.Sprintf("upstreams[%d].transport.response_header_timeout", i), upstream.Transport.ResponseHeaderTimeout)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		idleConnectionTimeout, err := parseDuration(fmt.Sprintf("upstreams[%d].transport.idle_connection_timeout", i), upstream.Transport.IdleConnectionTimeout)
		if err != nil {
			return BootstrapConfig{}, model.ResourceSet{}, err
		}
		endpoints := make([]model.Endpoint, len(upstream.Endpoints))
		for endpointIndex, rawURL := range upstream.Endpoints {
			endpoints[endpointIndex] = model.Endpoint{URL: rawURL, Weight: 1}
		}
		resources.Upstreams = append(resources.Upstreams, model.Upstream{
			ID:        upstream.ID,
			Endpoints: endpoints,
			Balancer: model.BalancerPolicy{
				Type: model.BalancerWeightedRoundRobin,
			},
			Transport: model.TransportConfig{
				DialTimeout:               dialTimeout,
				ResponseHeaderTimeout:     responseHeaderTimeout,
				IdleConnectionTimeout:     idleConnectionTimeout,
				MaxIdleConnections:        upstream.Transport.MaxIdleConnections,
				MaxIdleConnectionsPerHost: upstream.Transport.MaxIdleConnectionsPerHost,
			},
		})
	}

	return bootstrap, resources, nil
}

func parseDuration(field, raw string) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	return duration, nil
}
