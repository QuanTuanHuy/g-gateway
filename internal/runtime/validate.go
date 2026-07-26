package runtime

import (
	"fmt"
	"strings"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func validateResources(revision uint64, resources model.ResourceSet) *BuildError {
	if revision == 0 {
		return &BuildError{Code: "REVISION_INVALID", Stage: StageValidate, Cause: fmt.Errorf("revision must be greater than zero")}
	}
	if len(resources.Routes) == 0 {
		return &BuildError{Code: "ROUTES_EMPTY", Stage: StageValidate, Revision: revision, Cause: fmt.Errorf("at least one route is required")}
	}
	if err := validateResourceIDs(resources); err != nil {
		err.Revision = revision
		return err
	}
	upstreams := make(map[string]struct{}, len(resources.Upstreams))
	for _, resource := range resources.Upstreams {
		upstreams[resource.ID] = struct{}{}
	}
	services := make(map[string]model.Service, len(resources.Services))
	for _, service := range resources.Services {
		if _, ok := upstreams[service.UpstreamRef]; !ok {
			return &BuildError{
				Code:         "REFERENCE_NOT_FOUND",
				Stage:        StageResolve,
				Revision:     revision,
				ResourceKind: "service",
				ResourceID:   service.ID,
				Field:        "upstream_ref",
				Cause:        fmt.Errorf("upstream %q does not exist", service.UpstreamRef),
			}
		}
		if err := validatePluginScope(service.Plugins); err != nil {
			return &BuildError{
				Code:         "PLUGIN_SCOPE_INVALID",
				Stage:        StageValidate,
				Revision:     revision,
				ResourceKind: "service",
				ResourceID:   service.ID,
				Field:        "plugins",
				Cause:        err,
			}
		}
		services[service.ID] = service
	}
	for _, route := range resources.Routes {
		if (route.ServiceRef == "") == (route.UpstreamRef == "") {
			return &BuildError{
				Code:         "ROUTE_TARGET_INVALID",
				Stage:        StageValidate,
				Revision:     revision,
				ResourceKind: "route",
				ResourceID:   route.ID,
				Cause:        fmt.Errorf("exactly one of service_ref or upstream_ref must be set"),
			}
		}
		if route.ServiceRef != "" {
			if _, ok := services[route.ServiceRef]; !ok {
				return &BuildError{
					Code:         "REFERENCE_NOT_FOUND",
					Stage:        StageResolve,
					Revision:     revision,
					ResourceKind: "route",
					ResourceID:   route.ID,
					Field:        "service_ref",
					Cause:        fmt.Errorf("service %q does not exist", route.ServiceRef),
				}
			}
		}
		if route.UpstreamRef != "" {
			if _, ok := upstreams[route.UpstreamRef]; !ok {
				return &BuildError{
					Code:         "REFERENCE_NOT_FOUND",
					Stage:        StageResolve,
					Revision:     revision,
					ResourceKind: "route",
					ResourceID:   route.ID,
					Field:        "upstream_ref",
					Cause:        fmt.Errorf("upstream %q does not exist", route.UpstreamRef),
				}
			}
		}
		if err := validatePluginScope(route.Plugins); err != nil {
			return &BuildError{
				Code:         "PLUGIN_SCOPE_INVALID",
				Stage:        StageValidate,
				Revision:     revision,
				ResourceKind: "route",
				ResourceID:   route.ID,
				Field:        "plugins",
				Cause:        err,
			}
		}
	}
	return nil
}

func validateResourceIDs(resources model.ResourceSet) *BuildError {
	type resourceID struct {
		kind  string
		index int
		id    string
	}
	all := make([]resourceID, 0, len(resources.Routes)+len(resources.Services)+len(resources.Upstreams))
	for i, route := range resources.Routes {
		all = append(all, resourceID{kind: "route", index: i, id: route.ID})
	}
	for i, service := range resources.Services {
		all = append(all, resourceID{kind: "service", index: i, id: service.ID})
	}
	for i, resource := range resources.Upstreams {
		all = append(all, resourceID{kind: "upstream", index: i, id: resource.ID})
	}
	seen := make(map[string]map[string]struct{})
	for _, resource := range all {
		if strings.TrimSpace(resource.id) == "" || strings.TrimSpace(resource.id) != resource.id {
			return &BuildError{
				Code:         "RESOURCE_ID_INVALID",
				Stage:        StageValidate,
				ResourceKind: resource.kind,
				ResourceID:   resource.id,
				Field:        "id",
				Cause:        fmt.Errorf("ID at index %d must be non-empty without surrounding whitespace", resource.index),
			}
		}
		if seen[resource.kind] == nil {
			seen[resource.kind] = make(map[string]struct{})
		}
		if _, duplicate := seen[resource.kind][resource.id]; duplicate {
			return &BuildError{
				Code:         "RESOURCE_ID_DUPLICATE",
				Stage:        StageValidate,
				ResourceKind: resource.kind,
				ResourceID:   resource.id,
				Field:        "id",
				Cause:        fmt.Errorf("duplicate ID"),
			}
		}
		seen[resource.kind][resource.id] = struct{}{}
	}
	return nil
}

func validatePluginScope(plugins []model.PluginAttachment) error {
	seen := make(map[string]struct{}, len(plugins))
	for i, attachment := range plugins {
		if strings.TrimSpace(attachment.Name) == "" {
			return fmt.Errorf("plugin %d name must not be empty", i)
		}
		if _, duplicate := seen[attachment.Name]; duplicate {
			return fmt.Errorf("duplicate plugin name %q", attachment.Name)
		}
		seen[attachment.Name] = struct{}{}
	}
	return nil
}
