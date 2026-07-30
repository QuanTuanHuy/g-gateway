package benchdataset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"go.yaml.in/yaml/v3"
)

// GatewayRenderOptions overrides listener and TLS paths in rendered gateway
// bootstrap configuration. Empty fields select benchmark defaults.
type GatewayRenderOptions struct {
	// HTTPAddress is the cleartext listener address; the default is ":8080".
	HTTPAddress string
	// HTTPSAddress is the TLS listener address; the default is ":8443".
	HTTPSAddress string
	// AdminAddress is the telemetry listener address; the default is ":9090".
	AdminAddress string
	// CertificateFile is the certificate path visible to the gateway; the
	// default is "/certs/server.crt".
	CertificateFile string
	// PrivateKeyFile is the private-key path visible to the gateway; the
	// default is "/certs/server.key".
	PrivateKeyFile string
}

// RenderGateway translates resources into strict gateway/v1alpha2 YAML with
// benchmark metadata comments and fixed benchmark server and telemetry
// settings. It preserves resource order, does not mutate resources, and
// returns no bytes when plugin translation or YAML encoding fails.
func RenderGateway(
	resources model.ResourceSet,
	metadata Metadata,
	options GatewayRenderOptions,
) ([]byte, error) {
	options = defaultGatewayRenderOptions(options)
	document := gatewayDocument{
		APIVersion: "gateway/v1alpha2",
		Listeners: gatewayListeners{
			HTTP: gatewayListener{Address: options.HTTPAddress},
			HTTPS: gatewayTLSListener{
				Address:         options.HTTPSAddress,
				CertificateFile: options.CertificateFile,
				PrivateKeyFile:  options.PrivateKeyFile,
			},
			Admin: gatewayListener{Address: options.AdminAddress},
		},
		Server: gatewayServer{
			ReadHeaderTimeout:   "5s",
			IdleTimeout:         "1m",
			ShutdownTimeout:     "30s",
			MaxHeaderBytes:      1 << 20,
			MaxRequestBodyBytes: 64 << 20,
		},
		Telemetry: gatewayTelemetry{
			RequestMetricsEnabled: true,
			ProfilingEnabled:      false,
		},
		Routes:    make([]gatewayRoute, 0, len(resources.Routes)),
		Services:  make([]gatewayService, 0, len(resources.Services)),
		Upstreams: make([]gatewayUpstream, 0, len(resources.Upstreams)),
	}
	for _, route := range resources.Routes {
		plugins, err := gatewayPlugins(route.Plugins)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.ID, err)
		}
		document.Routes = append(document.Routes, gatewayRoute{
			ID:       route.ID,
			Priority: route.Priority,
			Match: gatewayRouteMatch{
				Hosts:   append([]string(nil), route.Match.Hosts...),
				Path:    route.Match.Path,
				Methods: append([]string(nil), route.Match.Methods...),
				Headers: gatewayPredicates(route.Match.Headers),
				Queries: gatewayPredicates(route.Match.Queries),
			},
			ServiceRef:  route.ServiceRef,
			UpstreamRef: route.UpstreamRef,
			Plugins:     plugins,
		})
	}
	for _, service := range resources.Services {
		plugins, err := gatewayPlugins(service.Plugins)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", service.ID, err)
		}
		document.Services = append(document.Services, gatewayService{
			ID:          service.ID,
			UpstreamRef: service.UpstreamRef,
			Plugins:     plugins,
		})
	}
	for _, upstream := range resources.Upstreams {
		endpoints := make([]string, len(upstream.Endpoints))
		for i, endpoint := range upstream.Endpoints {
			endpoints[i] = endpoint.URL
		}
		document.Upstreams = append(document.Upstreams, gatewayUpstream{
			ID:        upstream.ID,
			Endpoints: endpoints,
			Transport: gatewayTransport{
				DialTimeout:               upstream.Transport.DialTimeout.String(),
				ResponseHeaderTimeout:     upstream.Transport.ResponseHeaderTimeout.String(),
				IdleConnectionTimeout:     upstream.Transport.IdleConnectionTimeout.String(),
				MaxIdleConnections:        upstream.Transport.MaxIdleConnections,
				MaxIdleConnectionsPerHost: upstream.Transport.MaxIdleConnectionsPerHost,
			},
		})
	}
	rendered, err := yaml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode gateway YAML: %w", err)
	}
	return addMetadataComments(rendered, metadata, false), nil
}

// RenderAPISIX translates resources and supported built-in plugins into
// standalone APISIX YAML using the equivalent parameter-aware router and
// round-robin upstreams. It preserves resource order, appends the standalone
// "#END" marker, does not mutate resources, and returns no bytes on error.
func RenderAPISIX(resources model.ResourceSet, metadata Metadata) ([]byte, error) {
	document := apisixDocument{
		Deployment: apisixDeployment{
			Role: "data_plane",
			RoleDataPlane: apisixRoleDataPlane{
				ConfigProvider: "yaml",
			},
		},
		APISIX: apisixSettings{
			Router: apisixRouter{HTTP: "radixtree_uri_with_parameter"},
		},
		Routes:    make([]apisixRoute, 0, len(resources.Routes)),
		Services:  make([]apisixService, 0, len(resources.Services)),
		Upstreams: make([]apisixUpstream, 0, len(resources.Upstreams)),
	}
	for _, route := range resources.Routes {
		plugins, err := apisixPlugins(route.Plugins)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.ID, err)
		}
		renderedRoute := apisixRoute{
			ID:         route.ID,
			URI:        apisixPath(route.Match.Path),
			Methods:    append([]string(nil), route.Match.Methods...),
			Priority:   route.Priority,
			Vars:       apisixPredicates(route.Match.Headers, route.Match.Queries),
			ServiceID:  route.ServiceRef,
			UpstreamID: route.UpstreamRef,
			Plugins:    plugins,
		}
		if len(route.Match.Hosts) == 1 {
			renderedRoute.Host = route.Match.Hosts[0]
		} else if len(route.Match.Hosts) > 1 {
			renderedRoute.Hosts = append([]string(nil), route.Match.Hosts...)
		}
		document.Routes = append(document.Routes, renderedRoute)
	}
	for _, service := range resources.Services {
		plugins, err := apisixPlugins(service.Plugins)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", service.ID, err)
		}
		document.Services = append(document.Services, apisixService{
			ID:         service.ID,
			UpstreamID: service.UpstreamRef,
			Plugins:    plugins,
		})
	}
	for _, upstream := range resources.Upstreams {
		nodes := make(map[string]int, len(upstream.Endpoints))
		scheme := "http"
		for _, endpoint := range upstream.Endpoints {
			parsed, err := url.Parse(endpoint.URL)
			if err != nil || parsed.Host == "" {
				return nil, fmt.Errorf("upstream %q has invalid endpoint %q", upstream.ID, endpoint.URL)
			}
			scheme = parsed.Scheme
			nodes[parsed.Host] = int(endpoint.Weight)
		}
		document.Upstreams = append(document.Upstreams, apisixUpstream{
			ID:     upstream.ID,
			Type:   "roundrobin",
			Scheme: scheme,
			Nodes:  nodes,
		})
	}
	rendered, err := yaml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode APISIX YAML: %w", err)
	}
	return addMetadataComments(rendered, metadata, true), nil
}

type gatewayDocument struct {
	APIVersion string            `yaml:"api_version"`
	Listeners  gatewayListeners  `yaml:"listeners"`
	Server     gatewayServer     `yaml:"server"`
	Telemetry  gatewayTelemetry  `yaml:"telemetry"`
	Routes     []gatewayRoute    `yaml:"routes"`
	Services   []gatewayService  `yaml:"services,omitempty"`
	Upstreams  []gatewayUpstream `yaml:"upstreams"`
}

type gatewayListeners struct {
	HTTP  gatewayListener    `yaml:"http"`
	HTTPS gatewayTLSListener `yaml:"https"`
	Admin gatewayListener    `yaml:"admin"`
}

type gatewayListener struct {
	Address string `yaml:"address"`
}

type gatewayTLSListener struct {
	Address         string `yaml:"address"`
	CertificateFile string `yaml:"certificate_file"`
	PrivateKeyFile  string `yaml:"private_key_file"`
}

type gatewayServer struct {
	ReadHeaderTimeout   string `yaml:"read_header_timeout"`
	IdleTimeout         string `yaml:"idle_timeout"`
	ShutdownTimeout     string `yaml:"shutdown_timeout"`
	MaxHeaderBytes      int    `yaml:"max_header_bytes"`
	MaxRequestBodyBytes int64  `yaml:"max_request_body_bytes"`
}

type gatewayTelemetry struct {
	RequestMetricsEnabled bool `yaml:"request_metrics_enabled"`
	ProfilingEnabled      bool `yaml:"profiling_enabled"`
}

type gatewayRoute struct {
	ID          string            `yaml:"id"`
	Priority    int               `yaml:"priority,omitempty"`
	Match       gatewayRouteMatch `yaml:"match"`
	ServiceRef  string            `yaml:"service_ref,omitempty"`
	UpstreamRef string            `yaml:"upstream_ref,omitempty"`
	Plugins     []gatewayPlugin   `yaml:"plugins,omitempty"`
}

type gatewayRouteMatch struct {
	Hosts   []string           `yaml:"hosts,omitempty"`
	Path    string             `yaml:"path"`
	Methods []string           `yaml:"methods"`
	Headers []gatewayPredicate `yaml:"headers,omitempty"`
	Queries []gatewayPredicate `yaml:"queries,omitempty"`
}

type gatewayPredicate struct {
	Name     string   `yaml:"name"`
	Operator string   `yaml:"operator"`
	Values   []string `yaml:"values,omitempty"`
}

type gatewayService struct {
	ID          string          `yaml:"id"`
	UpstreamRef string          `yaml:"upstream_ref"`
	Plugins     []gatewayPlugin `yaml:"plugins,omitempty"`
}

type gatewayPlugin struct {
	Name    string         `yaml:"name"`
	Enabled bool           `yaml:"enabled"`
	Config  map[string]any `yaml:"config"`
}

type gatewayUpstream struct {
	ID        string           `yaml:"id"`
	Endpoints []string         `yaml:"endpoints"`
	Transport gatewayTransport `yaml:"transport"`
}

type gatewayTransport struct {
	DialTimeout               string `yaml:"dial_timeout"`
	ResponseHeaderTimeout     string `yaml:"response_header_timeout"`
	IdleConnectionTimeout     string `yaml:"idle_connection_timeout"`
	MaxIdleConnections        int    `yaml:"max_idle_connections"`
	MaxIdleConnectionsPerHost int    `yaml:"max_idle_connections_per_host"`
}

func gatewayPredicates(predicates []model.Predicate) []gatewayPredicate {
	rendered := make([]gatewayPredicate, len(predicates))
	for i, predicate := range predicates {
		rendered[i] = gatewayPredicate{
			Name:     predicate.Name,
			Operator: string(predicate.Operator),
			Values:   append([]string(nil), predicate.Values...),
		}
	}
	return rendered
}

func gatewayPlugins(attachments []model.PluginAttachment) ([]gatewayPlugin, error) {
	rendered := make([]gatewayPlugin, 0, len(attachments))
	for _, attachment := range attachments {
		config := make(map[string]any)
		if len(bytes.TrimSpace(attachment.RawConfig)) > 0 {
			if err := json.Unmarshal(attachment.RawConfig, &config); err != nil {
				return nil, fmt.Errorf("plugin %q config: %w", attachment.Name, err)
			}
		}
		rendered = append(rendered, gatewayPlugin{
			Name:    attachment.Name,
			Enabled: attachment.Enabled,
			Config:  config,
		})
	}
	return rendered, nil
}

type apisixDocument struct {
	Deployment apisixDeployment `yaml:"deployment"`
	APISIX     apisixSettings   `yaml:"apisix"`
	Routes     []apisixRoute    `yaml:"routes"`
	Services   []apisixService  `yaml:"services,omitempty"`
	Upstreams  []apisixUpstream `yaml:"upstreams"`
}

type apisixDeployment struct {
	Role          string              `yaml:"role"`
	RoleDataPlane apisixRoleDataPlane `yaml:"role_data_plane"`
}

type apisixRoleDataPlane struct {
	ConfigProvider string `yaml:"config_provider"`
}

type apisixSettings struct {
	Router apisixRouter `yaml:"router"`
}

type apisixRouter struct {
	HTTP string `yaml:"http"`
}

type apisixRoute struct {
	ID         string         `yaml:"id"`
	URI        string         `yaml:"uri"`
	Host       string         `yaml:"host,omitempty"`
	Hosts      []string       `yaml:"hosts,omitempty"`
	Methods    []string       `yaml:"methods"`
	Priority   int            `yaml:"priority,omitempty"`
	Vars       [][]any        `yaml:"vars,omitempty"`
	ServiceID  string         `yaml:"service_id,omitempty"`
	UpstreamID string         `yaml:"upstream_id,omitempty"`
	Plugins    map[string]any `yaml:"plugins,omitempty"`
}

type apisixService struct {
	ID         string         `yaml:"id"`
	UpstreamID string         `yaml:"upstream_id"`
	Plugins    map[string]any `yaml:"plugins,omitempty"`
}

type apisixUpstream struct {
	ID     string         `yaml:"id"`
	Type   string         `yaml:"type"`
	Scheme string         `yaml:"scheme"`
	Nodes  map[string]int `yaml:"nodes"`
}

func apisixPath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, "{*") && strings.HasSuffix(segment, "}") {
			segments[i] = "*"
			continue
		}
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			segments[i] = ":" + strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
		}
	}
	return strings.Join(segments, "/")
}

func apisixPredicates(headers, queries []model.Predicate) [][]any {
	rendered := make([][]any, 0, len(headers)+len(queries))
	for _, predicate := range headers {
		rendered = append(rendered, apisixPredicate("http_"+apisixVariableName(predicate.Name), predicate))
	}
	for _, predicate := range queries {
		rendered = append(rendered, apisixPredicate("arg_"+apisixVariableName(predicate.Name), predicate))
	}
	return rendered
}

func apisixPredicate(variable string, predicate model.Predicate) []any {
	switch predicate.Operator {
	case model.PredicateExists:
		return []any{variable, "~=", nil}
	case model.PredicateNotExists:
		return []any{variable, "==", nil}
	case model.PredicateEquals:
		return []any{variable, "==", predicate.Values[0]}
	case model.PredicateNotEquals:
		return []any{variable, "~=", predicate.Values[0]}
	case model.PredicateOneOf:
		return []any{variable, "in", append([]string(nil), predicate.Values...)}
	default:
		return []any{variable, "==", ""}
	}
}

func apisixVariableName(name string) string {
	var builder strings.Builder
	for _, current := range strings.ToLower(name) {
		if unicode.IsLetter(current) || unicode.IsDigit(current) {
			builder.WriteRune(current)
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func apisixPlugins(attachments []model.PluginAttachment) (map[string]any, error) {
	rendered := make(map[string]any)
	for _, attachment := range attachments {
		if !attachment.Enabled {
			continue
		}
		config := make(map[string]any)
		if len(bytes.TrimSpace(attachment.RawConfig)) > 0 {
			if err := json.Unmarshal(attachment.RawConfig, &config); err != nil {
				return nil, fmt.Errorf("plugin %q config: %w", attachment.Name, err)
			}
		}
		switch attachment.Name {
		case "request-id":
			headerName, _ := config["header_name"].(string)
			if headerName == "" {
				headerName = "X-Request-ID"
			}
			rendered["request-id"] = map[string]any{
				"header_name":         headerName,
				"include_in_response": true,
				"algorithm":           "uuid",
			}
		case "header-rewrite":
			if request, ok := config["request"].(map[string]any); ok && len(request) > 0 {
				rendered["proxy-rewrite"] = map[string]any{"headers": request}
			}
			if response, ok := config["response"].(map[string]any); ok && len(response) > 0 {
				rendered["response-rewrite"] = map[string]any{"headers": response}
			}
		default:
			return nil, fmt.Errorf("unsupported plugin %q", attachment.Name)
		}
	}
	if len(rendered) == 0 {
		return nil, nil
	}
	return rendered, nil
}

func defaultGatewayRenderOptions(options GatewayRenderOptions) GatewayRenderOptions {
	if options.HTTPAddress == "" {
		options.HTTPAddress = ":8080"
	}
	if options.HTTPSAddress == "" {
		options.HTTPSAddress = ":8443"
	}
	if options.AdminAddress == "" {
		options.AdminAddress = ":9090"
	}
	if options.CertificateFile == "" {
		options.CertificateFile = "/certs/server.crt"
	}
	if options.PrivateKeyFile == "" {
		options.PrivateKeyFile = "/certs/server.key"
	}
	return options
}

func addMetadataComments(rendered []byte, metadata Metadata, endMarker bool) []byte {
	var output bytes.Buffer
	_, _ = fmt.Fprintf(&output, "# benchmark_schema_version: %d\n", metadata.SchemaVersion)
	_, _ = fmt.Fprintf(&output, "# benchmark_checksum: %s\n", metadata.Checksum)
	_, _ = fmt.Fprintf(&output, "# benchmark_route_count: %d\n", metadata.RouteCount)
	output.Write(rendered)
	if endMarker {
		if output.Len() > 0 && output.Bytes()[output.Len()-1] != '\n' {
			output.WriteByte('\n')
		}
		output.WriteString("#END\n")
	}
	return output.Bytes()
}
