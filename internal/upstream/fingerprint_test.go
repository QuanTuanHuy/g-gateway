package upstream

import (
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestHealthFingerprintIgnoresWeightAndChangesWithPolicy(t *testing.T) {
	health, _ := validResiliencePolicies()
	transport := makeTransportKey(transportTestProfile(t, "https", model.TransportProtocolHTTP2))
	first := makeHealthKey("users\x00http://a:80", health, transport)
	same := makeHealthKey("users\x00http://a:80", health, transport)
	if first != same {
		t.Fatal("same endpoint and policy produced different health keys")
	}

	changed := health
	active := *health.Active
	active.HealthyInterval += time.Second
	changed.Active = &active
	if first == makeHealthKey("users\x00http://a:80", changed, transport) {
		t.Fatal("health interval change did not change key")
	}
}

func TestHealthKeyIncludesCompleteTransportProfile(t *testing.T) {
	health, _ := validResiliencePolicies()
	base := makeTransportKey(transportTestProfile(t, "https", model.TransportProtocolHTTP2))
	first := makeHealthKey("users\x00https://a:443", health, base)
	tests := []struct {
		name   string
		change func(*transportKey)
	}{
		{name: "scheme", change: func(key *transportKey) { key.scheme = "http" }},
		{name: "protocol", change: func(key *transportKey) { key.protocol = model.TransportProtocolHTTP1 }},
		{name: "server name", change: func(key *transportKey) { key.serverName = "changed.internal" }},
		{name: "trust bundle", change: func(key *transportKey) { key.trustFingerprint[0]++ }},
		{name: "client certificate", change: func(key *transportKey) { key.clientFingerprint[0]++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.change(&changed)
			if first == makeHealthKey("users\x00https://a:443", health, changed) {
				t.Fatalf("%s missing from health key", test.name)
			}
		})
	}
	if first != makeHealthKey("users\x00https://a:443", health, base) {
		t.Fatal("unrelated weight, retry, or route policy should not affect health key")
	}
}

func TestRetryBudgetFingerprintTracksOnlyBudgetPolicy(t *testing.T) {
	policy := model.RetryBudgetPolicy{RatioPer1000: 100, Burst: 10, MaxInflight: 32}
	first := makeBudgetKey("users", policy)
	if first != makeBudgetKey("users", policy) {
		t.Fatal("same policy produced different budget keys")
	}
	policy.Burst++
	if first == makeBudgetKey("users", policy) {
		t.Fatal("budget change did not change key")
	}
	if first == makeBudgetKey("other", model.RetryBudgetPolicy{RatioPer1000: 100, Burst: 10, MaxInflight: 32}) {
		t.Fatal("upstream identity did not affect key")
	}
}
