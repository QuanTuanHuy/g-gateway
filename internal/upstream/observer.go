package upstream

type PrepareStats struct {
	CreatedEndpoints  int
	ReusedEndpoints   int
	CreatedTransports int
	ReusedTransports  int
	CreatedSelections int
	ReusedSelections  int
	WRRSlots          int
	HashPoints        int
	Current           RegistryStats
}

type CleanupStats struct {
	ReleasedEndpoints  int
	ReleasedTransports int
	ClosedTransports   int
	Current            RegistryStats
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
	ActivePlanSets      int
	RetiredPlanSets     int
}
