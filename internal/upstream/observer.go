package upstream

import "github.com/QuanTuanHuy/g-gateway/internal/model"

// TransportGenerationDelta reports one compacted transport-generation
// lifecycle count using only closed action, TLS, and protocol dimensions.
type TransportGenerationDelta struct {
	// Action is create, reuse, or retire.
	Action string
	// TLS reports whether the transport uses upstream TLS.
	TLS bool
	// Protocol is the configured auto, HTTP/1, or HTTP/2 policy.
	Protocol model.TransportProtocol
	// Count is the positive number of matching generation events.
	Count int
}

// PrepareStats reports resource deltas for one candidate preparation plus the
// current registry gauges after preparation.
type PrepareStats struct {
	// CreatedEndpoints is the number of endpoint runtimes created.
	CreatedEndpoints int
	// ReusedEndpoints is the number of endpoint runtimes reused.
	ReusedEndpoints int
	// CreatedTransports is the number of transport runtimes created.
	CreatedTransports int
	// ReusedTransports is the number of transport runtimes reused.
	ReusedTransports int
	// CreatedSelections is the number of balancer selection states created.
	CreatedSelections int
	// ReusedSelections is the number of balancer selection states reused.
	ReusedSelections int
	// WRRSlots is the aggregate weighted round-robin schedule size prepared.
	WRRSlots int
	// HashPoints is the aggregate consistent-hash continuum size prepared.
	HashPoints int
	// CreatedHealthTrackers is the number of endpoint health runtimes created.
	CreatedHealthTrackers int
	// ReusedHealthTrackers is the number of endpoint health runtimes reused.
	ReusedHealthTrackers int
	// CreatedRetryBudgets is the number of retry budgets created.
	CreatedRetryBudgets int
	// ReusedRetryBudgets is the number of retry budgets reused.
	ReusedRetryBudgets int
	// TransportGenerations contains compacted create and reuse deltas.
	TransportGenerations []TransportGenerationDelta
	// Current contains registry gauges after the preparation transaction.
	Current RegistryStats
}

// CleanupStats reports resource-release deltas for one cleanup transaction
// plus the current registry gauges after cleanup.
type CleanupStats struct {
	// ReleasedEndpoints is the number of endpoint references released.
	ReleasedEndpoints int
	// ReleasedTransports is the number of transport references released.
	ReleasedTransports int
	// ClosedTransports is the number of transport runtimes whose idle
	// connections were closed.
	ClosedTransports int
	// ReleasedHealthTrackers is the number of health-runtime references
	// released.
	ReleasedHealthTrackers int
	// ReleasedRetryBudgets is the number of retry-budget references released.
	ReleasedRetryBudgets int
	// TransportGenerations contains compacted retire deltas.
	TransportGenerations []TransportGenerationDelta
	// Current contains registry gauges after cleanup.
	Current RegistryStats
}

// Observer receives synchronous, bounded registry lifecycle events. Callbacks
// must return promptly; the registry isolates callback panics from its own
// lifecycle operations.
type Observer interface {
	// RegistryPrepared reports a successfully prepared candidate.
	RegistryPrepared(PrepareStats)
	// RegistryRolledBack reports resources released by candidate rollback.
	RegistryRolledBack(PrepareStats)
	// RegistryRetired reports gauges after an active plan set is retired.
	RegistryRetired(RegistryStats)
	// RegistryCleaned reports final asynchronous resource cleanup.
	RegistryCleaned(CleanupStats)
	// RegistryError reports a stable bounded error code and its cause.
	RegistryError(code string, err error)
	// TLSHandshake reports one terminal TLS handshake result.
	TLSHandshake(result, mode string, protocol model.TransportProtocol)
	// TLSFailure reports one stable typed upstream TLS failure class.
	TLSFailure(class TLSFailureClass)
}

// RegistryStats contains current resource and plan-set gauges.
type RegistryStats struct {
	// LiveEndpoints is the current endpoint-runtime count.
	LiveEndpoints int
	// LiveTransports is the current transport-runtime count.
	LiveTransports int
	// LiveSelectionStates is the current shared balancer-state count.
	LiveSelectionStates int
	// LiveHealthTrackers is the current endpoint-health-runtime count.
	LiveHealthTrackers int
	// LiveRetryBudgets is the current upstream retry-budget count.
	LiveRetryBudgets int
	// ActivePlanSets is the current committed, non-retired plan-set count.
	ActivePlanSets int
	// RetiredPlanSets is the current plan-set count awaiting final release or
	// cleanup.
	RetiredPlanSets int
}

// ResilienceStats contains current bounded health and retry gauges for one
// upstream.
type ResilienceStats struct {
	// UpstreamID identifies the configuration-bounded upstream.
	UpstreamID string
	// UnknownEndpoints is the current number of unknown endpoints.
	UnknownEndpoints int
	// HealthyEndpoints is the current number of healthy endpoints.
	HealthyEndpoints int
	// UnhealthyEndpoints is the current number of unhealthy endpoints.
	UnhealthyEndpoints int
	// RetryInflight is the current number of acquired retry permits.
	RetryInflight uint32
	// RetryBudgetTokens is the current fixed-point credit balance expressed as
	// whole retry tokens.
	RetryBudgetTokens float64
}
