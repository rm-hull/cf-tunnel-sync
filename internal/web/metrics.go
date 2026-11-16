package web

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
)

// daemonCollector collects metrics from the daemon's Snapshot.
type daemonCollector struct {
	daemon *reconcile.Daemon
	// Pre-registered descriptors
	descPassCount       *prometheus.Desc
	descLastDuration    *prometheus.Desc
	descLastError       *prometheus.Desc
	descCFAPICalls      *prometheus.Desc
	descCFAPIErrors     *prometheus.Desc
	descCFAPICallsByRes *prometheus.Desc
	descEventCount      *prometheus.Desc
	descTunnelID        *prometheus.Desc
}

func newDaemonCollector(daemon *reconcile.Daemon) *daemonCollector {
	subsystem := "tunnel"
	return &daemonCollector{
		daemon: daemon,
		descPassCount: prometheus.NewDesc(
			prometheus.BuildFQName("cftunnelsync", subsystem, "pass_count_total"),
			"Total number of reconciliation passes completed.",
			nil, nil,
		),
		descLastDuration: prometheus.NewDesc(
			prometheus.BuildFQName("cftunnelsync", subsystem, "last_pass_duration_seconds"),
			"Duration of the last reconciliation pass in seconds.",
			nil, nil,
		),
		descLastError: prometheus.NewDesc(
			prometheus.BuildFQName("cftunnelsync", subsystem, "last_error"),
			"Whether the last reconciliation pass had an error (1=yes, 0=no).",
			nil, nil,
		),
		descCFAPICalls: prometheus.NewDesc(
			prometheus.BuildFQName("cftunnelsync", subsystem, "cf_api_calls_total"),
			"Total Cloudflare API calls.",
			nil, nil,
		),
		descCFAPIErrors: prometheus.NewDesc(
			prometheus.BuildFQName("cftunnelsync", subsystem, "cf_api_errors_total"),
			"Total Cloudflare API errors.",
			nil, nil,
		),
		descCFAPICallsByRes: prometheus.NewDesc(
			prometheus.BuildFQName("cftunnelsync", subsystem, "cf_api_calls_by_resource_total"),
			"Cloudflare API calls by resource type.",
			[]string{"resource"}, nil,
		),
		descEventCount: prometheus.NewDesc(
			prometheus.BuildFQName("cftunnelsync", subsystem, "docker_events_total"),
			"Total Docker events received.",
			nil, nil,
		),
		descTunnelID: prometheus.NewDesc(
			prometheus.BuildFQName("cftunnelsync", subsystem, "tunnel_id"),
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

	ch <- prometheus.MustNewConstMetric(c.descCFAPICalls, prometheus.CounterValue, float64(snap.CFStats.Calls))
	ch <- prometheus.MustNewConstMetric(c.descCFAPIErrors, prometheus.CounterValue, float64(snap.CFStats.Errors))

	if snap.CFStats.ByResource != nil {
		for resource, count := range snap.CFStats.ByResource {
			ch <- prometheus.MustNewConstMetric(c.descCFAPICallsByRes, prometheus.CounterValue, float64(count), resource)
		}
	}

	ch <- prometheus.MustNewConstMetric(c.descEventCount, prometheus.CounterValue, float64(snap.EventCount))

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
