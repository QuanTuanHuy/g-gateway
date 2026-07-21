package plugin

import (
	"github.com/valyala/fasthttp"
)

type RouteInfo struct {
	ID       string                 `json:"id"`
	URI      string                 `json:"uri"`
	Upstream string                 `json:"upstream"`
	Metadata map[string]interface{} `json:"metadata"`
}

type Plugin interface {
	Name() string
	PreHandle(ctx *fasthttp.RequestCtx, route *RouteInfo) (continueNext bool, err error)
	PostHandle(ctx *fasthttp.RequestCtx, route *RouteInfo) error
}
