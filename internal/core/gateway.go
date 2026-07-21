package core

import (
	"sync/atomic"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/router"
)

type RouteTable struct {
	Version uint64
	Router  *router.Router
	Routes  map[string]*plugin.RouteInfo
}

func NewRouteTable(version uint64) *RouteTable {
	return &RouteTable{
		Version: version,
		Router:  router.NewRouter(),
		Routes:  make(map[string]*plugin.RouteInfo),
	}
}

func (rt *RouteTable) AddRoute(route *plugin.RouteInfo) error {
	rt.Routes[route.ID] = route
	return rt.Router.AddRoute(route)
}

type Gateway struct {
	routeTable atomic.Pointer[RouteTable]
}

func NewGateway() *Gateway {
	gw := &Gateway{}
	emptyTable := NewRouteTable(0)
	gw.routeTable.Store(emptyTable)
	return gw
}

func (g *Gateway) GetRouteTable() *RouteTable {
	return g.routeTable.Load()
}

func (g *Gateway) SwapRouteTable(newTable *RouteTable) {
	g.routeTable.Store(newTable)
}
