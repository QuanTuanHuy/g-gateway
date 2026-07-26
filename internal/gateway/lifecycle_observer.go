package gateway

import (
	"log/slog"
	"time"

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

func (o *lifecycleObserver) RegistryPrepared(stats upstream.PrepareStats) {
	o.telemetry.RegistryPrepared(stats)
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

func (o *lifecycleObserver) RegistryRolledBack(stats upstream.PrepareStats) {
	o.telemetry.RegistryRolledBack(stats)
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

func (o *lifecycleObserver) RegistryRetired(stats upstream.RegistryStats) {
	o.telemetry.RegistryRetired(stats)
}

func (o *lifecycleObserver) RegistryCleaned(stats upstream.CleanupStats) {
	o.telemetry.RegistryCleaned(stats)
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

func (o *lifecycleObserver) RegistryError(code string, _ error) {
	o.logger.Error(
		"upstream_registry_error",
		"code", code,
	)
}

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
