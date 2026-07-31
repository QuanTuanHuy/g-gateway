package config

import "time"

const (
	// DefaultMaxRetiredSnapshots is the maximum number of retired runtime
	// snapshots retained while outstanding request leases drain.
	DefaultMaxRetiredSnapshots = 64
	// DefaultHealthWorkers is the number of shared active-health probe workers
	// used when the runtime setting is zero.
	DefaultHealthWorkers = 16
	// DefaultHealthQueueCapacity is the capacity of the shared ready-probe
	// queue used when the runtime setting is zero.
	DefaultHealthQueueCapacity = 4096
)

// BootstrapConfig contains process-level listener, server, telemetry, and
// runtime settings that are separate from canonical routing resources.
type BootstrapConfig struct {
	// HTTP configures the plaintext traffic listener.
	HTTP ListenerConfig
	// HTTPS configures the TLS traffic listener and startup certificate inputs.
	HTTPS TLSListenerConfig
	// Admin configures the private health and metrics listener.
	Admin ListenerConfig
	// Server configures HTTP server limits, timeouts, and shutdown behavior.
	Server ServerConfig
	// Telemetry configures opt-in request metrics and profiling endpoints.
	Telemetry TelemetryConfig
	// Runtime configures bounded snapshot retirement and health scheduling.
	Runtime RuntimeConfig
}

// RuntimeConfig contains process-wide bounds for snapshot and health runtime
// resources.
type RuntimeConfig struct {
	// MaxRetiredSnapshots bounds retired snapshots awaiting final lease
	// release; zero is replaced by DefaultMaxRetiredSnapshots during gateway
	// construction.
	MaxRetiredSnapshots int
	// Health configures the shared active-health coordinator.
	Health HealthRuntimeConfig
}

// HealthRuntimeConfig configures the fixed worker pool and bounded ready queue
// used by active health checks.
type HealthRuntimeConfig struct {
	// Workers is the number of probe workers; zero is replaced by
	// DefaultHealthWorkers during gateway construction.
	Workers int
	// ReadyQueueCapacity is the maximum number of probes ready for workers;
	// zero is replaced by DefaultHealthQueueCapacity during gateway
	// construction.
	ReadyQueueCapacity int
}

// ListenerConfig identifies one listener using Go net.Listen address syntax.
type ListenerConfig struct {
	// Address is passed to net.Listen for the listener.
	Address string
}

// TLSListenerConfig identifies one TLS listener and the certificate files
// loaded during gateway construction.
type TLSListenerConfig struct {
	// Address is passed to net.Listen for the TLS listener.
	Address string
	// CertificateFile is the startup path to the PEM-encoded certificate
	// chain.
	CertificateFile string
	// PrivateKeyFile is the startup path to the matching PEM-encoded private
	// key.
	PrivateKeyFile string
}

// ServerConfig defines HTTP server timeouts and request-size limits. Duration
// fields use time.Duration and byte limits are expressed in bytes.
type ServerConfig struct {
	// ReadHeaderTimeout bounds reading request headers.
	ReadHeaderTimeout time.Duration
	// IdleTimeout bounds how long an idle downstream connection is retained.
	IdleTimeout time.Duration
	// ShutdownTimeout bounds the gateway's default graceful shutdown.
	ShutdownTimeout time.Duration
	// MaxHeaderBytes limits request-header size in bytes.
	MaxHeaderBytes int
	// MaxRequestBodyBytes limits accepted request-body size in bytes.
	MaxRequestBodyBytes int64
}

// TelemetryConfig controls optional request-level metrics and profiling
// handlers. Both features are disabled by their zero values.
type TelemetryConfig struct {
	// RequestMetricsEnabled enables bounded HTTP request metrics.
	RequestMetricsEnabled bool
	// ProfilingEnabled exposes profiling handlers on the private admin
	// listener.
	ProfilingEnabled bool
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
