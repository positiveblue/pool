package metrics

import (
	"fmt"
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/montanaflynn/stats"
)

// ManagerConfig contains all of the required dependencies for the Manager 
// to carry out its duties.
type ManagerConfig struct {
        // TODO(jbrill): proper comment.
        Batches(context.Context) (map[orderT.BatchID]*matching.BatchSnapshot,
		error)

        // TODO(jbrill): proper comment.
        GetOrders(ctx context.Context) ([]order.ServerOrder, error)

        // TODO(jbrill): proper comment.
	LastUpdated time.Time

	// RefreshRate is the rate at which we'll repopulate the cache
	// specifically the Orders and Batches to the config struct.
	RefreshRate time.Duration

	// TimeDurationBuckets signify the deltas that we'll add metrics to
	// Ex: 1 year, 1 month, 1 week, etc...
	TimeDurationBuckets []time.Duration
}

// manager is responsible for the management of metrics.
type manager struct {
	cfg *ManagerConfig
        batches map[orderT.BatchID]*matching.BatchSnapshot
        orders []order.ServerOrder
}

// Compile time assertion that Manager implements the Manager interface.
var _ Manager = (*manager)(nil)

// NewManager instantiates a new Manager backed by the given config.
func NewManager(cfg *ManagerConfig) (*manager, error) {
	m := &manager{
		cfg: *cfg,
	}

	return m, nil
}


func (m *manager) getBatches() {
        if m.needsRefres() {
                m.refresh()
        }

        return m.batches
}

func (m *manager) getOrders() {
        if m.needsRefres() {
                m.refresh()
        }
        return m.orders
}

// GenerateOrderMetric iterates through a list of server orders, which are fed from
// the rpcserver, organized by leaseduration. The serverorders should correlate to the
// open orders cached by the rpcserver.
func (m *manager) GenerateOrderMetric() (*OrderMetric, error) {

        orders := m.getOrders()

	if len(orders) == 0 {
		return &OrderMetric{}, nil
	}

	for _, o := range orders {
                unfulfilled := (*o).Details().UnitsUnfulfilled
		volume := (int64(unfulfilled) * int64(orderT.BaseSupplyUnit))
		switch (*o).(type) {
		case *order.Ask:
			NumAsks++
			AskVolume += volume
		case *order.Bid:
			NumBids++
			BidVolume += volume
		}
	}

	return &OrderMetric{
		NumAsks:   NumAsks,
		NumBids:   NumBids,
		AskVolume: AskVolume,
		BidVolume: BidVolume,
	}, nil
}

// GenerateAPR provides the float64 value of the yearly APR of an order premium
// Formula for APR: rate_fixed # units = satoshis of interest premium / satoshis of channel capacity / block / billion
// Example: 5000 * 144 * 365 / 1e9 ~= 0.26 or 26%.
func GenerateAPR(batchClearingPricePremium float64) float64 {
	blocksPerDay := float64(144)
	daysPerYear := float64(365)
	APR := (batchClearingPricePremium * blocksPerDay * daysPerYear) / 1_000_000_000
	return APR
}

// TotalSatsLeasedFromMatchedOrders returns the cumulative
// sats leased from a list of matched orders.
func TotalSatsLeasedFromMatchedOrders(matchedOrders []*matching.MatchedOrder) int64 {
	satsLeased := int64(0)
	for _, matchedOrder := range matchedOrders {
		satsLeased += int64(matchedOrder.Details.Quote.TotalSatsCleared)
	}
	return satsLeased
}

// TotalFeesAccruedFromMatchedOrders returns the cumulative
// asker fees from a list of matched orders.
func TotalFeesAccruedFromMatchedOrders(matchedOrders []*matching.MatchedOrder) int64 {
	askerFees := int64(0)
	for _, matchedOrder := range matchedOrders {
		askerFees += int64(matchedOrder.Details.Quote.MatchingRate)
	}
	return askerFees
}

// OrderSizesFromMatchedOrders returns the median order size
// from a list of matched orders.
func OrderSizesFromMatchedOrders(matchedOrders []*matching.MatchedOrder) []float64 {
	medians := make([]float64, len(matchedOrders))
	for idx, matchedOrder := range matchedOrders {
		medians[idx] = float64(matchedOrder.Details.Quote.TotalSatsCleared)
	}
	return medians
}

// AllAPRsFromMatchedOrders returns the median APR size (via fixed premium)
// from a list of matched orders.
func AllAPRsFromMatchedOrders(matchedOrders []*matching.MatchedOrder) []float64 {
	allAPRs := make([]float64, len(matchedOrders))
	for idx, matchedOrder := range matchedOrders {
		generatedAPR := GenerateAPR(float64(matchedOrder.Details.Quote.MatchingRate))
		allAPRs[idx] = generatedAPR
	}
	return allAPRs
}

// GenerateBatchMetrics iterates through a list of matchedOrders, which are fed from
// the rpcserver, defining matchedorders by leaseduration and timeframe. The function
// returns a pointer to a batch metric containing the relevant data.
func (m *manager) GenerateBatchMetrics(matchedOrders []*matching.MatchedOrder) (*BatchMetric, error) {
	if len(matchedOrders) == 0 {
		return &BatchMetric{}, nil
	}

	allAPRs := AllAPRsFromMatchedOrders(matchedOrders)
	medianOrderSizes := OrderSizesFromMatchedOrders(matchedOrders)
	totalSatsLeased := TotalSatsLeasedFromMatchedOrders(matchedOrders)
	totalAskerFeesAccrued := TotalFeesAccruedFromMatchedOrders(matchedOrders)

	medianOfOrderAPRs, err := stats.Median(allAPRs)
	if err != nil {
		return nil, fmt.Errorf("error generating median APR: %v", err)
	}
	medianOfOrderSizes, err := stats.Median(medianOrderSizes)
	if err != nil {
		return nil, fmt.Errorf("error generating median of order sizes: %v", err)
	}

	return &BatchMetric{
		TotalSatsLeased:  totalSatsLeased,
		TotalFeesAccrued: totalAskerFeesAccrued,
		MedianAPR:        medianOfOrderAPRs,
		MedianOrderSize:  medianOfOrderSizes,
	}, nil
}

// SetBatches sets the config's batches.
func (m *manager) SetBatches(batches map[orderT.BatchID]*matching.BatchSnapshot) error {
	m.cfg.Batches = batches
	return nil
}

// SetOrders sets the config's orders.
func (m *manager) SetOrders(orders []*order.ServerOrder) error {
	m.cfg.Orders = orders
	return nil
}

// SetLastUpdated sets the config's last updated field.
func (m *manager) SetLastUpdated(lastUpdated time.Time) error {
	m.cfg.LastUpdated = lastUpdated
	return nil
}

// GetBatches gets the config's batches.
func (m *manager) GetBatches() map[orderT.BatchID]*matching.BatchSnapshot {
	return m.cfg.Batches
}

// GetOrders gets the config's orders.
func (m *manager) GetOrders() []*order.ServerOrder {
	return m.cfg.Orders
}

// GetLastUpdated gets the config's last updated field.
func (m *manager) GetLastUpdated() time.Time {
	return m.cfg.LastUpdated
}

// GetTimeDurations gets the config's time durations.
func (m *manager) GetTimeDurations() []time.Duration {
	return m.cfg.TimeDurationBuckets
}

// GetRefreshRate gets the config's cache refresh rate.
func (m *manager) GetRefreshRate() time.Duration {
	return m.cfg.RefreshRate
}

// SetRefreshRate sets the config's refresh rate.
func (m *manager) SetRefreshRate(newTimeDuration time.Duration) {
	m.cfg.RefreshRate = newTimeDuration
}
