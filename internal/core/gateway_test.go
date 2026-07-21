package core_test

import (
	"sync"
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/QuanTuanHuy/g-gateway/internal/core"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
)

func TestAtomicRouteTableSwap(t *testing.T) {
	gw := core.NewGateway()

	t1 := core.NewRouteTable(1)
	_ = t1.AddRoute(&plugin.RouteInfo{ID: "r1", URI: "/v1"})
	gw.SwapRouteTable(t1)

	assert.Equal(t, uint64(1), gw.GetRouteTable().Version)

	// Concurrent read during swap
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rt := gw.GetRouteTable()
			assert.NotNil(t, rt)
		}()
	}

	t2 := core.NewRouteTable(2)
	_ = t2.AddRoute(&plugin.RouteInfo{ID: "r2", URI: "/v2"})
	gw.SwapRouteTable(t2)

	wg.Wait()
	assert.Equal(t, uint64(2), gw.GetRouteTable().Version)
}
