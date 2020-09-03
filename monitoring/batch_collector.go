package monitoring

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/llm/clmscript"
	orderT "github.com/lightninglabs/llm/order"
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

	// conflictCount is the number of funding conflicts recorder per peer.
	conflictCount = "conflict_count"

	labelBatchID   = "batch_id"
	labelOrderPair = "order_pair"

	labelReporterID = "reporter_id"
	labelSubjectID  = "subject_id"
	labelReason     = "reason"
)

// batchCollector is a collector that keeps track of our accounts.
type batchCollector struct {
	cfg *PrometheusConfig

	g gauges
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
	g.addGauge(conflictCount, "number of funding conflicts", conflictLabels)
	return &batchCollector{
		cfg: cfg,
		g:   g,
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
	batchKey := clmscript.DecrementKey(currentBatchKey)
	batchID := orderT.NewBatchID(batchKey)

	// Count the number of batches that happened so far by "counting down"
	// until we're at the initial key. We start at 1 because if there wasn't
	// one yet, we'd returned above.
	numBatches := float64(1)
	tempBatchKey := batchKey
	for !tempBatchKey.IsEqual(subastadb.InitialBatchKey) {
		numBatches++
		tempBatchKey = clmscript.DecrementKey(tempBatchKey)

		// Unlikely to happen but in case we start from an invalid value
		// we want to avoid an endless loop. Can't image we'd ever do
		// more than a million batches before this code is replaced, so
		// we take that as our safety net.
		if numBatches > 1e6 {
			break
		}
	}
	c.g[batchCount].With(nil).Set(numBatches)

	// Fetch the batch by its ID.
	ctx, cancel = context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	batch, tx, err := c.cfg.Store.GetBatchSnapshot(ctx, batchID)
	if err != nil {
		log.Errorf("could not query batch snapshot with ID %v: %v",
			batchID, err)
		return
	}

	// Record all metrics for the current batch. We don't need to collect
	// the information about previous batches, Prometheus itself will add
	// the time factor by querying these metrics periodically.
	c.observeBatch(batchID, batch, tx)

	// Finally, collect the metrics into the prometheus collect channel.
	c.g.collect(ch)
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

// RegisterMetricFuncs signals to the underlying hybrid collector that it
// should register all metrics that it aims to export with the global
// Prometheus registry. Rather than using the series of "MustRegister"
// directives, implementers of this interface should instead propagate back any
// errors related to metric registration.
//
// NOTE: Part of the MetricGroup interface.
func (c *batchCollector) RegisterMetricFuncs() error {
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
