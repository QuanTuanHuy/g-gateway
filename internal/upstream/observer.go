package upstream

type PrepareStats struct {
	CreatedEndpoints      int
	ReusedEndpoints       int
	CreatedTransports     int
	ReusedTransports      int
	CreatedSelections     int
	ReusedSelections      int
	WRRSlots              int
	HashPoints            int
	CreatedHealthTrackers int
	ReusedHealthTrackers  int
	CreatedRetryBudgets   int
	ReusedRetryBudgets    int
	Current               RegistryStats
}

type CleanupStats struct {
	ReleasedEndpoints      int
	ReleasedTransports     int
	ClosedTransports       int
	ReleasedHealthTrackers int
	ReleasedRetryBudgets   int
	Current                RegistryStats
}

type Observer interface {
	RegistryPrepared(PrepareStats)
	RegistryRolledBack(PrepareStats)
	RegistryRetired(RegistryStats)
	RegistryCleaned(CleanupStats)
	RegistryError(code string, err error)
}

type RegistryStats struct {
	LiveEndpoints       int
	LiveTransports      int
	LiveSelectionStates int
	LiveHealthTrackers  int
	LiveRetryBudgets    int
	ActivePlanSets      int
	RetiredPlanSets     int
}

type ResilienceStats struct {
	UpstreamID         string
	UnknownEndpoints   int
	HealthyEndpoints   int
	UnhealthyEndpoints int
	RetryInflight      uint32
	RetryBudgetTokens  float64
}
