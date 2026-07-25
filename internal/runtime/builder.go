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
	upstreams   *upstream.Table
	registry    *plugin.Registry
	beforeBuild func(uint64)
}

func NewBuilder(upstreams *upstream.Table, registry *plugin.Registry) (*Builder, error) {
	if upstreams == nil {
		return nil, fmt.Errorf("upstream table is required")
	}
	if registry == nil {
		return nil, fmt.Errorf("plugin registry is required")
	}
	return &Builder{upstreams: upstreams, registry: registry}, nil
}

func (b *Builder) Build(revision uint64, input model.ResourceSet) (*Snapshot, error) {
	if b.beforeBuild != nil {
		b.beforeBuild(revision)
	}
	resources := model.CloneResourceSet(input)
	if err := validateResources(revision, resources, b.upstreams); err != nil {
		return nil, err
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
		upstreamRuntime, ok := b.upstreams.Get(upstreamID)
		if !ok {
			return nil, &BuildError{
				Code:         "REFERENCE_NOT_FOUND",
				Stage:        StageResolve,
				Revision:     revision,
				ResourceKind: "route",
				ResourceID:   route.ID,
				Field:        "upstream_ref",
				Cause:        fmt.Errorf("upstream runtime %q does not exist", upstreamID),
			}
		}
		chain, err := b.registry.CompileChain(servicePlugins, route.Plugins)
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
			upstream:     upstreamRuntime,
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
