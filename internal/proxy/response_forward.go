package proxy

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

type responseStreamError struct{ err error }

func (e *responseStreamError) Error() string { return e.err.Error() }

func (e *responseStreamError) Unwrap() error { return e.err }

func (h *handler) forwardResponse(
	writer http.ResponseWriter,
	request *http.Request,
	state *requestctx.Context,
	response *http.Response,
) error {
	if response == nil || response.Body == nil {
		return fmt.Errorf("upstream response body is required")
	}
	defer response.Body.Close()
	if err := state.Runtime.RunResponse(state, response); err != nil {
		return &responsePluginError{err: err}
	}
	removeHopByHopHeaders(response.Header)
	for name, values := range response.Header {
		writer.Header()[name] = append([]string(nil), values...)
	}
	for name := range response.Trailer {
		writer.Header().Add("Trailer", name)
	}
	status := response.StatusCode
	if status < 100 {
		return fmt.Errorf("invalid upstream response status")
	}
	state.ResponseCode = status
	writer.WriteHeader(status)
	if _, err := io.Copy(writer, response.Body); err != nil {
		return &responseStreamError{err: err}
	}
	for name, values := range response.Trailer {
		writer.Header()[name] = append([]string(nil), values...)
	}
	_ = request
	return nil
}
