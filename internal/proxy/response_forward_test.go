package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

func TestForwardResponseCopiesStatusBodyAndRemovesHopHeaders(t *testing.T) {
	resources := webSocketResources("http://127.0.0.1:1", true)
	_, manager, _, _ := newWebSocketTestHandler(t, resources)
	lease, ok := manager.Acquire()
	if !ok {
		t.Fatal("manager has no snapshot")
	}
	defer lease.Release()
	request := validProxyWebSocketRequest()
	match, err := lease.Snapshot().Match(request)
	if err != nil || !match.Found {
		t.Fatalf("Match() = %+v, %v", match, err)
	}
	state := &requestctx.Context{Runtime: match.Route}
	response := &http.Response{
		StatusCode: http.StatusAccepted,
		Header: http.Header{
			"Connection": {"X-Remove"},
			"X-Remove":   {"gone"},
			"X-Keep":     {"kept"},
		},
		Body: io.NopCloser(strings.NewReader("streamed")),
	}
	recorder := httptest.NewRecorder()
	if err := (&handler{}).forwardResponse(recorder, request, state, response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusAccepted || recorder.Body.String() != "streamed" ||
		recorder.Header().Get("X-Keep") != "kept" || recorder.Header().Get("X-Remove") != "" ||
		state.ResponseCode != http.StatusAccepted {
		t.Fatalf("response=%d %#v %q state=%+v", recorder.Code, recorder.Header(), recorder.Body.String(), state)
	}
}
