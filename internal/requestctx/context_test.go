package requestctx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

func TestAttachCarriesOneTypedContext(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://gateway/users/42", nil)
	request, state := Attach(request, 2)
	state.Revision = 7
	state.Route = &RouteMeta{ID: "users"}
	state.Scratch[1] = "plugin-value"

	got, ok := From(request.Context())
	if !ok || got != state || got.Revision != 7 || got.Route.ID != "users" {
		t.Fatalf("From() = %+v, %v", got, ok)
	}
	if got.Scratch[1] != "plugin-value" || len(got.Scratch) != 2 {
		t.Fatalf("scratch = %#v", got.Scratch)
	}
}

func TestAttachIsolatesRequestStateAndScratch(t *testing.T) {
	firstRequest, first := Attach(httptest.NewRequest(http.MethodGet, "http://gateway/first", nil), 1)
	secondRequest, second := Attach(httptest.NewRequest(http.MethodGet, "http://gateway/second", nil), 1)

	first.Scratch[0] = "first"
	if first == second {
		t.Fatal("Attach() reused Context across requests")
	}
	if second.Scratch[0] != nil {
		t.Fatalf("second scratch aliases first: %#v", second.Scratch)
	}
	if firstRequest.Context() == secondRequest.Context() {
		t.Fatal("Attach() reused request context")
	}
}

func TestAttachPreservesTypedSnapshotAndRuntimeReferences(t *testing.T) {
	request, state := Attach(httptest.NewRequest(http.MethodGet, "http://gateway/users", nil), 0)
	snapshot := testSnapshot{revision: 12}
	runtime := &testRuntimeRoute{}
	state.Snapshot = snapshot
	state.Runtime = runtime

	got, ok := From(request.Context())
	if !ok {
		t.Fatal("From() did not find attached state")
	}
	if got.Snapshot.Revision() != 12 || got.Runtime != runtime {
		t.Fatalf("typed references = snapshot:%v runtime:%v", got.Snapshot, got.Runtime)
	}
}

func TestMiddlewareKeepsSameContextVisibleAroundNext(t *testing.T) {
	var before, during, after *Context
	leaf := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		during, _ = From(request.Context())
	})
	wrapper := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		before, _ = From(request.Context())
		leaf.ServeHTTP(response, request)
		after, _ = From(request.Context())
	})

	Middleware(wrapper).ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "http://gateway/users", nil),
	)

	if before == nil || before != during || during != after {
		t.Fatalf("context pointers before=%p during=%p after=%p", before, during, after)
	}
}

func TestAllocateScratchReplacesPreviousSlots(t *testing.T) {
	state := &Context{Scratch: []any{"old"}}
	state.AllocateScratch(2)

	if len(state.Scratch) != 2 || state.Scratch[0] != nil {
		t.Fatalf("scratch = %#v", state.Scratch)
	}
}

type testSnapshot struct {
	revision uint64
}

func (s testSnapshot) Revision() uint64 {
	return s.revision
}

type testRuntimeRoute struct{}

func (r *testRuntimeRoute) Select(*http.Request) (upstream.Selection, error) {
	return upstream.Selection{}, nil
}

func (r *testRuntimeRoute) RunResponse(*Context, *http.Response) error {
	return nil
}
