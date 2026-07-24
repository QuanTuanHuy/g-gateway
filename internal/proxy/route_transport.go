package proxy

import (
	"errors"
	"net/http"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

type routeTransport struct{}

func (routeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	state, ok := requestctx.From(request.Context())
	if !ok || state.Runtime == nil {
		return nil, errors.New("proxy request missing compiled runtime route")
	}
	return state.Runtime.RoundTrip(request)
}
