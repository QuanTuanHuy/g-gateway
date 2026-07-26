package gateway

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gatewayruntime "github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/telemetry"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

func TestLifecycleObserverForwardsMetricsAndLogsBoundedFields(t *testing.T) {
	telemetryRuntime, err := telemetry.New(false, false)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	observer := newLifecycleObserver(
		telemetryRuntime,
		slog.New(slog.NewJSONHandler(&logs, nil)),
	)

	observer.SnapshotApplied(gatewayruntime.Stats{
		Revision:      7,
		RouteCount:    11,
		ServiceCount:  3,
		UpstreamCount: 2,
		PluginCount:   5,
		BuildDuration: 25 * time.Millisecond,
	})
	observer.SnapshotRejected(&gatewayruntime.BuildError{
		Code:         "REFERENCE_NOT_FOUND",
		Stage:        gatewayruntime.StageResolve,
		Revision:     8,
		ResourceKind: "route",
		ResourceID:   "http://secret-host.example/private",
		Cause:        errors.New("client 203.0.113.99 hash abc123"),
	}, 10*time.Millisecond)
	observer.RegistryPrepared(upstream.PrepareStats{
		CreatedEndpoints:  2,
		CreatedTransports: 1,
		CreatedSelections: 1,
		Current: upstream.RegistryStats{
			LiveEndpoints:       2,
			LiveTransports:      1,
			LiveSelectionStates: 1,
			ActivePlanSets:      1,
		},
	})
	observer.RegistryRolledBack(upstream.PrepareStats{
		Current: upstream.RegistryStats{
			LiveEndpoints:  2,
			LiveTransports: 1,
		},
	})
	observer.RegistryRetired(upstream.RegistryStats{
		LiveEndpoints:       2,
		LiveTransports:      1,
		LiveSelectionStates: 1,
		ActivePlanSets:      1,
		RetiredPlanSets:     1,
	})
	observer.RegistryCleaned(upstream.CleanupStats{
		ReleasedEndpoints:  1,
		ReleasedTransports: 1,
		ClosedTransports:   1,
		Current:            upstream.RegistryStats{},
	})
	observer.RegistryError(
		"UPSTREAM_ENDPOINT_INVALID",
		errors.New("http://secret-host.example client=203.0.113.99 hash=abc123"),
	)
	observer.ShutdownCleanup(upstream.RegistryStats{})

	logBody := logs.String()
	for _, event := range []string{
		"runtime_snapshot_applied",
		"runtime_snapshot_rejected",
		"upstream_registry_prepared",
		"upstream_registry_rolled_back",
		"upstream_registry_cleaned",
		"upstream_registry_error",
		"upstream_shutdown_cleanup",
	} {
		if !strings.Contains(logBody, event) {
			t.Fatalf("logs do not contain event %q:\n%s", event, logBody)
		}
	}
	for _, fragment := range []string{
		`"revision":7`,
		`"code":"REFERENCE_NOT_FOUND"`,
		`"stage":"resolve"`,
		`"live_endpoints":2`,
	} {
		if !strings.Contains(logBody, fragment) {
			t.Fatalf("logs do not contain bounded field %q:\n%s", fragment, logBody)
		}
	}
	for _, forbidden := range []string{
		"secret-host.example",
		"203.0.113.99",
		"abc123",
	} {
		if strings.Contains(logBody, forbidden) {
			t.Fatalf("logs contain forbidden value %q:\n%s", forbidden, logBody)
		}
	}

	recorder := httptest.NewRecorder()
	telemetryRuntime.AdminHandler().ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "http://admin/metrics", nil),
	)
	metrics := recorder.Body.String()
	for _, fragment := range []string{
		`gateway_runtime_active_revision 7`,
		`gateway_upstream_registry_rollbacks_total 1`,
		`gateway_upstream_transport_cleanup_total 1`,
	} {
		if !strings.Contains(metrics, fragment) {
			t.Fatalf("metrics do not contain %q:\n%s", fragment, metrics)
		}
	}
}
