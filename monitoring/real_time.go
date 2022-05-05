//go:build !regtest
// +build !regtest

package monitoring

import (
	"encoding/hex"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// realTimeCollectorName is the name of the MetricGroup for the
	// realTimeCollector.
	realTimeCollectorName = "realTime"

	// acctKeyLabel is the label used to attached more context ti
	// incremented counter instances.
	acctKeyLabel = "acct_key"
)

// realTimeCollector is a collector dedicated to collecting more real-time
// metrics that occur outside of normal batch execution.
type realTimeCollector struct {
	cfg *PrometheusConfig

	numActiveTraders *prometheus.Desc

	numLiveTradersCounter *prometheus.CounterVec

	numFailedTradersCounter *prometheus.CounterVec
}

// newRealTimeCollector returns a new instance of the real-time collector.
func newRealTimeCollector(cfg *PrometheusConfig) *realTimeCollector {
	baseLabels := []string{acctKeyLabel}
	return &realTimeCollector{
		cfg: cfg,

		numActiveTraders: prometheus.NewDesc(
			"subasta_num_traders", "number of live traders at an instant",
			nil, nil,
		),

		numLiveTradersCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "subasta_live_traders",
				Help: "counter incremented with each new trader",
			},
			baseLabels,
		),
		numFailedTradersCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "subasta_failed_traders",
				Help: "counter incremented with each new trader disconnect",
			},
			baseLabels,
		),
	}
}

// RegisterMetricFuncs signals to the underlying hybrid collector that it
// should register all metrics that it aims to export with the global
// Prometheus registry. Rather than using the series of "MustRegister"
// directives, implementers of this interface should instead propagate back any
// errors related to metric registration.
//
// NOTE: Part of the MetricGroup interface.
func (r *realTimeCollector) RegisterMetricFuncs() error {
	// Before we register both servers, we'll also ensure that the
	// collector will export latency metrics for the histogram.
	grpc_prometheus.EnableHandlingTimeHistogram()

	// Export the gRPC information for both the admin RPC as well as the
	// public gRPC server. Exporting the admin RPC server as well gives us
	// a light weight audit log of any manual actions that may have been
	// carried out.
	grpc_prometheus.Register(r.cfg.PublicRPCServer)
	grpc_prometheus.Register(r.cfg.AdminRPCServer)

	// Next, we'll need to manually register our counters, as we don't send
	// them over the Describe channel like we do gauges.
	if err := prometheus.Register(r.numLiveTradersCounter); err != nil {
		return err
	}
	if err := prometheus.Register(r.numFailedTradersCounter); err != nil {
		return err
	}

	return prometheus.Register(r)
}

// Name is the name of the metric group. When exported to prometheus, it's
// expected that all metric under this group have the same prefix.
//
// NOTE: Part of the MetricGroup interface.
func (r *realTimeCollector) Name() string {
	return realTimeCollectorName
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector to the provided channel and returns once the
// last descriptor has been sent.
//
// NOTE: Part of the prometheus.Collector interface.
func (r *realTimeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- r.numActiveTraders
}

// Collect is called by the Prometheus registry when collecting metrics.
//
// NOTE: Part of the prometheus.Collector interface.
func (r *realTimeCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(
		r.numActiveTraders, prometheus.GaugeValue,
		float64(r.cfg.NumActiveTraders()),
	)
}

// fetchRealTimeCollector is a helper function that we'll use to allow those at
// the package level to obtain an active pointer to the current
// realTimeCollector instance.
func fetchRealTimeCollector() *realTimeCollector {
	metricsMtx.Lock()
	defer metricsMtx.Unlock()

	realTimeGroup, ok := activeGroups[realTimeCollectorName]
	if !ok {
		return nil
	}

	s := realTimeGroup.(*realTimeCollector)
	return s
}

// ObserveNewConnection is called to observe an instance of a new trader
// subscribing to batch events.
func ObserveNewConnection(acctKey [33]byte) {
	r := fetchRealTimeCollector()
	if r == nil {
		return
	}

	r.numLiveTradersCounter.With(prometheus.Labels{
		acctKeyLabel: hex.EncodeToString(acctKey[:]),
	}).Inc()
}

// ObserveFailedConnection is called to observe an instance of a trader being
// disconnected from their subscription.
func ObserveFailedConnection(acctKey [33]byte) {
	r := fetchRealTimeCollector()
	if r == nil {
		return
	}

	r.numFailedTradersCounter.With(prometheus.Labels{
		acctKeyLabel: hex.EncodeToString(acctKey[:]),
	}).Inc()
}

// TODO(roasbeef): make sure to have graphs of error rate using gRPC prom
// helpers
//  * also the number of LSAT routing fails?

// A compile time flag to ensure the realTimeCollectorName satisfies the
// MetricGroup interface.
var _ MetricGroup = (*realTimeCollector)(nil)

func init() {
	metricsMtx.Lock()
	metricGroups[realTimeCollectorName] = func(cfg *PrometheusConfig) (
		MetricGroup, error) {

		return newRealTimeCollector(cfg), nil
	}
	metricsMtx.Unlock()
}
