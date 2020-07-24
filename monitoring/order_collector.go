package monitoring

import (
	"context"

	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// orderCollectorName is the name of the MetricGroup for the
	// orderCollector.
	orderCollectorName = "order"

	// orderCount is a gauge that keeps track of the total number of all
	// orders there are.
	orderCount = "order_count"

	// orderUnits is a gauge that keeps track of the total number of order
	// units of all orders there are.
	orderUnits = "order_units"

	// orderUnitsUnfulfilled is a gauge that keeps track of the number of
	// unfulfilled order units of all orders there are.
	orderUnitsUnfulfilled = "order_units_unfulfilled"

	// orderDuration is a gauge that keeps track of the min/max order
	// duration of all orders there are.
	orderDuration = "order_duration"

	// orderRate is a gauge that keeps track of the order rate (parts per
	// billion) of all orders there are.
	orderRate = "order_rate"

	labelOrderType  = "order_type"
	labelOrderState = "order_state"
	labelOrderNonce = "order_nonce"
)

// orderCollector is a collector that keeps track of our accounts.
type orderCollector struct {
	cfg *PrometheusConfig

	g gauges
}

// newOrderCollector makes a new orderCollector instance.
func newOrderCollector(cfg *PrometheusConfig) *orderCollector {
	baseLabels := []string{labelOrderType, labelOrderState}
	g := make(gauges)
	g.addGauge(
		orderCount, "total number of orders", baseLabels,
	)
	g.addGauge(
		orderUnits, "number of units in orders",
		append(baseLabels, labelOrderNonce),
	)
	g.addGauge(
		orderUnitsUnfulfilled, "number of unfulfilled units in orders",
		append(baseLabels, labelOrderNonce),
	)
	g.addGauge(
		orderDuration, "min/max duration of orders",
		append(baseLabels, labelOrderNonce),
	)
	g.addGauge(
		orderRate, "fixed rate of orders",
		append(baseLabels, labelOrderNonce),
	)
	return &orderCollector{
		cfg: cfg,
		g:   g,
	}
}

// Name is the name of the metric group. When exported to prometheus, it's
// expected that all metric under this group have the same prefix.
//
// NOTE: Part of the MetricGroup interface.
func (c *orderCollector) Name() string {
	return orderCollectorName
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector to the provided channel and returns once the
// last descriptor has been sent.
//
// NOTE: Part of the prometheus.Collector interface.
func (c *orderCollector) Describe(ch chan<- *prometheus.Desc) {
	c.g.describe(ch)
}

// Collect is called by the Prometheus registry when collecting metrics.
//
// NOTE: Part of the prometheus.Collector interface.
func (c *orderCollector) Collect(ch chan<- prometheus.Metric) {
	// We must reset our metrics that we collect from the DB here, otherwise
	// they would increment with each run.
	c.resetGauges()

	// Next, we'll fetch all our active orders so we can export our series
	// of gauges.
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	activeOrders, err := c.cfg.Store.GetOrders(ctx)
	if err != nil {
		log.Errorf("unable to get active orders: %v", err)
		return
	}

	// Record all metrics for each order.
	for _, o := range activeOrders {
		c.observeOrder(o, true)
	}

	// Next, we'll fetch all our archived orders so we can export our series
	// of gauges.
	ctx, cancel = context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	archivedOrders, err := c.cfg.Store.GetArchivedOrders(ctx)
	if err != nil {
		log.Errorf("unable to get archived orders: %v", err)
		return
	}

	// Record all metrics for each order.
	for _, o := range archivedOrders {
		c.observeOrder(o, false)
	}

	// Finally, collect the metrics into the prometheus collect channel.
	c.g.collect(ch)
}

// observeOrder adds all order metrics to our gauges.
func (c *orderCollector) observeOrder(o order.ServerOrder, active bool) {
	labels := prometheus.Labels{
		labelOrderType:  o.Type().String(),
		labelOrderState: o.Details().State.String(),
	}
	c.g[orderCount].With(labels).Inc()

	// Only track exact values for active orders as these will create more
	// lines in the metrics export because we include the nonce.
	if !active {
		return
	}

	labels = prometheus.Labels{
		labelOrderType:  o.Type().String(),
		labelOrderState: o.Details().State.String(),
		labelOrderNonce: o.Nonce().String(),
	}

	c.g[orderUnits].With(labels).Set(float64(o.Details().Units))
	c.g[orderUnitsUnfulfilled].With(labels).Set(
		float64(o.Details().UnitsUnfulfilled),
	)
	c.g[orderRate].With(labels).Set(float64(o.Details().FixedRate))

	switch t := o.(type) {
	case *order.Ask:
		c.g[orderDuration].With(labels).Set(float64(t.MaxDuration()))

	case *order.Bid:
		c.g[orderDuration].With(labels).Set(float64(t.MinDuration()))
	}
}

// resetGauges resets all gauges and adds some default values for nicer graphs.
func (c *orderCollector) resetGauges() {
	c.g.reset()

	// To make prometheus reset values nicely, we can't just remove them.
	// We need to add a baseline for all order types and states.
	types := []orderT.Type{orderT.TypeAsk, orderT.TypeBid}
	for _, t := range types {
		for i := orderT.StateSubmitted; i <= orderT.StateFailed; i++ {
			labels := prometheus.Labels{
				labelOrderType:  t.String(),
				labelOrderState: i.String(),
			}
			c.g[orderCount].With(labels).Set(0)
		}
	}
}

// RegisterMetricFuncs signals to the underlying hybrid collector that it
// should register all metrics that it aims to export with the global
// Prometheus registry. Rather than using the series of "MustRegister"
// directives, implementers of this interface should instead propagate back any
// errors related to metric registration.
//
// NOTE: Part of the MetricGroup interface.
func (c *orderCollector) RegisterMetricFuncs() error {
	return prometheus.Register(c)
}

// A compile time flag to ensure the orderCollector satisfies the MetricGroup
// interface.
var _ MetricGroup = (*orderCollector)(nil)

func init() {
	metricsMtx.Lock()
	metricGroups[orderCollectorName] = func(cfg *PrometheusConfig) (
		MetricGroup, error) {

		return newOrderCollector(cfg), nil
	}
	metricsMtx.Unlock()
}
