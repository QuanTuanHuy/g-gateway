package gateway

import (
	"log/slog"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	gatewayruntime "github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/telemetry"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

type lifecycleObserver struct {
	telemetry *telemetry.Telemetry
	logger    *slog.Logger
}

func newLifecycleObserver(telemetryRuntime *telemetry.Telemetry, logger *slog.Logger) *lifecycleObserver {
	return &lifecycleObserver{
		telemetry: telemetryRuntime,
		logger:    logger,
	}
}

// SnapshotApplied forwards bounded snapshot gauges and logs the published
// revision, counts, and build duration.
func (o *lifecycleObserver) SnapshotApplied(stats gatewayruntime.Stats) {
	o.telemetry.SnapshotApplied(stats)
	o.logger.Info(
		"runtime_snapshot_applied",
		"revision", stats.Revision,
		"routes", stats.RouteCount,
		"services", stats.ServiceCount,
		"upstreams", stats.UpstreamCount,
		"plugins", stats.PluginCount,
		"duration_seconds", stats.BuildDuration.Seconds(),
	)
}

// SnapshotRejected forwards bounded rejection metrics and logs only stable
// error metadata plus build duration.
func (o *lifecycleObserver) SnapshotRejected(buildErr *gatewayruntime.BuildError, duration time.Duration) {
	o.telemetry.SnapshotRejected(buildErr, duration)
	var (
		revision uint64
		code     string
		stage    gatewayruntime.BuildStage
		kind     string
	)
	if buildErr != nil {
		revision = buildErr.Revision
		code = buildErr.Code
		stage = buildErr.Stage
		kind = buildErr.ResourceKind
	}
	o.logger.Warn(
		"runtime_snapshot_rejected",
		"revision", revision,
		"code", code,
		"stage", stage,
		"resource_kind", kind,
		"duration_seconds", duration.Seconds(),
	)
}

// RegistryPrepared forwards resource deltas and logs bounded registry and
// compilation counts.
func (o *lifecycleObserver) RegistryPrepared(stats upstream.PrepareStats) {
	o.telemetry.RegistryPrepared(stats)
	o.logTransportGenerations(stats.TransportGenerations)
	o.logger.Info(
		"upstream_registry_prepared",
		"created_endpoints", stats.CreatedEndpoints,
		"reused_endpoints", stats.ReusedEndpoints,
		"created_transports", stats.CreatedTransports,
		"reused_transports", stats.ReusedTransports,
		"created_selection_states", stats.CreatedSelections,
		"reused_selection_states", stats.ReusedSelections,
		"wrr_slots", stats.WRRSlots,
		"hash_points", stats.HashPoints,
		"live_endpoints", stats.Current.LiveEndpoints,
		"live_transports", stats.Current.LiveTransports,
		"live_selection_states", stats.Current.LiveSelectionStates,
		"active_plan_sets", stats.Current.ActivePlanSets,
		"retired_plan_sets", stats.Current.RetiredPlanSets,
	)
}

// RegistryRolledBack forwards the rollback and logs bounded post-cleanup
// counts.
func (o *lifecycleObserver) RegistryRolledBack(stats upstream.PrepareStats) {
	o.telemetry.RegistryRolledBack(stats)
	o.logTransportGenerations(stats.TransportGenerations)
	o.logger.Warn(
		"upstream_registry_rolled_back",
		"created_endpoints", stats.CreatedEndpoints,
		"created_transports", stats.CreatedTransports,
		"created_selection_states", stats.CreatedSelections,
		"live_endpoints", stats.Current.LiveEndpoints,
		"live_transports", stats.Current.LiveTransports,
		"live_selection_states", stats.Current.LiveSelectionStates,
		"active_plan_sets", stats.Current.ActivePlanSets,
		"retired_plan_sets", stats.Current.RetiredPlanSets,
	)
}

// RegistryRetired forwards the current registry gauges.
func (o *lifecycleObserver) RegistryRetired(stats upstream.RegistryStats) {
	o.telemetry.RegistryRetired(stats)
}

// RegistryCleaned forwards cleanup deltas and logs bounded cleanup and current
// registry counts.
func (o *lifecycleObserver) RegistryCleaned(stats upstream.CleanupStats) {
	o.telemetry.RegistryCleaned(stats)
	o.logTransportGenerations(stats.TransportGenerations)
	o.logger.Info(
		"upstream_registry_cleaned",
		"released_endpoints", stats.ReleasedEndpoints,
		"released_transports", stats.ReleasedTransports,
		"closed_transports", stats.ClosedTransports,
		"live_endpoints", stats.Current.LiveEndpoints,
		"live_transports", stats.Current.LiveTransports,
		"live_selection_states", stats.Current.LiveSelectionStates,
		"active_plan_sets", stats.Current.ActivePlanSets,
		"retired_plan_sets", stats.Current.RetiredPlanSets,
	)
}

// RegistryError logs the stable registry error code without raw error text.
func (o *lifecycleObserver) RegistryError(code string, _ error) {
	o.logger.Error(
		"upstream_registry_error",
		"code", code,
	)
}

// TLSHandshake forwards one bounded TLS handshake result and logs only closed
// dimensions.
func (o *lifecycleObserver) TLSHandshake(
	result, mode string,
	protocol model.TransportProtocol,
) {
	o.telemetry.TLSHandshake(result, mode, protocol)
	o.logger.Info(
		"upstream_tls_handshake",
		"result", result,
		"mode", mode,
		"protocol", protocol,
	)
}

// TLSFailure forwards one stable TLS failure class without raw error details.
func (o *lifecycleObserver) TLSFailure(class upstream.TLSFailureClass) {
	o.telemetry.TLSFailure(class)
	o.logger.Warn(
		"upstream_tls_failure",
		"class", class,
	)
}

func (o *lifecycleObserver) logTransportGenerations(
	deltas []upstream.TransportGenerationDelta,
) {
	for _, delta := range deltas {
		o.logger.Info(
			"upstream_transport_generation",
			"action", delta.Action,
			"tls", delta.TLS,
			"protocol", delta.Protocol,
			"count", delta.Count,
		)
	}
}

// ShutdownCleanup logs final bounded registry gauges after manager cleanup.
func (o *lifecycleObserver) ShutdownCleanup(stats upstream.RegistryStats) {
	o.logger.Info(
		"upstream_shutdown_cleanup",
		"live_endpoints", stats.LiveEndpoints,
		"live_transports", stats.LiveTransports,
		"live_selection_states", stats.LiveSelectionStates,
		"active_plan_sets", stats.ActivePlanSets,
		"retired_plan_sets", stats.RetiredPlanSets,
	)
}
