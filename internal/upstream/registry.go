package upstream

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type endpointEntry struct {
	runtime *endpointRuntime
	refs    int
}

type transportEntry struct {
	runtime *transportRuntime
	refs    int
}

type selectionKey struct {
	upstreamID string
	algorithm  model.BalancerType
}

type selectionEntry struct {
	state *selectionState
	refs  int
}

type healthEntry struct {
	runtime      *EndpointHealth
	registration *healthRegistration
	refs         int
}

type budgetEntry struct {
	runtime *retryBudget
	refs    int
}

type resourceRefs struct {
	endpointIDs   []string
	transportKeys []transportKey
	selectionKeys []selectionKey
	healthKeys    []healthKey
	budgetKeys    []budgetKey
}

type RegistryOptions struct {
	MaxRetiredSnapshots int
	HealthWorkers       int
	HealthQueueCapacity int
	Observer            Observer
}

type Registry struct {
	mu                  sync.Mutex
	maxRetiredSnapshots int
	observer            Observer
	closed              bool
	endpoints           map[string]*endpointEntry
	transports          map[transportKey]*transportEntry
	selections          map[selectionKey]*selectionEntry
	health              map[healthKey]*healthEntry
	budgets             map[budgetKey]*budgetEntry
	coordinator         *HealthCoordinator
	generation          atomic.Uint64
	activePlanSets      int
	retired             []*PlanSet
	reapWake            chan struct{}
	reapStop            chan struct{}
	reapDone            chan struct{}
	stopReaperOnce      sync.Once
}

type Candidate struct {
	registry *Registry
	plans    map[string]*Plan
	owned    resourceRefs
	stats    PrepareStats
	done     atomic.Bool
}

func NewRegistry(options RegistryOptions) (*Registry, error) {
	if options.MaxRetiredSnapshots < 1 || options.MaxRetiredSnapshots > 1024 {
		return nil, fmt.Errorf("max retired snapshots must be between 1 and 1024")
	}
	coordinator, err := newHealthCoordinator(healthCoordinatorOptions{
		Workers:       options.HealthWorkers,
		QueueCapacity: options.HealthQueueCapacity,
	})
	if err != nil {
		return nil, err
	}
	registry := &Registry{
		maxRetiredSnapshots: options.MaxRetiredSnapshots,
		observer:            options.Observer,
		endpoints:           make(map[string]*endpointEntry),
		transports:          make(map[transportKey]*transportEntry),
		selections:          make(map[selectionKey]*selectionEntry),
		health:              make(map[healthKey]*healthEntry),
		budgets:             make(map[budgetKey]*budgetEntry),
		coordinator:         coordinator,
		reapWake:            make(chan struct{}, 1),
		reapStop:            make(chan struct{}),
		reapDone:            make(chan struct{}),
	}
	go registry.runReaper()
	return registry, nil
}

func (r *Registry) Prepare(resources []model.Upstream) (*Candidate, error) {
	if err := r.prepareAllowed(); err != nil {
		r.notifyError(err.Code, err)
		return nil, err
	}
	normalized, err := Normalize(resources)
	if err != nil {
		r.notifyError(configErrorCode(err), err)
		return nil, err
	}

	r.mu.Lock()
	if err := r.prepareErrorLocked(); err != nil {
		r.mu.Unlock()
		r.notifyError(err.Code, err)
		return nil, err
	}

	plans := make(map[string]*Plan, len(normalized))
	owned := resourceRefs{}
	stats := PrepareStats{}
	for upstreamIndex, resource := range normalized {
		plan, compileErr := r.preparePlanLocked(resource, &owned, &stats)
		if compileErr != nil {
			cleanup, transports := r.releaseRefsLocked(owned)
			stats.Current = cleanup.Current
			r.mu.Unlock()
			closeTransports(transports)
			r.notifyRolledBack(stats)
			r.notifyError(configErrorCode(compileErr), compileErr)
			return nil, compileErr
		}
		plans[resource.ID] = plan
		stats.WRRSlots += len(plan.wrr.schedule)
		stats.HashPoints += len(plan.continuum.hashes)
		if stats.WRRSlots > MaxSnapshotWRRSlots || stats.HashPoints > MaxSnapshotHashPoints {
			err := configError(
				"BALANCER_BUDGET_EXCEEDED",
				"upstreams",
				resource.ID,
				fmt.Errorf("balancer budget exceeded at upstream %d", upstreamIndex),
			)
			cleanup, transports := r.releaseRefsLocked(owned)
			stats.Current = cleanup.Current
			r.mu.Unlock()
			closeTransports(transports)
			r.notifyRolledBack(stats)
			r.notifyError(err.Code, err)
			return nil, err
		}
	}
	stats.Current = r.statsLocked()
	candidate := &Candidate{
		registry: r,
		plans:    plans,
		owned:    owned,
		stats:    stats,
	}
	r.mu.Unlock()
	r.notifyPrepared(stats)
	return candidate, nil
}

func (r *Registry) preparePlanLocked(resource model.Upstream, owned *resourceRefs, stats *PrepareStats) (*Plan, error) {
	transportKey := makeTransportKey(resource.Transport)
	transport := r.transports[transportKey]
	if transport == nil {
		transport = &transportEntry{runtime: newTransportRuntime(resource.Transport)}
		r.transports[transportKey] = transport
		stats.CreatedTransports++
	} else {
		stats.ReusedTransports++
	}
	transport.refs++
	owned.transportKeys = append(owned.transportKeys, transportKey)

	key := selectionKey{upstreamID: resource.ID, algorithm: resource.Balancer.Type}
	selection := r.selections[key]
	if selection == nil {
		selection = &selectionEntry{state: &selectionState{}}
		r.selections[key] = selection
		stats.CreatedSelections++
	} else {
		stats.ReusedSelections++
	}
	selection.refs++
	owned.selectionKeys = append(owned.selectionKeys, key)

	planEndpoints := make([]planEndpoint, 0, len(resource.Endpoints))
	for _, endpoint := range resource.Endpoints {
		identity := endpointIdentity(resource.ID, endpoint.URL)
		entry := r.endpoints[identity]
		if entry == nil {
			runtime, err := newEndpointRuntime(resource.ID, endpoint)
			if err != nil {
				return nil, err
			}
			entry = &endpointEntry{runtime: runtime}
			r.endpoints[identity] = entry
			stats.CreatedEndpoints++
		} else {
			stats.ReusedEndpoints++
		}
		entry.refs++
		owned.endpointIDs = append(owned.endpointIDs, identity)
		if endpoint.Weight > 0 {
			var health *EndpointHealth
			if resource.Health.Active != nil {
				key := makeHealthKey(resource.ID, identity, resource.Health)
				healthRuntime := r.health[key]
				if healthRuntime == nil {
					health = newEndpointHealth(identity, resource.Health, r.generation.Add(1))
					registration := r.coordinator.Register(ProbeTarget{
						EndpointID: identity,
						URL:        entry.runtime.target,
						Generation: health.Generation(),
						Policy:     *resource.Health.Active,
					}, health)
					healthRuntime = &healthEntry{runtime: health, registration: registration}
					r.health[key] = healthRuntime
					stats.CreatedHealthTrackers++
				} else {
					health = healthRuntime.runtime
					stats.ReusedHealthTrackers++
				}
				healthRuntime.refs++
				owned.healthKeys = append(owned.healthKeys, key)
			}
			planEndpoints = append(planEndpoints, planEndpoint{
				runtime:  entry.runtime,
				health:   health,
				identity: identity,
				weight:   endpoint.Weight,
			})
		}
	}

	var budget *retryBudget
	if resource.Retry.Budget.Burst > 0 && resource.Retry.Budget.MaxInflight > 0 {
		key := makeBudgetKey(resource.ID, resource.Retry.Budget)
		entry := r.budgets[key]
		if entry == nil {
			entry = &budgetEntry{runtime: newRetryBudget(resource.Retry.Budget)}
			r.budgets[key] = entry
			stats.CreatedRetryBudgets++
		} else {
			stats.ReusedRetryBudgets++
		}
		entry.refs++
		owned.budgetKeys = append(owned.budgetKeys, key)
		budget = entry.runtime
	}

	weighted := make([]weightedEndpoint, len(planEndpoints))
	for index, endpoint := range planEndpoints {
		weighted[index] = weightedEndpoint{
			identity: endpoint.identity,
			weight:   endpoint.weight,
		}
	}
	plan := &Plan{
		id:        resource.ID,
		algorithm: resource.Balancer.Type,
		endpoints: planEndpoints,
		transport: transport.runtime,
		budget:    budget,
	}
	for _, endpoint := range planEndpoints {
		if endpoint.health != nil {
			key := makeHealthKey(resource.ID, endpoint.identity, resource.Health)
			plan.healthRegistrations = append(plan.healthRegistrations, r.health[key].registration)
		}
	}
	switch resource.Balancer.Type {
	case model.BalancerWeightedRoundRobin:
		selector, err := compileWRR(weighted, selection.state)
		if err != nil {
			return nil, err
		}
		plan.wrr = selector
	case model.BalancerConsistentHash:
		extractor, err := compileHashKey(resource.Balancer.HashKey)
		if err != nil {
			return nil, err
		}
		compiled, err := compileContinuum(weighted)
		if err != nil {
			return nil, err
		}
		plan.hashKey = extractor
		plan.continuum = compiled
		plan.wrr.state = selection.state
	default:
		return nil, configError("BALANCER_TYPE_INVALID", "upstreams", resource.ID, nil)
	}
	return plan, nil
}

func (r *Registry) Stats() RegistryStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statsLocked()
}

func (r *Registry) statsLocked() RegistryStats {
	return RegistryStats{
		LiveEndpoints:       len(r.endpoints),
		LiveTransports:      len(r.transports),
		LiveSelectionStates: len(r.selections),
		LiveHealthTrackers:  len(r.health),
		LiveRetryBudgets:    len(r.budgets),
		ActivePlanSets:      r.activePlanSets,
		RetiredPlanSets:     len(r.retired),
	}
}

func (r *Registry) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.signalReaper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		r.reapNow()
		stats := r.Stats()
		if stats.LiveEndpoints == 0 &&
			stats.LiveTransports == 0 &&
			stats.LiveSelectionStates == 0 &&
			stats.LiveHealthTrackers == 0 &&
			stats.LiveRetryBudgets == 0 &&
			stats.ActivePlanSets == 0 &&
			stats.RetiredPlanSets == 0 {
			r.stopReaperOnce.Do(func() {
				close(r.reapStop)
			})
			select {
			case <-r.reapDone:
				return r.coordinator.Close(ctx)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Registry) prepareAllowed() *ConfigError {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prepareErrorLocked()
}

func (r *Registry) prepareErrorLocked() *ConfigError {
	if r.closed {
		return configError("REGISTRY_CLOSED", "upstreams", "", fmt.Errorf("registry is closed"))
	}
	if len(r.retired) >= r.maxRetiredSnapshots {
		return configError(
			"RETIRED_SNAPSHOT_LIMIT",
			"runtime.max_retired_snapshots",
			"",
			fmt.Errorf("retired plan sets reached %d", r.maxRetiredSnapshots),
		)
	}
	return nil
}

func (c *Candidate) Plan(id string) (*Plan, bool) {
	if c == nil {
		return nil, false
	}
	plan, ok := c.plans[id]
	return plan, ok
}

func (c *Candidate) Commit() *PlanSet {
	if c == nil || !c.done.CompareAndSwap(false, true) {
		return nil
	}
	set := &PlanSet{
		registry: c.registry,
		plans:    c.plans,
		owned:    c.owned,
	}
	set.refs.Store(1)
	c.registry.mu.Lock()
	c.registry.activePlanSets++
	c.registry.mu.Unlock()
	c.owned = resourceRefs{}
	return set
}

func (c *Candidate) Rollback() {
	if c == nil || !c.done.CompareAndSwap(false, true) {
		return
	}
	cleanup, transports := c.registry.releaseRefs(c.owned)
	c.stats.Current = cleanup.Current
	c.owned = resourceRefs{}
	closeTransports(transports)
	c.registry.notifyRolledBack(c.stats)
}

func (r *Registry) registerRetired(set *PlanSet) {
	r.mu.Lock()
	if r.activePlanSets <= 0 {
		r.mu.Unlock()
		panic("upstream registry active plan-set underflow")
	}
	r.activePlanSets--
	r.retired = append(r.retired, set)
	stats := r.statsLocked()
	r.mu.Unlock()
	r.notifyRetired(stats)
	r.signalReaper()
}

func (r *Registry) releaseRefs(owned resourceRefs) (CleanupStats, []*transportRuntime) {
	r.mu.Lock()
	cleanup, transports := r.releaseRefsLocked(owned)
	r.mu.Unlock()
	return cleanup, transports
}

func (r *Registry) releaseRefsLocked(owned resourceRefs) (CleanupStats, []*transportRuntime) {
	cleanup := CleanupStats{}
	for _, identity := range owned.endpointIDs {
		entry := r.endpoints[identity]
		if entry == nil || entry.refs <= 0 {
			panic("upstream endpoint reference underflow")
		}
		entry.refs--
		cleanup.ReleasedEndpoints++
		if entry.refs == 0 {
			delete(r.endpoints, identity)
		}
	}
	transports := make([]*transportRuntime, 0)
	for _, key := range owned.transportKeys {
		entry := r.transports[key]
		if entry == nil || entry.refs <= 0 {
			panic("upstream transport reference underflow")
		}
		entry.refs--
		cleanup.ReleasedTransports++
		if entry.refs == 0 {
			delete(r.transports, key)
			transports = append(transports, entry.runtime)
			cleanup.ClosedTransports++
		}
	}
	for _, key := range owned.selectionKeys {
		entry := r.selections[key]
		if entry == nil || entry.refs <= 0 {
			panic("upstream selection-state reference underflow")
		}
		entry.refs--
		if entry.refs == 0 {
			delete(r.selections, key)
		}
	}
	for _, key := range owned.healthKeys {
		entry := r.health[key]
		if entry == nil || entry.refs <= 0 {
			panic("upstream health reference underflow")
		}
		entry.refs--
		cleanup.ReleasedHealthTrackers++
		if entry.refs == 0 {
			delete(r.health, key)
			entry.registration.Retire()
		}
	}
	for _, key := range owned.budgetKeys {
		entry := r.budgets[key]
		if entry == nil || entry.refs <= 0 {
			panic("upstream retry budget reference underflow")
		}
		entry.refs--
		cleanup.ReleasedRetryBudgets++
		if entry.refs == 0 {
			delete(r.budgets, key)
		}
	}
	cleanup.Current = r.statsLocked()
	return cleanup, transports
}

func (r *Registry) StopHealth() {
	if r != nil && r.coordinator != nil {
		r.coordinator.StopHealth()
	}
}

func closeTransports(transports []*transportRuntime) {
	for _, transport := range transports {
		transport.CloseIdleConnections()
	}
}

func configErrorCode(err error) string {
	var configErr *ConfigError
	if errors.As(err, &configErr) {
		return configErr.Code
	}
	return "REGISTRY_PREPARE_FAILED"
}

func (r *Registry) notifyPrepared(stats PrepareStats) {
	r.observe(func(observer Observer) { observer.RegistryPrepared(stats) })
}

func (r *Registry) notifyRolledBack(stats PrepareStats) {
	r.observe(func(observer Observer) { observer.RegistryRolledBack(stats) })
}

func (r *Registry) notifyRetired(stats RegistryStats) {
	r.observe(func(observer Observer) { observer.RegistryRetired(stats) })
}

func (r *Registry) notifyCleaned(stats CleanupStats) {
	r.observe(func(observer Observer) { observer.RegistryCleaned(stats) })
}

func (r *Registry) notifyError(code string, err error) {
	r.observe(func(observer Observer) { observer.RegistryError(code, err) })
}

func (r *Registry) observe(call func(Observer)) {
	if r.observer == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	call(r.observer)
}
