package web

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
)

const namespace = "cftunnelsync"

// daemonCollector collects metrics from the daemon's Snapshot.
type daemonCollector struct {
	daemon *reconcile.Daemon
	// Pre-registered descriptors
	descPassCount           *prometheus.Desc
	descLastDuration        *prometheus.Desc
	descLastError           *prometheus.Desc
	descLastPassTime        *prometheus.Desc
	descConsecutiveFailures *prometheus.Desc
	descDeferredPass        *prometheus.Desc
	descRulesActive         *prometheus.Desc
	descRulesPending        *prometheus.Desc
	descRulesExpired        *prometheus.Desc
	descRulesConflict       *prometheus.Desc
	descRulesAddedTotal     *prometheus.Desc
	descRulesUpdatedTotal   *prometheus.Desc
	descDNSCreatedTotal     *prometheus.Desc
	descDNSUpdatedTotal     *prometheus.Desc
	descIngressUpdatedTotal *prometheus.Desc
	descTunnelConnections   *prometheus.Desc
	descCFAPICalls          *prometheus.Desc
	descCFAPIErrors         *prometheus.Desc
	descCFAPICallsByRes     *prometheus.Desc
	descEventCount          *prometheus.Desc
	descTunnelID            *prometheus.Desc
}

func newDaemonCollector(daemon *reconcile.Daemon) *daemonCollector {
	return &daemonCollector{
		daemon: daemon,
		descPassCount: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "reconciler", "pass_count_total"),
			"Total number of reconciliation passes completed.",
			nil, nil,
		),
		descLastDuration: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "reconciler", "last_pass_duration_seconds"),
			"Duration of the last reconciliation pass in seconds.",
			nil, nil,
		),
		descLastError: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "reconciler", "last_error"),
			"Whether the last reconciliation pass had an error (1=yes, 0=no).",
			nil, nil,
		),
		descLastPassTime: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "reconciler", "last_pass_time_seconds"),
			"Unix timestamp of the last reconciliation pass.",
			nil, nil,
		),
		descConsecutiveFailures: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "reconciler", "consecutive_failures"),
			"Number of consecutive failed reconciliation passes.",
			nil, nil,
		),
		descDeferredPass: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "reconciler", "deferred_pass"),
			"Whether a reconciliation pass is currently deferred (1=yes, 0=no).",
			nil, nil,
		),
		descCFAPICalls: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "cf_api", "calls_total"),
			"Total Cloudflare API calls.",
			nil, nil,
		),
		descCFAPIErrors: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "cf_api", "errors_total"),
			"Total Cloudflare API errors.",
			nil, nil,
		),
		descCFAPICallsByRes: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "cf_api", "calls_by_resource_total"),
			"Cloudflare API calls by resource type.",
			[]string{"resource"}, nil,
		),
		descRulesActive: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "rules", "active"),
			"Number of active rules.",
			nil, nil,
		),
		descRulesPending: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "rules", "pending"),
			"Number of pending rules (within grace period).",
			nil, nil,
		),
		descRulesExpired: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "rules", "expired"),
			"Number of expired rules (grace period elapsed).",
			nil, nil,
		),
		descRulesConflict: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "rules", "conflict"),
			"Number of rules with DNS conflicts.",
			nil, nil,
		),
		descRulesAddedTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "rules", "added_total"),
			"Total number of rules added across all reconciliation passes.",
			nil, nil,
		),
		descRulesUpdatedTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "rules", "updated_total"),
			"Total number of rules updated across all reconciliation passes.",
			nil, nil,
		),
		descDNSCreatedTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "dns", "created_total"),
			"Total number of DNS records created across all reconciliation passes.",
			nil, nil,
		),
		descDNSUpdatedTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "dns", "updated_total"),
			"Total number of DNS records updated across all reconciliation passes.",
			nil, nil,
		),
		descIngressUpdatedTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "ingress", "updated_total"),
			"Total number of times the ingress configuration was updated.",
			nil, nil,
		),
		descTunnelConnections: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "tunnel", "connections"),
			"Number of active cloudflared connections to the tunnel.",
			nil, nil,
		),
		descEventCount: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "docker", "events_total"),
			"Total Docker events received per container and action.",
			[]string{"container", "action"}, nil,
		),
		descTunnelID: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "tunnel", "id"),
			"Current tunnel ID as a label-gauge.",
			[]string{"id", "name"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *daemonCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		c.descPassCount,
		c.descLastDuration,
		c.descLastError,
		c.descLastPassTime,
		c.descConsecutiveFailures,
		c.descDeferredPass,
		c.descRulesActive,
		c.descRulesPending,
		c.descRulesExpired,
		c.descRulesConflict,
		c.descRulesAddedTotal,
		c.descRulesUpdatedTotal,
		c.descDNSCreatedTotal,
		c.descDNSUpdatedTotal,
		c.descIngressUpdatedTotal,
		c.descTunnelConnections,
		c.descCFAPICalls,
		c.descCFAPIErrors,
		c.descCFAPICallsByRes,
		c.descEventCount,
		c.descTunnelID,
	} {
		ch <- desc
	}
}

// Collect implements prometheus.Collector.
func (c *daemonCollector) Collect(ch chan<- prometheus.Metric) {
	snap := c.daemon.Snapshot()

	ch <- prometheus.MustNewConstMetric(c.descPassCount, prometheus.CounterValue, float64(snap.PassCount))
	ch <- prometheus.MustNewConstMetric(c.descLastDuration, prometheus.GaugeValue, snap.LastDuration.Seconds())

	hasError := 0.0
	if snap.LastError != nil {
		hasError = 1.0
	}
	ch <- prometheus.MustNewConstMetric(c.descLastError, prometheus.GaugeValue, hasError)

	if !snap.LastPassTime.IsZero() {
		ch <- prometheus.MustNewConstMetric(c.descLastPassTime, prometheus.GaugeValue, float64(snap.LastPassTime.Unix()))
	}
	ch <- prometheus.MustNewConstMetric(c.descConsecutiveFailures, prometheus.GaugeValue, float64(snap.ConsecutiveFailures))

	deferred := 0.0
	if snap.DeferredPass {
		deferred = 1.0
	}
	ch <- prometheus.MustNewConstMetric(c.descDeferredPass, prometheus.GaugeValue, deferred)

	ch <- prometheus.MustNewConstMetric(c.descRulesActive, prometheus.GaugeValue, float64(snap.RulesActive))
	ch <- prometheus.MustNewConstMetric(c.descRulesPending, prometheus.GaugeValue, float64(snap.RulesPending))
	ch <- prometheus.MustNewConstMetric(c.descRulesExpired, prometheus.GaugeValue, float64(snap.RulesExpired))
	ch <- prometheus.MustNewConstMetric(c.descRulesConflict, prometheus.GaugeValue, float64(snap.RulesConflict))

	ch <- prometheus.MustNewConstMetric(c.descRulesAddedTotal, prometheus.CounterValue, float64(snap.RulesAddedTotal))
	ch <- prometheus.MustNewConstMetric(c.descRulesUpdatedTotal, prometheus.CounterValue, float64(snap.RulesUpdatedTotal))
	ch <- prometheus.MustNewConstMetric(c.descDNSCreatedTotal, prometheus.CounterValue, float64(snap.DNSCreatedTotal))
	ch <- prometheus.MustNewConstMetric(c.descDNSUpdatedTotal, prometheus.CounterValue, float64(snap.DNSUpdatedTotal))
	ch <- prometheus.MustNewConstMetric(c.descIngressUpdatedTotal, prometheus.CounterValue, float64(snap.IngressUpdatedTotal))

	ch <- prometheus.MustNewConstMetric(c.descTunnelConnections, prometheus.GaugeValue, float64(snap.Connections))

	ch <- prometheus.MustNewConstMetric(c.descCFAPICalls, prometheus.CounterValue, float64(snap.CFStats.Calls))
	ch <- prometheus.MustNewConstMetric(c.descCFAPIErrors, prometheus.CounterValue, float64(snap.CFStats.Errors))

	if snap.CFStats.ByResource != nil {
		for resource, count := range snap.CFStats.ByResource {
			ch <- prometheus.MustNewConstMetric(c.descCFAPICallsByRes, prometheus.CounterValue, float64(count), resource)
		}
	}

	// Emit Docker events per container and action
	if snap.EventCounts != nil {
		for container, actions := range snap.EventCounts {
			for action, count := range actions {
				ch <- prometheus.MustNewConstMetric(
					c.descEventCount, prometheus.CounterValue, float64(count), container, action,
				)
			}
		}
	}

	if snap.TunnelID != "" {
		ch <- prometheus.MustNewConstMetric(c.descTunnelID, prometheus.GaugeValue, 1, snap.TunnelID, snap.TunnelName)
	}
}

// newMetricsGatherer creates a prometheus.Gatherer that combines default Go/runtime
// metrics with daemon-specific metrics.
func newMetricsGatherer(daemon *reconcile.Daemon) prometheus.Gatherer {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewBuildInfoCollector(),
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		newDaemonCollector(daemon),
	)
	return reg
}
