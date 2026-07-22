package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

const apiVersion = "gateway/v1alpha1"

func validate(version string, bootstrap *BootstrapConfig, resources *model.ResourceSet) error {
	if version != apiVersion {
		return fmt.Errorf("api_version: got %q, want %q", version, apiVersion)
	}
	if err := validateListeners(*bootstrap); err != nil {
		return err
	}
	if err := validateServer(bootstrap.Server); err != nil {
		return err
	}
	if err := validateResourceIDs(*resources); err != nil {
		return err
	}
	if len(resources.Routes) != 1 {
		return fmt.Errorf("routes: Phase 1 requires exactly one route")
	}
	if len(resources.Upstreams) != 1 {
		return fmt.Errorf("upstreams: Phase 1 requires exactly one upstream")
	}
	if err := validateUpstream(&resources.Upstreams[0], 0); err != nil {
		return err
	}
	if err := validateRoute(&resources.Routes[0], 0, resources.Upstreams); err != nil {
		return err
	}
	return nil
}

func validateListeners(bootstrap BootstrapConfig) error {
	listeners := []struct {
		name    string
		address string
	}{
		{name: "listeners.http.address", address: bootstrap.HTTP.Address},
		{name: "listeners.https.address", address: bootstrap.HTTPS.Address},
		{name: "listeners.admin.address", address: bootstrap.Admin.Address},
	}

	seen := make(map[string]string, len(listeners))
	for _, listener := range listeners {
		key, err := normalizedListenAddress(listener.address)
		if err != nil {
			return fmt.Errorf("%s: %w", listener.name, err)
		}
		if previous, ok := seen[key]; ok {
			return fmt.Errorf("listener address %q conflicts between %s and %s", listener.address, previous, listener.name)
		}
		seen[key] = listener.name
	}

	if err := validateReadableRegularFile("listeners.https.certificate_file", bootstrap.HTTPS.CertificateFile); err != nil {
		return err
	}
	if err := validateReadableRegularFile("listeners.https.private_key_file", bootstrap.HTTPS.PrivateKeyFile); err != nil {
		return err
	}
	return nil
}

func normalizedListenAddress(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("address must not be empty")
	}
	address, err := net.ResolveTCPAddr("tcp", raw)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}
	host := "*"
	if address.IP != nil && !address.IP.IsUnspecified() {
		host = address.IP.String()
	}
	return net.JoinHostPort(host, strconv.Itoa(address.Port)), nil
}

func validateReadableRegularFile(field, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s: path must not be empty", field)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("%s: stat: %w", field, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: %q is not a regular file", field, path)
	}
	return nil
}

func validateServer(server ServerConfig) error {
	checks := []struct {
		field string
		valid bool
	}{
		{field: "server.read_header_timeout", valid: server.ReadHeaderTimeout > 0},
		{field: "server.idle_timeout", valid: server.IdleTimeout > 0},
		{field: "server.shutdown_timeout", valid: server.ShutdownTimeout > 0},
		{field: "server.max_header_bytes", valid: server.MaxHeaderBytes > 0},
		{field: "server.max_request_body_bytes", valid: server.MaxRequestBodyBytes > 0},
	}
	for _, check := range checks {
		if !check.valid {
			return fmt.Errorf("%s: must be greater than zero", check.field)
		}
	}
	return nil
}

func validateResourceIDs(resources model.ResourceSet) error {
	routeIDs := make(map[string]struct{}, len(resources.Routes))
	for i := range resources.Routes {
		id := strings.TrimSpace(resources.Routes[i].ID)
		if id == "" {
			return fmt.Errorf("routes[%d].id: must not be empty", i)
		}
		if _, ok := routeIDs[id]; ok {
			return fmt.Errorf("duplicate route id %q", id)
		}
		routeIDs[id] = struct{}{}
	}

	upstreamIDs := make(map[string]struct{}, len(resources.Upstreams))
	for i := range resources.Upstreams {
		id := strings.TrimSpace(resources.Upstreams[i].ID)
		if id == "" {
			return fmt.Errorf("upstreams[%d].id: must not be empty", i)
		}
		if _, ok := upstreamIDs[id]; ok {
			return fmt.Errorf("duplicate upstream id %q", id)
		}
		upstreamIDs[id] = struct{}{}
	}
	return nil
}

func validateRoute(route *model.Route, index int, upstreams []model.Upstream) error {
	if !strings.HasPrefix(route.Match.Path, "/") {
		return fmt.Errorf("routes[%d].match.path: must be an absolute path", index)
	}
	if strings.ContainsAny(route.Match.Path, "?#") {
		return fmt.Errorf("routes[%d].match.path: query and fragment are not allowed", index)
	}
	if len(route.Match.Methods) == 0 {
		return fmt.Errorf("routes[%d].match.methods: must not be empty", index)
	}
	seenMethods := make(map[string]struct{}, len(route.Match.Methods))
	for i, method := range route.Match.Methods {
		if !validHTTPToken(method) {
			return fmt.Errorf("routes[%d].match.methods[%d]: invalid HTTP method %q", index, i, method)
		}
		method = strings.ToUpper(method)
		if _, ok := seenMethods[method]; ok {
			return fmt.Errorf("routes[%d].match.methods: duplicate method %q", index, method)
		}
		seenMethods[method] = struct{}{}
		route.Match.Methods[i] = method
	}

	found := false
	for _, upstream := range upstreams {
		if upstream.ID == route.UpstreamRef {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("routes[%d].upstream_ref: %q does not resolve", index, route.UpstreamRef)
	}
	return nil
}

func validHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r > unicode.MaxASCII || unicode.IsControl(r) || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", r) {
			return false
		}
	}
	return true
}

func validateUpstream(upstream *model.Upstream, index int) error {
	if len(upstream.Endpoints) != 1 {
		return fmt.Errorf("upstreams[%d].endpoints: Phase 1 requires exactly one endpoint", index)
	}
	endpoint, err := url.Parse(upstream.Endpoints[0])
	if err != nil {
		return fmt.Errorf("upstreams[%d].endpoints[0]: %w", index, err)
	}
	if endpoint.Scheme != "http" {
		return fmt.Errorf("upstreams[%d].endpoints[0]: Phase 1 requires scheme http", index)
	}
	if endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return fmt.Errorf("upstreams[%d].endpoints[0]: must contain only an HTTP scheme and host", index)
	}
	transport := upstream.Transport
	checks := []struct {
		field string
		valid bool
	}{
		{field: "dial_timeout", valid: transport.DialTimeout > 0},
		{field: "response_header_timeout", valid: transport.ResponseHeaderTimeout > 0},
		{field: "idle_connection_timeout", valid: transport.IdleConnectionTimeout > 0},
		{field: "max_idle_connections", valid: transport.MaxIdleConnections > 0},
		{field: "max_idle_connections_per_host", valid: transport.MaxIdleConnectionsPerHost > 0},
	}
	for _, check := range checks {
		if !check.valid {
			return fmt.Errorf("upstreams[%d].transport.%s: must be greater than zero", index, check.field)
		}
	}
	return nil
}
