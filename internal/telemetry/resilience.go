package telemetry

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

// ResilienceStatsProvider supplies point-in-time upstream and scheduler
// gauges to the Prometheus resilience collector.
type ResilienceStatsProvider interface {
	// ResilienceStats returns per-upstream health and retry gauges.
	ResilienceStats() []upstream.ResilienceStats
	// HealthCoordinatorStats returns health-scheduler gauges and counters.
	HealthCoordinatorStats() upstream.HealthCoordinatorStats
}

type resilienceCollector struct {
	provider ResilienceStatsProvider

	healthEndpoints      *prometheus.Desc
	healthTransitions    *prometheus.Desc
	healthProbes         *prometheus.Desc
	healthProbeDuration  *prometheus.Desc
	schedulerQueue       *prometheus.Desc
	schedulerReschedules *prometheus.Desc
	attempts             *prometheus.Desc
	retries              *prometheus.Desc
	retrySuppressed      *prometheus.Desc
	retryInflight        *prometheus.Desc
	retryBudgetTokens    *prometheus.Desc
}

func newResilienceCollector(provider ResilienceStatsProvider) *resilienceCollector {
	return &resilienceCollector{
		provider: provider,
		healthEndpoints: prometheus.NewDesc(
			"gateway_upstream_health_endpoints",
			"Current endpoint count by upstream and public health state.",
			[]string{"upstream_id", "state"}, nil,
		),
		healthTransitions: prometheus.NewDesc(
			"gateway_upstream_health_transitions_total",
			"Total endpoint health state transitions.",
			[]string{"source", "to_state"}, nil,
		),
		healthProbes: prometheus.NewDesc(
			"gateway_upstream_health_probes_total",
			"Total active health probes.",
			[]string{"type", "outcome"}, nil,
		),
		healthProbeDuration: prometheus.NewDesc(
			"gateway_upstream_health_probe_duration_seconds",
			"Active health probe duration.",
			[]string{"type"}, nil,
		),
		schedulerQueue: prometheus.NewDesc(
			"gateway_upstream_health_scheduler_queue",
			"Current health scheduler ready queue depth.",
			nil, nil,
		),
		schedulerReschedules: prometheus.NewDesc(
			"gateway_upstream_health_scheduler_reschedules_total",
			"Total health scheduler reschedules.",
			[]string{"reason"}, nil,
		),
		attempts: prometheus.NewDesc(
			"gateway_upstream_attempts_total",
			"Total upstream attempts.",
			[]string{"upstream_id", "outcome"}, nil,
		),
		retries: prometheus.NewDesc(
			"gateway_upstream_retries_total",
			"Total upstream retries.",
			[]string{"upstream_id", "result"}, nil,
		),
		retrySuppressed: prometheus.NewDesc(
			"gateway_upstream_retry_suppressed_total",
			"Total suppressed retries.",
			[]string{"reason"}, nil,
		),
		retryInflight: prometheus.NewDesc(
			"gateway_upstream_retry_inflight",
			"Current inflight retries by upstream.",
			[]string{"upstream_id"}, nil,
		),
		retryBudgetTokens: prometheus.NewDesc(
			"gateway_upstream_retry_budget_tokens",
			"Current retry budget tokens by upstream.",
			[]string{"upstream_id"}, nil,
		),
	}
}

// Describe sends the collector's fixed Prometheus descriptors to out.
func (c *resilienceCollector) Describe(out chan<- *prometheus.Desc) {
	for _, description := range []*prometheus.Desc{
		c.healthEndpoints, c.healthTransitions, c.healthProbes,
		c.healthProbeDuration, c.schedulerQueue, c.schedulerReschedules,
		c.attempts, c.retries, c.retrySuppressed, c.retryInflight,
		c.retryBudgetTokens,
	} {
		out <- description
	}
}

// Collect reads one point-in-time provider snapshot and sends metrics to out.
// An empty provider emits a bounded "__none__" upstream series.
func (c *resilienceCollector) Collect(out chan<- prometheus.Metric) {
	stats := c.provider.ResilienceStats()
	if len(stats) == 0 {
		stats = []upstream.ResilienceStats{{UpstreamID: "__none__"}}
	}
	for _, current := range stats {
		out <- prometheus.MustNewConstMetric(c.healthEndpoints, prometheus.GaugeValue, float64(current.UnknownEndpoints), current.UpstreamID, "unknown")
		out <- prometheus.MustNewConstMetric(c.healthEndpoints, prometheus.GaugeValue, float64(current.HealthyEndpoints), current.UpstreamID, "healthy")
		out <- prometheus.MustNewConstMetric(c.healthEndpoints, prometheus.GaugeValue, float64(current.UnhealthyEndpoints), current.UpstreamID, "unhealthy")
		out <- prometheus.MustNewConstMetric(c.attempts, prometheus.CounterValue, 0, current.UpstreamID, "success")
		out <- prometheus.MustNewConstMetric(c.retries, prometheus.CounterValue, 0, current.UpstreamID, "attempted")
		out <- prometheus.MustNewConstMetric(c.retryInflight, prometheus.GaugeValue, float64(current.RetryInflight), current.UpstreamID)
		out <- prometheus.MustNewConstMetric(c.retryBudgetTokens, prometheus.GaugeValue, current.RetryBudgetTokens, current.UpstreamID)
	}
	coordinator := c.provider.HealthCoordinatorStats()
	out <- prometheus.MustNewConstMetric(c.healthTransitions, prometheus.CounterValue, 0, "active", "unknown")
	out <- prometheus.MustNewConstMetric(c.healthProbes, prometheus.CounterValue, 0, "http", "success")
	out <- prometheus.MustNewConstHistogram(c.healthProbeDuration, 0, 0, nil, "http")
	out <- prometheus.MustNewConstMetric(c.schedulerQueue, prometheus.GaugeValue, float64(coordinator.ReadyQueue))
	out <- prometheus.MustNewConstMetric(c.schedulerReschedules, prometheus.CounterValue, float64(coordinator.Reschedules), "queue_full")
	out <- prometheus.MustNewConstMetric(c.retrySuppressed, prometheus.CounterValue, 0, "none")
}

// RegisterResilienceProvider registers exactly one resilience collector backed
// by provider. It rejects nil inputs and returns an error if an equivalent
// collector has already been registered.
func (t *Telemetry) RegisterResilienceProvider(provider ResilienceStatsProvider) error {
	if t == nil || provider == nil {
		return fmt.Errorf("resilience stats provider is required")
	}
	if err := t.registry.Register(newResilienceCollector(provider)); err != nil {
		return fmt.Errorf("register resilience collector: %w", err)
	}
	return nil
}
