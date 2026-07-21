package router_test

import (
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/router"
)

func TestRadixRouterLookup(t *testing.T) {
	r := router.NewRouter()

	r1 := &plugin.RouteInfo{ID: "route-users", URI: "/api/v1/users", Upstream: "http://users-service"}
	r2 := &plugin.RouteInfo{ID: "route-orders", URI: "/api/v1/orders", Upstream: "http://orders-service"}

	err := r.AddRoute(r1)
	assert.NoError(t, err)
	err = r.AddRoute(r2)
	assert.NoError(t, err)

	matched, found := r.Match("/api/v1/users")
	assert.True(t, found)
	assert.Equal(t, "route-users", matched.ID)

	_, foundNotFound := r.Match("/api/v1/unknown")
	assert.False(t, foundNotFound)
}
