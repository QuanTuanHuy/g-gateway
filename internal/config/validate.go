package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	upstreamkernel "github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

const (
	apiVersionV1Alpha1 = "gateway/v1alpha1"
	apiVersionV1Alpha2 = "gateway/v1alpha2"
)

func validateV1(version string, bootstrap *BootstrapConfig, resources *model.ResourceSet) error {
	if version != apiVersionV1Alpha1 {
		return fmt.Errorf("api_version: got %q, want %q", version, apiVersionV1Alpha1)
	}
	if err := validateBootstrap(*bootstrap); err != nil {
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

func validateV2(version string, bootstrap *BootstrapConfig, resources *model.ResourceSet) error {
	if version != apiVersionV1Alpha2 {
		return fmt.Errorf("api_version: got %q, want %q", version, apiVersionV1Alpha2)
	}
	if err := validateBootstrap(*bootstrap); err != nil {
		return err
	}
	if err := validateResourceIDs(*resources); err != nil {
		return err
	}
	if len(resources.Routes) == 0 {
		return fmt.Errorf("routes: must contain at least one route")
	}
	if len(resources.Upstreams) == 0 {
		return fmt.Errorf("upstreams: must contain at least one upstream")
	}
	for i := range resources.Upstreams {
		if err := validateUpstream(&resources.Upstreams[i], i); err != nil {
			return err
		}
	}
	for i := range resources.Services {
		if err := validateService(&resources.Services[i], i, resources.Upstreams); err != nil {
			return err
		}
	}
	for i := range resources.Routes {
		if err := validateRouteV2(&resources.Routes[i], i, resources.Services, resources.Upstreams); err != nil {
			return err
		}
	}
	return nil
}

func validateBootstrap(bootstrap BootstrapConfig) error {
	if err := validateListeners(bootstrap); err != nil {
		return err
	}
	return validateServer(bootstrap.Server)
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

	serviceIDs := make(map[string]struct{}, len(resources.Services))
	for i := range resources.Services {
		id := strings.TrimSpace(resources.Services[i].ID)
		if id == "" {
			return fmt.Errorf("services[%d].id: must not be empty", i)
		}
		if _, ok := serviceIDs[id]; ok {
			return fmt.Errorf("duplicate service id %q", id)
		}
		serviceIDs[id] = struct{}{}
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

func validateRouteV2(route *model.Route, index int, services []model.Service, upstreams []model.Upstream) error {
	if err := validateRouteMatch(&route.Match, index); err != nil {
		return err
	}
	if (route.ServiceRef == "") == (route.UpstreamRef == "") {
		return fmt.Errorf("routes[%d]: exactly one of service_ref or upstream_ref must be set", index)
	}
	if route.ServiceRef != "" && !serviceExists(services, route.ServiceRef) {
		return fmt.Errorf("routes[%d].service_ref: %q does not resolve", index, route.ServiceRef)
	}
	if route.UpstreamRef != "" && !upstreamExists(upstreams, route.UpstreamRef) {
		return fmt.Errorf("routes[%d].upstream_ref: %q does not resolve", index, route.UpstreamRef)
	}
	if err := validatePredicates(route.Match.Headers, fmt.Sprintf("routes[%d].match.headers", index)); err != nil {
		return err
	}
	if err := validatePredicates(route.Match.Queries, fmt.Sprintf("routes[%d].match.queries", index)); err != nil {
		return err
	}
	return validatePluginAttachments(route.Plugins, fmt.Sprintf("routes[%d].plugins", index))
}

func validateService(service *model.Service, index int, upstreams []model.Upstream) error {
	if !upstreamExists(upstreams, service.UpstreamRef) {
		return fmt.Errorf("services[%d].upstream_ref: %q does not resolve", index, service.UpstreamRef)
	}
	return validatePluginAttachments(service.Plugins, fmt.Sprintf("services[%d].plugins", index))
}

func validateRouteMatch(match *model.RouteMatch, routeIndex int) error {
	if !strings.HasPrefix(match.Path, "/") {
		return fmt.Errorf("routes[%d].match.path: must be an absolute path", routeIndex)
	}
	if strings.ContainsAny(match.Path, "?#") {
		return fmt.Errorf("routes[%d].match.path: query and fragment are not allowed", routeIndex)
	}
	if len(match.Methods) == 0 {
		return fmt.Errorf("routes[%d].match.methods: must not be empty", routeIndex)
	}
	seenMethods := make(map[string]struct{}, len(match.Methods))
	for i, method := range match.Methods {
		if !validHTTPToken(method) {
			return fmt.Errorf("routes[%d].match.methods[%d]: invalid HTTP method %q", routeIndex, i, method)
		}
		method = strings.ToUpper(method)
		if _, ok := seenMethods[method]; ok {
			return fmt.Errorf("routes[%d].match.methods: duplicate method %q", routeIndex, method)
		}
		seenMethods[method] = struct{}{}
		match.Methods[i] = method
	}
	return nil
}

func validatePredicates(predicates []model.Predicate, field string) error {
	for i, predicate := range predicates {
		if strings.TrimSpace(predicate.Name) == "" {
			return fmt.Errorf("%s[%d].name: must not be empty", field, i)
		}
		switch predicate.Operator {
		case model.PredicateExists, model.PredicateNotExists:
			if len(predicate.Values) != 0 {
				return fmt.Errorf("%s[%d].values: operator %q requires no values", field, i, predicate.Operator)
			}
		case model.PredicateEquals, model.PredicateNotEquals:
			if len(predicate.Values) != 1 {
				return fmt.Errorf("%s[%d].values: operator %q requires exactly one value", field, i, predicate.Operator)
			}
		case model.PredicateOneOf:
			if len(predicate.Values) == 0 {
				return fmt.Errorf("%s[%d].values: operator %q requires at least one value", field, i, predicate.Operator)
			}
		default:
			return fmt.Errorf("%s[%d].operator: unsupported %q", field, i, predicate.Operator)
		}
	}
	return nil
}

func validatePluginAttachments(plugins []model.PluginAttachment, field string) error {
	seen := make(map[string]struct{}, len(plugins))
	for i, plugin := range plugins {
		if strings.TrimSpace(plugin.Name) == "" {
			return fmt.Errorf("%s[%d].name: must not be empty", field, i)
		}
		if _, ok := seen[plugin.Name]; ok {
			return fmt.Errorf("%s: duplicate plugin name %q", field, plugin.Name)
		}
		seen[plugin.Name] = struct{}{}
	}
	return nil
}

func serviceExists(services []model.Service, id string) bool {
	for _, service := range services {
		if service.ID == id {
			return true
		}
	}
	return false
}

func upstreamExists(upstreams []model.Upstream, id string) bool {
	for _, upstream := range upstreams {
		if upstream.ID == id {
			return true
		}
	}
	return false
}

func validateRoute(route *model.Route, index int, upstreams []model.Upstream) error {
	if err := validateRouteMatch(&route.Match, index); err != nil {
		return err
	}

	if !upstreamExists(upstreams, route.UpstreamRef) {
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
	if upstream.Endpoints[0].Weight != 1 {
		return fmt.Errorf("upstreams[%d].endpoints[0].weight: legacy configuration requires weight 1", index)
	}
	normalized, err := upstreamkernel.Normalize([]model.Upstream{*upstream})
	if err != nil {
		return err
	}
	*upstream = normalized[0]
	return nil
}
