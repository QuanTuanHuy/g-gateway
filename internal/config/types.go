package config

import "time"

const (
	DefaultMaxRetiredSnapshots = 64
	DefaultHealthWorkers       = 16
	DefaultHealthQueueCapacity = 4096
)

type BootstrapConfig struct {
	HTTP      ListenerConfig
	HTTPS     TLSListenerConfig
	Admin     ListenerConfig
	Server    ServerConfig
	Telemetry TelemetryConfig
	Runtime   RuntimeConfig
}

type RuntimeConfig struct {
	MaxRetiredSnapshots int
	Health              HealthRuntimeConfig
}

type HealthRuntimeConfig struct {
	Workers            int
	ReadyQueueCapacity int
}

type ListenerConfig struct {
	Address string
}

type TLSListenerConfig struct {
	Address         string
	CertificateFile string
	PrivateKeyFile  string
}

type ServerConfig struct {
	ReadHeaderTimeout   time.Duration
	IdleTimeout         time.Duration
	ShutdownTimeout     time.Duration
	MaxHeaderBytes      int
	MaxRequestBodyBytes int64
}

type TelemetryConfig struct {
	RequestMetricsEnabled bool
	ProfilingEnabled      bool
}

type document struct {
	APIVersion string             `yaml:"api_version"`
	Listeners  listenersDocument  `yaml:"listeners"`
	Server     serverDocument     `yaml:"server"`
	Telemetry  telemetryDocument  `yaml:"telemetry"`
	Routes     []routeDocument    `yaml:"routes"`
	Upstreams  []upstreamDocument `yaml:"upstreams"`
}

type listenersDocument struct {
	HTTP  listenerDocument    `yaml:"http"`
	HTTPS tlsListenerDocument `yaml:"https"`
	Admin listenerDocument    `yaml:"admin"`
}

type listenerDocument struct {
	Address string `yaml:"address"`
}

type tlsListenerDocument struct {
	Address         string `yaml:"address"`
	CertificateFile string `yaml:"certificate_file"`
	PrivateKeyFile  string `yaml:"private_key_file"`
}

type serverDocument struct {
	ReadHeaderTimeout   string `yaml:"read_header_timeout"`
	IdleTimeout         string `yaml:"idle_timeout"`
	ShutdownTimeout     string `yaml:"shutdown_timeout"`
	MaxHeaderBytes      int    `yaml:"max_header_bytes"`
	MaxRequestBodyBytes int64  `yaml:"max_request_body_bytes"`
}

type telemetryDocument struct {
	RequestMetricsEnabled bool `yaml:"request_metrics_enabled"`
	ProfilingEnabled      bool `yaml:"profiling_enabled"`
}

type routeDocument struct {
	ID          string             `yaml:"id"`
	Match       routeMatchDocument `yaml:"match"`
	UpstreamRef string             `yaml:"upstream_ref"`
}

type routeMatchDocument struct {
	Path    string   `yaml:"path"`
	Methods []string `yaml:"methods"`
}

type upstreamDocument struct {
	ID        string            `yaml:"id"`
	Endpoints []string          `yaml:"endpoints"`
	Transport transportDocument `yaml:"transport"`
}

type transportDocument struct {
	DialTimeout               string `yaml:"dial_timeout"`
	ResponseHeaderTimeout     string `yaml:"response_header_timeout"`
	IdleConnectionTimeout     string `yaml:"idle_connection_timeout"`
	MaxIdleConnections        int    `yaml:"max_idle_connections"`
	MaxIdleConnectionsPerHost int    `yaml:"max_idle_connections_per_host"`
}
