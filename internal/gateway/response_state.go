package gateway

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
)

type responseState struct {
	http.ResponseWriter
	committed bool
}

// WriteHeader marks the response committed on its first call and forwards the
// status code to the underlying writer.
func (w *responseState) WriteHeader(status int) {
	if !w.committed {
		w.committed = true
	}
	w.ResponseWriter.WriteHeader(status)
}

// Write commits an implicit 200 response before forwarding data.
func (w *responseState) Write(data []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

// Unwrap returns the underlying writer so http.ResponseController can recover
// supported optional response-writer interfaces.
func (w *responseState) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func recoverPanics(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		state := &responseState{ResponseWriter: response}
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(recovered)
			}
			logger.Error("panic serving downstream request", "panic", recovered, "stack", string(debug.Stack()))
			if state.committed {
				panic(http.ErrAbortHandler)
			}
			clear(state.Header())
			state.Header().Set("Content-Type", "application/json")
			state.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(state).Encode(struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}{Code: "INTERNAL_ERROR", Message: "internal server error"})
		}()
		next.ServeHTTP(state, request)
	})
}

func clear(header http.Header) {
	for name := range header {
		header.Del(name)
	}
}
