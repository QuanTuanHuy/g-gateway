package router

import (
	"fmt"
	radix "github.com/armon/go-radix"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
)

type Router struct {
	tree *radix.Tree
}

func NewRouter() *Router {
	return &Router{
		tree: radix.New(),
	}
}

func (r *Router) AddRoute(route *plugin.RouteInfo) error {
	if route == nil || route.URI == "" {
		return fmt.Errorf("invalid route: URI cannot be empty")
	}
	r.tree.Insert(route.URI, route)
	return nil
}

func (r *Router) Match(path string) (*plugin.RouteInfo, bool) {
	val, found := r.tree.Get(path)
	if !found {
		return nil, false
	}
	route, ok := val.(*plugin.RouteInfo)
	return route, ok
}
