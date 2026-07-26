package upstream

import (
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestTransportKeyIncludesEveryPoolSemantic(t *testing.T) {
	base := validTransportConfig()
	key := makeTransportKey(base)
	if !key.disableCompression || !key.http1Only {
		t.Fatalf("fixed transport semantics = %+v", key)
	}

	tests := []struct {
		name   string
		change func(*model.TransportConfig)
	}{
		{name: "dial timeout", change: func(config *model.TransportConfig) { config.DialTimeout += time.Nanosecond }},
		{name: "response header timeout", change: func(config *model.TransportConfig) { config.ResponseHeaderTimeout += time.Nanosecond }},
		{name: "idle connection timeout", change: func(config *model.TransportConfig) { config.IdleConnectionTimeout += time.Nanosecond }},
		{name: "max idle connections", change: func(config *model.TransportConfig) { config.MaxIdleConnections++ }},
		{name: "max idle per host", change: func(config *model.TransportConfig) { config.MaxIdleConnectionsPerHost++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.change(&changed)
			if key == makeTransportKey(changed) {
				t.Fatalf("%s missing from transport key", test.name)
			}
		})
	}
	if key != makeTransportKey(base) {
		t.Fatal("identical complete profiles produced different keys")
	}
}

func TestTransportRuntimeCloseIdleConnectionsIsIdempotent(t *testing.T) {
	runtime := newTransportRuntime(validTransportConfig())
	calls := 0
	runtime.closeIdleConnections = func() {
		calls++
	}
	runtime.CloseIdleConnections()
	runtime.CloseIdleConnections()
	if calls != 1 {
		t.Fatalf("close calls = %d, want 1", calls)
	}
}
