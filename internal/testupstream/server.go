package testupstream

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxFixedBodyBytes = 64 * 1024

type server struct {
	logger        *slog.Logger
	requests      atomic.Int64
	cancellations atomic.Int64
	connectionsMu sync.Mutex
	connections   map[string]struct{}
	handler       http.Handler
}

type stateResponse struct {
	Requests      int64 `json:"requests"`
	Connections   int   `json:"connections"`
	Cancellations int64 `json:"cancellations"`
}

type headersResponse struct {
	Host       string      `json:"host"`
	Protocol   string      `json:"protocol"`
	RemoteAddr string      `json:"remote_addr"`
	Headers    http.Header `json:"headers"`
}

func New(logger *slog.Logger) http.Handler {
	state := &server{
		logger:      logger,
		connections: make(map[string]struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixed/{bytes}", state.fixed)
	mux.HandleFunc("POST /echo", state.echo)
	mux.HandleFunc("GET /headers", state.headers)
	mux.HandleFunc("GET /stream", state.stream)
	mux.HandleFunc("GET /delay/{duration}", state.delay)
	mux.HandleFunc("GET /status/{code}", state.status)
	mux.HandleFunc("GET /header-delay/{duration}", state.headerDelay)
	mux.HandleFunc("GET /stream-delay/{duration}", state.streamDelay)
	mux.HandleFunc("GET /trailers", state.trailers)
	mux.HandleFunc("GET /close", state.closeConnection)
	mux.HandleFunc("GET /debug/state", state.debugState)
	mux.HandleFunc("POST /debug/reset", state.reset)
	state.handler = mux
	return state
}

func (s *server) status(response http.ResponseWriter, request *http.Request) {
	code, err := strconv.Atoi(request.PathValue("code"))
	if err != nil || code < 100 || code > 599 {
		http.Error(response, "invalid status code", http.StatusBadRequest)
		return
	}
	response.WriteHeader(code)
}

func (s *server) headerDelay(response http.ResponseWriter, request *http.Request) {
	duration, ok := boundedDelay(response, request.PathValue("duration"))
	if !ok {
		return
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		response.WriteHeader(http.StatusNoContent)
	case <-request.Context().Done():
		s.cancellations.Add(1)
	}
}

func (s *server) streamDelay(response http.ResponseWriter, request *http.Request) {
	duration, ok := boundedDelay(response, request.PathValue("duration"))
	if !ok {
		return
	}
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(response, "first\n")
	if err := http.NewResponseController(response).Flush(); err != nil {
		return
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		_, _ = io.WriteString(response, "second\n")
	case <-request.Context().Done():
		s.cancellations.Add(1)
	}
}

func boundedDelay(response http.ResponseWriter, raw string) (time.Duration, bool) {
	duration, err := time.ParseDuration(raw)
	if err != nil || duration < 0 || duration > 30*time.Second {
		http.Error(response, "invalid delay", http.StatusBadRequest)
		return 0, false
	}
	return duration, true
}

func (s *server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if !strings.HasPrefix(request.URL.Path, "/debug/") {
		s.requests.Add(1)
		if request.RemoteAddr != "" {
			s.connectionsMu.Lock()
			s.connections[request.RemoteAddr] = struct{}{}
			s.connectionsMu.Unlock()
		}
	}
	s.handler.ServeHTTP(response, request)
}

func (s *server) fixed(response http.ResponseWriter, request *http.Request) {
	size, err := strconv.Atoi(request.PathValue("bytes"))
	if err != nil || size < 0 || size > maxFixedBodyBytes {
		http.Error(response, "invalid fixed body size", http.StatusBadRequest)
		return
	}
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("Content-Length", strconv.Itoa(size))
	_, _ = io.Copy(response, bytes.NewReader(bytes.Repeat([]byte{'x'}, size)))
}

func (s *server) echo(response http.ResponseWriter, request *http.Request) {
	controller := http.NewResponseController(response)
	_ = controller.EnableFullDuplex()
	response.Header().Set("Content-Type", "application/octet-stream")
	buffer := make([]byte, 32*1024)
	for {
		read, readErr := request.Body.Read(buffer)
		if read > 0 {
			if _, writeErr := response.Write(buffer[:read]); writeErr != nil {
				return
			}
			if flushErr := controller.Flush(); flushErr != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}

func (s *server) headers(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(headersResponse{
		Host:       request.Host,
		Protocol:   request.Proto,
		RemoteAddr: request.RemoteAddr,
		Headers:    request.Header.Clone(),
	})
}

func (s *server) stream(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(response, "first\n")
	if err := http.NewResponseController(response).Flush(); err != nil {
		s.logger.Error("flush stream", "error", err)
		return
	}
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		_, _ = io.WriteString(response, "second\n")
	case <-request.Context().Done():
		s.cancellations.Add(1)
	}
}

func (s *server) delay(response http.ResponseWriter, request *http.Request) {
	duration, err := time.ParseDuration(request.PathValue("duration"))
	if err != nil || duration < 0 || duration > 30*time.Second {
		http.Error(response, "invalid delay", http.StatusBadRequest)
		return
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		response.WriteHeader(http.StatusNoContent)
	case <-request.Context().Done():
		s.cancellations.Add(1)
	}
}

func (s *server) trailers(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Trailer", "X-Checksum")
	_, _ = io.WriteString(response, "body")
	response.Header().Set("X-Checksum", "abc123")
}

func (s *server) closeConnection(response http.ResponseWriter, _ *http.Request) {
	connection, buffered, err := http.NewResponseController(response).Hijack()
	if err != nil {
		http.Error(response, "hijack unavailable", http.StatusInternalServerError)
		return
	}
	if err := buffered.Flush(); err != nil {
		s.logger.Debug("flush before closing test connection", "error", err)
	}
	_ = connection.Close()
}

func (s *server) debugState(response http.ResponseWriter, _ *http.Request) {
	s.connectionsMu.Lock()
	connectionCount := len(s.connections)
	s.connectionsMu.Unlock()
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(stateResponse{
		Requests:      s.requests.Load(),
		Connections:   connectionCount,
		Cancellations: s.cancellations.Load(),
	})
}

func (s *server) reset(response http.ResponseWriter, _ *http.Request) {
	s.requests.Store(0)
	s.cancellations.Store(0)
	s.connectionsMu.Lock()
	s.connections = make(map[string]struct{})
	s.connectionsMu.Unlock()
	response.WriteHeader(http.StatusNoContent)
}
