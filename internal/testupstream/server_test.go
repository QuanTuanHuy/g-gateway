package testupstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFixedBodiesAndLimits(t *testing.T) {
	handler := New(testLogger())
	for _, size := range []int{0, 1, 1024, 16384, 65536} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/fixed/"+intString(size), nil))
			if recorder.Code != http.StatusOK || recorder.Body.Len() != size {
				t.Fatalf("status=%d body bytes=%d, want 200 and %d", recorder.Code, recorder.Body.Len(), size)
			}
			if bytes.Count(recorder.Body.Bytes(), []byte{'x'}) != size {
				t.Fatal("fixed body contains bytes other than x")
			}
		})
	}
	for _, path := range []string{"/fixed/not-a-number", "/fixed/-1", "/fixed/65537"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status=%d, want 400", path, recorder.Code)
		}
	}
}

func TestEchoAndHeaders(t *testing.T) {
	handler := New(testLogger())
	echo := httptest.NewRecorder()
	handler.ServeHTTP(echo, httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("stream me")))
	if echo.Code != http.StatusOK || echo.Body.String() != "stream me" {
		t.Fatalf("echo response=%d %q", echo.Code, echo.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/headers", nil)
	request.Host = "original.example:8443"
	request.Header.Set("X-Test", "value")
	headers := httptest.NewRecorder()
	handler.ServeHTTP(headers, request)
	var body struct {
		Host       string      `json:"host"`
		Protocol   string      `json:"protocol"`
		RemoteAddr string      `json:"remote_addr"`
		Headers    http.Header `json:"headers"`
	}
	if err := json.Unmarshal(headers.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Host != "original.example:8443" || body.Protocol != "HTTP/1.1" || body.Headers.Get("X-Test") != "value" {
		t.Fatalf("headers response=%+v", body)
	}
}

func TestStreamFlushesBeforeCompletion(t *testing.T) {
	handler := New(testLogger())
	writer := newStreamWriter()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/stream", nil))
		close(done)
	}()

	select {
	case <-writer.flushed:
	case <-time.After(time.Second):
		t.Fatal("stream did not flush first chunk")
	}
	select {
	case <-done:
		t.Fatal("stream completed before first flush was observable")
	default:
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not complete")
	}
	if got := writer.bodyString(); got != "first\nsecond\n" {
		t.Fatalf("stream body=%q", got)
	}
}

func TestDelayCancellationIsObservable(t *testing.T) {
	server := httptest.NewServer(New(testLogger()))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/delay/10s", nil)
	done := make(chan error, 1)
	go func() {
		_, err := http.DefaultClient.Do(request)
		done <- err
	}()
	waitForState(t, server.URL, func(state debugState) bool { return state.Requests == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled request did not return")
	}
	waitForState(t, server.URL, func(state debugState) bool { return state.Cancellations == 1 })
}

func TestTrailers(t *testing.T) {
	server := httptest.NewServer(New(testLogger()))
	defer server.Close()
	response, err := http.Get(server.URL + "/trailers")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if got := response.Trailer.Get("X-Checksum"); got != "abc123" {
		t.Fatalf("X-Checksum trailer=%q", got)
	}
}

func TestCloseTerminatesConnection(t *testing.T) {
	server := httptest.NewServer(New(testLogger()))
	defer server.Close()
	response, err := http.Get(server.URL + "/close")
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("GET /close error=nil, want terminated connection")
	}
}

func TestDebugStateCountsConnectionsAndResets(t *testing.T) {
	server := httptest.NewServer(New(testLogger()))
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1}}
	defer client.CloseIdleConnections()
	for range 2 {
		response, err := client.Get(server.URL + "/fixed/0")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	state := getState(t, server.URL)
	if state.Requests != 2 || state.Connections != 1 {
		t.Fatalf("state=%+v, want 2 requests on 1 connection", state)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/debug/reset", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	state = getState(t, server.URL)
	if state != (debugState{}) {
		t.Fatalf("state after reset=%+v", state)
	}
}

type debugState struct {
	Requests      int64 `json:"requests"`
	Connections   int   `json:"connections"`
	Cancellations int64 `json:"cancellations"`
}

func getState(t *testing.T, baseURL string) debugState {
	t.Helper()
	response, err := http.Get(baseURL + "/debug/state")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var state debugState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func waitForState(t *testing.T, baseURL string, condition func(debugState) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition(getState(t, baseURL)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("debug state condition was not reached")
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func intString(value int) string {
	return strconv.Itoa(value)
}

type streamWriter struct {
	mu           sync.Mutex
	header       http.Header
	body         bytes.Buffer
	flushed      chan struct{}
	flushedFirst bool
}

func newStreamWriter() *streamWriter {
	return &streamWriter{header: make(http.Header), flushed: make(chan struct{})}
}

func (w *streamWriter) Header() http.Header { return w.header }
func (w *streamWriter) WriteHeader(int)     {}
func (w *streamWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(data)
}
func (w *streamWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.flushedFirst {
		w.flushedFirst = true
		close(w.flushed)
	}
}
func (w *streamWriter) bodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}
