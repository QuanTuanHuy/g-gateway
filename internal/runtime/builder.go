package runtime

import (
	"fmt"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/router"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

type Builder struct {
	plugins     *plugin.Registry
	beforeBuild func(uint64)
}

func NewBuilder(plugins *plugin.Registry) (*Builder, error) {
	if plugins == nil {
		return nil, fmt.Errorf("plugin registry is required")
	}
	return &Builder{plugins: plugins}, nil
}

func (b *Builder) Build(revision uint64, input model.ResourceSet, candidate *upstream.Candidate) (*Snapshot, error) {
	if b.beforeBuild != nil {
		b.beforeBuild(revision)
	}
	resources := model.CloneResourceSet(input)
	if err := validateResources(revision, resources); err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, &BuildError{
			Code:     "UPSTREAM_CANDIDATE_MISSING",
			Stage:    StageResolve,
			Revision: revision,
			Field:    "upstreams",
			Cause:    fmt.Errorf("upstream candidate is required"),
		}
	}

	services := make(map[string]model.Service, len(resources.Services))
	for _, service := range resources.Services {
		services[service.ID] = service
	}
	routes := make([]CompiledRoute, 0, len(resources.Routes))
	specs := make([]router.RouteSpec, 0, len(resources.Routes))
	pluginCount := 0
	for _, route := range resources.Routes {
		var (
			serviceMeta    *requestctx.ServiceMeta
			servicePlugins []model.PluginAttachment
			upstreamID     string
		)
		if route.ServiceRef != "" {
			service := services[route.ServiceRef]
			serviceMeta = &requestctx.ServiceMeta{ID: service.ID}
			servicePlugins = service.Plugins
			upstreamID = service.UpstreamRef
		} else {
			upstreamID = route.UpstreamRef
		}
		upstreamPlan, ok := candidate.Plan(upstreamID)
		if !ok {
			return nil, &BuildError{
				Code:         "REFERENCE_NOT_FOUND",
				Stage:        StageResolve,
				Revision:     revision,
				ResourceKind: "route",
				ResourceID:   route.ID,
				Field:        "upstream_ref",
				Cause:        fmt.Errorf("upstream plan %q does not exist", upstreamID),
			}
		}
		chain, err := b.plugins.CompileChain(servicePlugins, route.Plugins)
		if err != nil {
			return nil, &BuildError{
				Code:         "PLUGIN_COMPILE_FAILED",
				Stage:        StagePlugin,
				Revision:     revision,
				ResourceKind: "route",
				ResourceID:   route.ID,
				Field:        "plugins",
				Cause:        err,
			}
		}
		pluginCount += len(chain.Names())
		routeIndex := len(routes)
		routes = append(routes, CompiledRoute{
			meta:         &requestctx.RouteMeta{ID: route.ID},
			service:      serviceMeta,
			upstreamMeta: &requestctx.UpstreamMeta{ID: upstreamID},
			plan:         upstreamPlan,
			plugins:      chain,
		})
		specs = append(specs, router.RouteSpec{
			Index:    routeIndex,
			ID:       route.ID,
			Priority: route.Priority,
			Match:    route.Match,
		})
	}

	compiledRouter, err := router.Compile(specs)
	if err != nil {
		return nil, &BuildError{
			Code:     "ROUTER_COMPILE_FAILED",
			Stage:    StageRouter,
			Revision: revision,
			Field:    "routes",
			Cause:    err,
		}
	}
	return &Snapshot{
		revision: revision,
		router:   compiledRouter,
		routes:   routes,
		stats: Stats{
			Revision:      revision,
			RouteCount:    len(routes),
			ServiceCount:  len(resources.Services),
			UpstreamCount: len(resources.Upstreams),
			PluginCount:   pluginCount,
		},
	}, nil
}
