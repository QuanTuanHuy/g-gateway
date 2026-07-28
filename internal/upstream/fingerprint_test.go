package upstream

import (
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestHealthFingerprintIgnoresWeightAndChangesWithPolicy(t *testing.T) {
	health, _ := validResiliencePolicies()
	first := makeHealthKey("users", "users\x00http://a:80", health)
	same := makeHealthKey("users", "users\x00http://a:80", health)
	if first != same {
		t.Fatal("same endpoint and policy produced different health keys")
	}

	changed := health
	active := *health.Active
	active.HealthyInterval += time.Second
	changed.Active = &active
	if first == makeHealthKey("users", "users\x00http://a:80", changed) {
		t.Fatal("health interval change did not change key")
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
