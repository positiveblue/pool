package monitoring

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// batchCollectorName is the name of the MetricGroup for the
	// batchCollector.
	batchCollectorName = "batch"

	// batchCount is the number of batches that happened up to this point
	// in time.
	batchCount = "batch_count"

	// batchNumMatchedOrders is the number of orders matched in each
	// completed order batch.
	batchNumMatchedOrders = "batch_num_matched_orders"

	// batchUnitsMatched is the number of units matched for each order pair
	// in each completed order batch.
	batchUnitsMatched = "batch_units_matched"

	// batchNumAccounts is the number of accounts involved in each completed
	// order batch.
	batchNumAccounts = "batch_num_accounts"

	// batchClearingPrice is the clearing price of each completed order
	// batch.
	batchClearingPrice = "batch_clearing_price"

	// batchAuctionFees is the number of sats in auction fees the
	// auctioneer received for each completed order batch.
	batchAuctionFees = "batch_auction_fees"

	// batchTxNumInputs is the number of transaction inputs for each of the
	// published batch transactions.
	batchTxNumInputs = "batch_tx_num_inputs"

	// batchTxNumOutputs is the number of transaction outputs for each of
	// the published batch transactions.
	batchTxNumOutputs = "batch_tx_num_outputs"

	// batchTxFees is the number of satoshis paid in transaction fees for
	// each of the published batch transactions.
	batchTxFees = "batch_tx_fees"

	// batchTxFeeEstimate is the fee estimate for our current batch conf
	// target.
	batchTxFeeEstimate = "batch_tx_fee_estimate"

	// conflictCount is the number of funding conflicts recorder per peer.
	conflictCount = "conflict_count"

	// batchTotalVolume is a gague that keeps track the total volume of the
	// system to date.
	//
	// TODO(roasbeef): need to find a more scalable way of exporting this
	// info, just make into admin RPC command?
	batchTotalVolume = "batch_total_volume"

	// batchTotalRevenue is a gague that keeps track of the total amount of
	// revenue the system has earned to date.
	batchTotalRevenue = "batch_total_revenue"

	// batchMatchAttempts is a counter that is incremented with each attempt at
	// matching a new batch.
	batchMatchAttempts = "batch_match_attempt"

	// batchExecutionAttempts is a counter that's incremented each time we
	// try to *execute* a new batch.
	batchExecutionAttempts = "batch_exe_attempt"

	// batchMatchTime is the amount of time it took to generate a match of
	// a given batch.
	//
	// TODO(roasbeef): what labels other than batch ID?
	batchMatchTime = "batch_match_latency_ms"

	// batchExecutionTime is the amount of time that it took to execute a
	// given batch from top to bottom.
	batchExecutionTime = "batch_execution_latency_ms"

	// batchFailure is a counter that's incremented each time we fail to
	// fully execute a batch.
	batchFailureCounter = "batch_failures"

	labelBatchID   = "batch_id"
	labelOrderPair = "order_pair"

	labelReporterID = "reporter_id"
	labelSubjectID  = "subject_id"
	labelReason     = "reason"
	labelMarketMade = "market_made"

	labelConfTarget = "conf_target"
)

// batchCollector is a collector that keeps track of our accounts.
type batchCollector struct {
	cfg *PrometheusConfig

	g gauges

	permGauges gauges

	// batchMatchCounter is incremented each time we attempt to match a new
	// match. This lets us see how many times we attempt to restart match
	// making with a given batch.
	batchMatchCounter *prometheus.CounterVec

	// batchExecutionCounter is incremented each time we attempt to execute
	// a given batch. Note that it's possible that we execute a match set
	// multiple times as traders fall out of it for w/e reason.
	batchExecutionCounter *prometheus.CounterVec

	// batchFailureCounter is incremented each time we fail a batch.
	batchFailureCounter *prometheus.CounterVec

	// matchingLatencyHisto is a histogram of how long it takes to perform
	// match making for  a given batch.
	matchingLatencyHisto *prometheus.HistogramVec

	// executionLatencyHisto is a histogram of how long it takes to execute
	// a given match.
	executionLatencyHisto *prometheus.HistogramVec
}

// newBatchCollector makes a new batchCollector instance.
func newBatchCollector(cfg *PrometheusConfig) *batchCollector {
	baseLabels := []string{labelBatchID}
	conflictLabels := []string{labelReporterID, labelSubjectID, labelReason}
	g := make(gauges)
	g.addGauge(batchCount, "number of batches", nil)
	g.addGauge(
		batchNumMatchedOrders, "number of matched orders", baseLabels,
	)
	g.addGauge(
		batchUnitsMatched, "number of matched units per order pair",
		append(baseLabels, labelOrderPair),
	)
	g.addGauge(batchNumAccounts, "number of involved accounts", baseLabels)
	g.addGauge(batchClearingPrice, "batch clearing rate/price", baseLabels)
	g.addGauge(batchAuctionFees, "fees received by auctioneer", baseLabels)
	g.addGauge(batchTxNumInputs, "number of on-chain tx inputs", baseLabels)
	g.addGauge(
		batchTxNumOutputs, "number of on-chain tx outputs", baseLabels,
	)
	g.addGauge(batchTxFees, "on-chain tx fees in satoshis", baseLabels)

	feeRateLabels := []string{labelConfTarget}
	g.addGauge(
		batchTxFeeEstimate, "on-chain tx fee estimate in sat/vb",
		feeRateLabels,
	)

	g.addGauge(conflictCount, "number of funding conflicts", conflictLabels)

	// Now that we've created the set of gagues to be reset each round,
	// we'll make a new set that are never reset to ensure we have a
	// cummulative vaue.
	permGauges := make(gauges)
	permGauges.addGauge(batchTotalVolume, "total batch volume", nil)
	permGauges.addGauge(batchTotalRevenue, "total batch revenue", nil)

	return &batchCollector{
		cfg:        cfg,
		g:          g,
		permGauges: permGauges,

		batchMatchCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: batchMatchAttempts,
				Help: "counter that tracks match making attempts",
			},
			append(baseLabels, labelMarketMade),
		),
		batchExecutionCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: batchExecutionAttempts,
				Help: "counter that tracks batch execution attempts",
			},
			baseLabels,
		),
		batchFailureCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: batchFailureCounter,
				Help: "a counter that's incremented each time we fail a batch",
			},
			append(baseLabels, labelReason, labelReporterID),
		),

		matchingLatencyHisto: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name: batchMatchTime,
				Help: "time in ms it takes to find a match",

				// We'll create a series of exponentially
				// increasing buckets starting from 10 ms. The
				// final set of buckets will be: [10 20 40 80
				// 160 320 640 1280 2560 5120 10240 20480
				// 40960].
				Buckets: prometheus.ExponentialBuckets(
					10, 2, 13,
				),
			},
			baseLabels,
		),
		executionLatencyHisto: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name: batchExecutionTime,
				Help: "time in ms it takes to execute a batch",

				// We'll create a series of exponentially
				// increasing buckets starting from 10 ms. The
				// final set of buckets will be: [10 20 40 80
				// 160 320 640 1280 2560 5120 10240 20480
				// 40960].
				Buckets: prometheus.ExponentialBuckets(
					10, 2, 13,
				),
			},
			baseLabels,
		),
	}
}

// Name is the name of the metric group. When exported to prometheus, it's
// expected that all metric under this group have the same prefix.
//
// NOTE: Part of the MetricGroup interface.
func (c *batchCollector) Name() string {
	return batchCollectorName
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector to the provided channel and returns once the
// last descriptor has been sent.
//
// NOTE: Part of the prometheus.Collector interface.
func (c *batchCollector) Describe(ch chan<- *prometheus.Desc) {
	c.g.describe(ch)
}

// Collect is called by the Prometheus registry when collecting metrics.
//
// NOTE: Part of the prometheus.Collector interface.
func (c *batchCollector) Collect(ch chan<- prometheus.Metric) {
	// We must reset our metrics that we collect from the DB here, otherwise
	// they would increment with each run.
	c.g.reset()

	// Next, we'll fetch the current batch key.
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	currentBatchKey, err := c.cfg.Store.BatchKey(ctx)
	if err != nil {
		log.Errorf("could not query batch key: %v", err)
		return
	}

	// Record all metrics for the currently tracked funding conflicts.
	conflicts := c.cfg.FundingConflicts.Export()
	for reporter, subjectMap := range conflicts {
		reporterID := hex.EncodeToString(reporter[:])
		for subject, conflictList := range subjectMap {
			subjectID := hex.EncodeToString(subject[:])
			for _, conflict := range conflictList {
				// This label combination together with Inc()
				// has the effect of counting the number of
				// different problems between two nodes. So we
				// can easily track if a node gets flagged over
				// and over for the same reason.
				labels := prometheus.Labels{
					labelReporterID: reporterID,
					labelSubjectID:  subjectID,
					labelReason:     conflict.Reason,
				}

				c.g[conflictCount].With(labels).Inc()
			}
		}
	}

	// The "current" batch key is always the one that is not yet completed.
	// We need to roll back by one, unless we're still at the beginning.
	if currentBatchKey.IsEqual(subastadb.InitialBatchKey) {
		// Let's still collect the conflicts but then bail as there are
		// no batches to collect metrics for.
		c.g.collect(ch)
		return
	}
	batchKey := poolscript.DecrementKey(currentBatchKey)
	batchID := orderT.NewBatchID(batchKey)

	// Count the number of batches that happened so far by "counting down"
	// until we're at the initial key. We start at 1 because if there wasn't
	// one yet, we'd returned above.
	numBatches := float64(1)
	tempBatchKey := batchKey
	var totalVolume, totalRevenue btcutil.Amount
	for !tempBatchKey.IsEqual(subastadb.InitialBatchKey) {
		numBatches++
		tempBatchKey = poolscript.DecrementKey(tempBatchKey)

		// For the set of metrics which want a cumulative value, we'll
		// fetch the snapshot as well to be able to export the volume
		// as well as the revenue.
		//
		// TODO(roasbeef): fetch all and do a single batch query
		// instead?
		batchID := orderT.NewBatchID(tempBatchKey)
		batch, err := c.cfg.Store.GetBatchSnapshot(ctx, batchID)
		if err != nil {
			log.Errorf("could not query batch snapshot with ID %v: %v",
				batchID, err)
		}

		// Tally up the total volume in the batch as well as the amount
		// of satoshis the auctioneer has earned from the batch.
		for _, matchedOrder := range batch.OrderBatch.Orders {
			totalVolume += matchedOrder.Details.Quote.TotalSatsCleared
		}
		totalRevenue += batch.OrderBatch.FeeReport.AuctioneerFeesAccrued

		// Unlikely to happen but in case we start from an invalid value
		// we want to avoid an endless loop. Can't image we'd ever do
		// more than a million batches before this code is replaced, so
		// we take that as our safety net.
		if numBatches > 1e6 {
			break
		}
	}
	c.g[batchCount].With(nil).Set(numBatches)

	c.permGauges[batchTotalVolume].With(nil).Set(float64(totalVolume))
	c.permGauges[batchTotalRevenue].With(nil).Set(float64(totalRevenue))

	// Fetch the batch by its ID.
	ctx, cancel = context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	batchSnapshot, err := c.cfg.Store.GetBatchSnapshot(ctx, batchID)
	if err != nil {
		log.Errorf("could not query batch snapshot with ID %v: %v",
			batchID, err)
		return
	}
	batch := batchSnapshot.OrderBatch
	tx := batchSnapshot.BatchTx

	// Record all metrics for the current batch. We don't need to collect
	// the information about previous batches, Prometheus itself will add
	// the time factor by querying these metrics periodically.
	c.observeBatch(batchID, batch, tx)

	feeRate, err := c.cfg.Lnd.WalletKit.EstimateFee(
		ctx, c.cfg.BatchConfTarget,
	)
	if err != nil {
		log.Errorf("could not query fee estimate: %v", err)
		return
	}

	labels := prometheus.Labels{
		labelConfTarget: fmt.Sprintf("%d", c.cfg.BatchConfTarget),
	}
	c.g[batchTxFeeEstimate].With(labels).Set(
		float64(feeRate.FeePerKVByte()) / 1000,
	)

	// Finally, collect the metrics into the prometheus collect channel.
	c.g.collect(ch)
	c.permGauges.collect(ch)
}

// observeBatch records all metrics of a batch and fetches the details of the
// batch chain TX from the chain backend. The tx details are then also added to
// the metrics.
func (c *batchCollector) observeBatch(id orderT.BatchID,
	b *matching.OrderBatch, tx *wire.MsgTx) {

	labels := prometheus.Labels{
		labelBatchID: hex.EncodeToString(id[:]),
	}

	c.g[batchNumMatchedOrders].With(labels).Set(float64(len(b.Orders)))
	c.g[batchNumAccounts].With(labels).Set(
		float64(len(b.FeeReport.AccountDiffs)),
	)
	c.g[batchClearingPrice].With(labels).Set(float64(b.ClearingPrice))
	c.g[batchAuctionFees].With(labels).Set(
		float64(b.FeeReport.AuctioneerFeesAccrued),
	)
	c.g[batchTxNumInputs].With(labels).Set(float64(len(tx.TxIn)))
	c.g[batchTxNumOutputs].With(labels).Set(float64(len(tx.TxOut)))

	// Query the chain backend for the fees (need to get the full TxIn for
	// this).
	fee, err := c.cfg.BitcoinClient.GetTxFee(tx)
	if err != nil {
		log.Errorf("could not query fee for tx %v: %v", tx.TxHash(),
			err)
		return
	}
	c.g[batchTxFees].With(labels).Set(float64(fee))

	// For the units matched, we also add a label that consists of the two
	// order pairs. That way we can get the average channel size easily.
	for _, matchedOrder := range b.Orders {
		ask := matchedOrder.Details.Ask
		bid := matchedOrder.Details.Bid
		units := matchedOrder.Details.Quote.UnitsMatched

		orderPair := fmt.Sprintf("%s:%s", ask.Nonce().String(),
			bid.Nonce().String())
		labels = prometheus.Labels{
			labelBatchID:   hex.EncodeToString(id[:]),
			labelOrderPair: orderPair,
		}

		c.g[batchUnitsMatched].With(labels).Set(float64(units))
	}
}

// fetchBatchCollector is a helper function that we'll use to allow those at
// the package level to obtain an active pointer to the current batchCollector
// instance.
func fetchBatchCollector() *batchCollector {
	metricsMtx.Lock()
	defer metricsMtx.Unlock()

	batchGroup, ok := activeGroups[batchCollectorName]
	if !ok {
		return nil
	}

	s := batchGroup.(*batchCollector)
	return s
}

// ObserveBatchMatchAttempt increments the counter that tracks how often we
// attempt to perform match making for a given batch.
func ObserveBatchMatchAttempt(batchID []byte, marketMade bool) {
	c := fetchBatchCollector()
	if c == nil {
		return
	}

	c.batchMatchCounter.With(prometheus.Labels{
		labelBatchID:    hex.EncodeToString(batchID),
		labelMarketMade: strconv.FormatBool(marketMade),
	}).Inc()
}

// ObserveBatchExecutionAttempt increments a counter that tracks how often we
// attempt to execute a batch given a set of valid matches.
func ObserveBatchExecutionAttempt(batchID []byte) {
	c := fetchBatchCollector()
	if c == nil {
		return
	}

	c.batchExecutionCounter.With(prometheus.Labels{
		labelBatchID: hex.EncodeToString(batchID),
	}).Inc()
}

// ObserveMatchingLatency exports a new observation of how long it took to
// perform match making for a given batch.
func ObserveMatchingLatency(batchID []byte, t time.Duration) {
	c := fetchBatchCollector()
	if c == nil {
		return
	}

	c.matchingLatencyHisto.With(prometheus.Labels{
		labelBatchID: hex.EncodeToString(batchID),
	}).Observe(float64(t.Milliseconds()))
}

// ObserveBatchExecutionLatency exports a new observation of how long it took
// to fully execute a given batch.
func ObserveBatchExecutionLatency(batchID []byte, t time.Duration) {
	c := fetchBatchCollector()
	if c == nil {
		return
	}

	c.executionLatencyHisto.With(prometheus.Labels{
		labelBatchID: hex.EncodeToString(batchID),
	}).Observe(float64(t.Milliseconds()))
}

// ObserveBatchExeFailure observes an instance wherein we attempt to execute a
// batch but failed.
func ObserveBatchExeFailure(batchID []byte, reason string, reporter []byte) {
	c := fetchBatchCollector()
	if c == nil {
		return
	}

	c.batchFailureCounter.With(prometheus.Labels{
		labelBatchID:    hex.EncodeToString(batchID),
		labelReason:     reason,
		labelReporterID: hex.EncodeToString(reporter),
	}).Inc()
}

// RegisterMetricFuncs signals to the underlying hybrid collector that it
// should register all metrics that it aims to export with the global
// Prometheus registry. Rather than using the series of "MustRegister"
// directives, implementers of this interface should instead propagate back any
// errors related to metric registration.
//
// NOTE: Part of the MetricGroup interface.
func (c *batchCollector) RegisterMetricFuncs() error {
	// We'll also need to register our counters now individually.
	if err := prometheus.Register(c.batchMatchCounter); err != nil {
		return err
	}
	if err := prometheus.Register(c.batchExecutionCounter); err != nil {
		return err
	}
	if err := prometheus.Register(c.batchFailureCounter); err != nil {
		return err
	}
	if err := prometheus.Register(c.matchingLatencyHisto); err != nil {
		return err
	}
	if err := prometheus.Register(c.executionLatencyHisto); err != nil {
		return err
	}

	return prometheus.Register(c)
}

// A compile time flag to ensure the batchCollector satisfies the MetricGroup
// interface.
var _ MetricGroup = (*batchCollector)(nil)

func init() {
	metricsMtx.Lock()
	metricGroups[batchCollectorName] = func(cfg *PrometheusConfig) (
		MetricGroup, error) {

		return newBatchCollector(cfg), nil
	}
	metricsMtx.Unlock()
}
