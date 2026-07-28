package upstream

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"
)

type healthCoordinatorOptions struct {
	Workers       int
	QueueCapacity int
	HTTPProber    Prober
	TCPProber     Prober
}

type HealthCoordinatorStats struct {
	Workers     int
	Scheduled   int
	ReadyQueue  int
	Reschedules uint64
}

type HealthCoordinator struct {
	ctx         context.Context
	cancel      context.CancelFunc
	workers     int
	ready       chan *scheduledProbe
	wake        chan struct{}
	http        Prober
	tcp         Prober
	mu          sync.Mutex
	scheduled   scheduledProbeHeap
	sequence    uint64
	reschedules atomic.Uint64
	stopped     atomic.Bool
	closeOnce   sync.Once
	wg          sync.WaitGroup
}

type healthRegistration struct {
	coordinator *HealthCoordinator
	target      ProbeTarget
	health      *EndpointHealth
	activated   atomic.Bool
	retired     atomic.Bool
}

type scheduledProbe struct {
	due       time.Time
	sequence  uint64
	target    ProbeTarget
	health    *EndpointHealth
	reg       *healthRegistration
	cancelled atomic.Bool
	index     int
}

type scheduledProbeHeap []*scheduledProbe

func (h scheduledProbeHeap) Len() int { return len(h) }
func (h scheduledProbeHeap) Less(i, j int) bool {
	if !h[i].due.Equal(h[j].due) {
		return h[i].due.Before(h[j].due)
	}
	return h[i].sequence < h[j].sequence
}
func (h scheduledProbeHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *scheduledProbeHeap) Push(value any) {
	item := value.(*scheduledProbe)
	item.index = len(*h)
	*h = append(*h, item)
}
func (h *scheduledProbeHeap) Pop() any {
	old := *h
	last := len(old) - 1
	item := old[last]
	old[last] = nil
	item.index = -1
	*h = old[:last]
	return item
}

func newHealthCoordinator(options healthCoordinatorOptions) (*HealthCoordinator, error) {
	if options.Workers < 1 || options.Workers > 256 {
		return nil, fmt.Errorf("health workers must be between 1 and 256")
	}
	if options.QueueCapacity < 1 || options.QueueCapacity > 65536 {
		return nil, fmt.Errorf("health ready queue capacity must be between 1 and 65536")
	}
	if options.HTTPProber == nil {
		options.HTTPProber = newHTTPProber()
	}
	if options.TCPProber == nil {
		options.TCPProber = newTCPProber()
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := &HealthCoordinator{
		ctx:     ctx,
		cancel:  cancel,
		workers: options.Workers,
		ready:   make(chan *scheduledProbe, options.QueueCapacity),
		wake:    make(chan struct{}, 1),
		http:    options.HTTPProber,
		tcp:     options.TCPProber,
	}
	heap.Init(&coordinator.scheduled)
	coordinator.wg.Add(1 + options.Workers)
	go coordinator.runScheduler()
	for range options.Workers {
		go coordinator.runWorker()
	}
	return coordinator, nil
}

func (c *HealthCoordinator) Register(target ProbeTarget, health *EndpointHealth) *healthRegistration {
	return &healthRegistration{coordinator: c, target: target, health: health}
}

func (r *healthRegistration) ActivateHealth() {
	if r == nil || r.coordinator == nil || r.health == nil ||
		r.retired.Load() || r.coordinator.stopped.Load() ||
		!r.activated.CompareAndSwap(false, true) {
		return
	}
	r.coordinator.schedule(r, time.Now().Add(initialProbeJitter(r.target)))
}

func (r *healthRegistration) Retire() {
	if r == nil || !r.retired.CompareAndSwap(false, true) {
		return
	}
	if r.health != nil {
		r.health.Retire()
	}
	if r.coordinator != nil {
		r.coordinator.signalWake()
	}
}

func (c *HealthCoordinator) schedule(registration *healthRegistration, due time.Time) {
	if c.stopped.Load() || registration.retired.Load() {
		return
	}
	c.mu.Lock()
	c.sequence++
	heap.Push(&c.scheduled, &scheduledProbe{
		due:      due,
		sequence: c.sequence,
		target:   registration.target,
		health:   registration.health,
		reg:      registration,
	})
	c.mu.Unlock()
	c.signalWake()
}

func (c *HealthCoordinator) signalWake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *HealthCoordinator) runScheduler() {
	defer c.wg.Done()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	for {
		item, wait := c.nextScheduled()
		if item == nil {
			select {
			case <-c.ctx.Done():
				return
			case <-c.wake:
				continue
			}
		}
		if wait > 0 {
			timer.Reset(wait)
			select {
			case <-c.ctx.Done():
				return
			case <-c.wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				continue
			case <-timer.C:
			}
		}
		item = c.popDue()
		if item == nil || item.reg.retired.Load() {
			continue
		}
		select {
		case c.ready <- item:
		default:
			c.reschedules.Add(1)
			backoff := probeInterval(item.health) / 10
			if backoff <= 0 || backoff > 100*time.Millisecond {
				backoff = 100 * time.Millisecond
			}
			c.schedule(item.reg, time.Now().Add(backoff))
		}
	}
}

func (c *HealthCoordinator) nextScheduled() (*scheduledProbe, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.scheduled) > 0 && c.scheduled[0].reg.retired.Load() {
		heap.Pop(&c.scheduled)
	}
	if len(c.scheduled) == 0 {
		return nil, 0
	}
	item := c.scheduled[0]
	return item, time.Until(item.due)
}

func (c *HealthCoordinator) popDue() *scheduledProbe {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.scheduled) == 0 || time.Until(c.scheduled[0].due) > 0 {
		return nil
	}
	return heap.Pop(&c.scheduled).(*scheduledProbe)
}

func (c *HealthCoordinator) runWorker() {
	defer c.wg.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case item := <-c.ready:
			c.processProbe(item)
		}
	}
}

func (c *HealthCoordinator) processProbe(item *scheduledProbe) {
	defer func() {
		if recover() != nil && !c.stopped.Load() && !item.reg.retired.Load() {
			c.reschedules.Add(1)
			c.schedule(item.reg, time.Now().Add(probeInterval(item.health)))
		}
	}()
	if item == nil || item.reg.retired.Load() || c.stopped.Load() {
		return
	}
	var prober Prober
	switch item.target.Policy.Type {
	case "http":
		prober = c.http
	case "tcp":
		prober = c.tcp
	default:
		return
	}
	result := prober.Probe(c.ctx, item.target)
	if !item.reg.retired.Load() &&
		result.Target.Generation == item.health.Generation() {
		item.health.Observe(result.Observation)
	}
	if !c.stopped.Load() && !item.reg.retired.Load() {
		c.schedule(item.reg, time.Now().Add(probeInterval(item.health)))
	}
}

func probeInterval(health *EndpointHealth) time.Duration {
	if health == nil || health.policy.Active == nil {
		return time.Second
	}
	if health.State() == HealthUnhealthy {
		return health.policy.Active.UnhealthyInterval
	}
	return health.policy.Active.HealthyInterval
}

func initialProbeJitter(target ProbeTarget) time.Duration {
	interval := target.Policy.HealthyInterval
	if interval <= 0 {
		return 0
	}
	window := interval / 10
	if window <= 0 {
		return 0
	}
	digest := fnv.New64a()
	_, _ = digest.Write([]byte(target.EndpointID))
	return time.Duration(digest.Sum64() % uint64(window))
}

func (c *HealthCoordinator) Stats() HealthCoordinatorStats {
	c.mu.Lock()
	scheduled := len(c.scheduled)
	c.mu.Unlock()
	return HealthCoordinatorStats{
		Workers:     c.workers,
		Scheduled:   scheduled,
		ReadyQueue:  len(c.ready),
		Reschedules: c.reschedules.Load(),
	}
}

func (c *HealthCoordinator) StopHealth() {
	if c != nil && c.stopped.CompareAndSwap(false, true) {
		c.signalWake()
	}
}

func (c *HealthCoordinator) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.StopHealth()
		c.cancel()
		c.http.CloseIdleConnections()
		c.tcp.CloseIdleConnections()
	})
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Join(fmt.Errorf("close health coordinator"), ctx.Err())
	}
}
