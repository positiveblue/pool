package monitoring

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/subasta/chain"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

const (
	// dbTimeout is the default database timeout.
	dbTimeout = 20 * time.Second
)

// MetricGroupCreator is a factory method that given the primary prometheus
// config, will create a new MetricGroup that will be managed by the main
// PrometheusExporter.
type MetricGroupCreator func(*PrometheusConfig) (MetricGroup, error)

var (
	// metricGroups is a global variable of all registered metrics
	// projected by the mutex below. All new MetricGroups should add
	// themselves to this map within the init() method of their file.
	metricGroups = make(map[string]MetricGroupCreator)

	// activeGroups is a global map of all active metric groups. This can
	// be used by some of the "static' package level methods to look up the
	// target metric group to export observations.
	activeGroups = make(map[string]MetricGroup)

	metricsMtx sync.Mutex
)

// MetricGroup is the primary interface of this package. The main exporter (in
// this case the PrometheusExporter), will manage these directly, ensuring that
// all MetricGroups are registered before the main prometheus exporter starts
// and any additional tracing is added.
type MetricGroup interface {
	// Name is the name of the metric group. When exported to prometheus,
	// it's expected that all metric under this group have the same prefix.
	Name() string

	// RegisterMetricFuncs signals to the underlying hybrid collector that
	// it should register all metrics that it aims to export with the
	// global Prometheus registry. Rather than using the series of
	// "MustRegister" directives, implementers of this interface should
	// instead propagate back any errors related to metric registration.
	RegisterMetricFuncs() error
}

// PrometheusConfig is the set of configuration data that specifies if
// Prometheus metric exporting is activated, and if so the listening address of
// the Prometheus server.
type PrometheusConfig struct {
	// Active, if true, then Prometheus metrics will be expired.
	Active bool `long:"active" description:"if true prometheus metrics will be exported"`

	// ListenAddr is the listening address that we should use to allow the
	// main Prometheus server to scrape our metrics.
	ListenAddr string `long:"listenaddr" description:"the interface we should listen on for prometheus"`

	// Store is the active database where all data is stored.
	Store *subastadb.EtcdStore

	// BitcoinClient holds the connection to the bitcoin chain backend.
	BitcoinClient *chain.BitcoinClient

	// LightningNode is the connection to the backing Lightning node
	// for the subasta server. Only read-only permissions are required.
	Lnd lndclient.LndServices

	// FundingConflicts is a conflict tracker that keeps track of funding
	// conflicts (problems during channel funding) between nodes.
	FundingConflicts matching.NodeConflictTracker

	// AdminRPCServer is a pointer to the admin RPC server. We use this to
	// ensure that we'll export metrics for the admin RPC.
	AdminRPCServer *grpc.Server

	// PublicRPCServer is a pointer to the main RPC server. We use this to
	// export generic RPC metrics to monitor the health of the service.
	PublicRPCServer *grpc.Server

	// NumActiveTraders should return the number of active traders in the
	// venue in a thread-safe manner.
	NumActiveTraders func() int

	// BatchConfTarget should mimic the conf target that is set in the main
	// config. We use it to monitor the estimated batch feerates.
	BatchConfTarget int32

	// SnapshotSource is a function that returns the batch snapshot for a
	// batch with the given batch ID.
	SnapshotSource func(context.Context,
		*btcec.PublicKey) (*subastadb.BatchSnapshot, error)
}

// PrometheusExporter is a metric exporter that uses Prometheus directly. The
// internal subasta server will interact with this struct in order to export
// relevant metrics such as the total number of fees earned from swaps over
// time.
type PrometheusExporter struct {
	config *PrometheusConfig
}

// NewPrometheusExporter makes a new instance of the PrometheusExporter given
// the config.
func NewPrometheusExporter(cfg *PrometheusConfig) *PrometheusExporter {
	return &PrometheusExporter{
		config: cfg,
	}
}

// Start registers all relevant metrics with the Prometheus library, then
// launches the HTTP server that Prometheus will hit to scrape our metrics.
func (p *PrometheusExporter) Start() error {
	// If we're not active, then there's nothing more to do.
	if !p.config.Active {
		return nil
	}

	// Next, we'll attempt to register all our metrics. If we fail to
	// register ANY metric, then we'll fail all together.
	if err := p.registerMetrics(); err != nil {
		return err
	}

	// Finally, we'll launch the HTTP server that Prometheus will use to
	// scape our metrics.
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		fmt.Println(http.ListenAndServe(p.config.ListenAddr, nil))
	}()

	return nil
}

// registerMetrics iterates through all the registered metric groups and
// attempts to register each one. If any of the MetricGroups fail to register,
// then an error will be returned.
func (p *PrometheusExporter) registerMetrics() error {
	metricsMtx.Lock()
	defer metricsMtx.Unlock()

	for _, metricGroupFunc := range metricGroups {
		metricGroup, err := metricGroupFunc(p.config)
		if err != nil {
			return err
		}

		if err := metricGroup.RegisterMetricFuncs(); err != nil {
			return err
		}

		activeGroups[metricGroup.Name()] = metricGroup
	}

	return nil
}

// gauges is a map type that maps a gauge to its unique name.
type gauges map[string]*prometheus.GaugeVec

// addGauge adds a new gauge vector to the map.
func (g gauges) addGauge(name, help string, labels []string) {
	g[name] = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: name,
			Help: help,
		},
		labels,
	)
}

// describe describes all gauges contained in the map to the given channel.
func (g gauges) describe(ch chan<- *prometheus.Desc) {
	for _, gauge := range g {
		gauge.Describe(ch)
	}
}

// collect collects all metrics of the map's gauges to the given channel.
func (g gauges) collect(ch chan<- prometheus.Metric) {
	for _, gauge := range g {
		gauge.Collect(ch)
	}
}

// reset resets all gauges in the map.
func (g gauges) reset() {
	for _, gauge := range g {
		gauge.Reset()
	}
}
