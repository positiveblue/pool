package metrics

import (
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
)

// BatchMetric is the struct used for generating insights about the given
// batches.
type BatchMetric struct {
	// TotalSatsLeased is the amount of sats leased in the given batches.
	TotalSatsLeased int64

	// TotalFeesAccrued is the amount of fees accrued in sats by an asker
	// in the given batches.
	TotalFeesAccrued int64

	// MedianAPR is the median APR of all matched orders in the given
	// batches.
	MedianAPR float64

	// MedianOrderSize is the median order size of all matched orders in the
	// given bathes.
	MedianOrderSize float64
}

// OrderMetrics is the struct used for generating insights about given Orders.
type OrderMetric struct {
	// NumAsks is the nubmer of asks in the given orders.
	NumAsks int64

	// NumBids is the number of bids in the given Bids.
	NumBids int64

	// AskVolume is the total ask volume of all open orders.
	//
	// NOTE: Only the unfullfiled units are taken into account.
	AskVolume int64

	// BidVolume is the total bid volume of all open orders.
	//
	// NOTE: Only the unfullfiled units are taken into account.
	BidVolume int64
}

// Manager interface for obtaining metrics.
type Manager interface {
	// GenerateOrderMetric calculates the relevant OrderMetric's for a list
	// of ServerOrders.
	GenerateOrderMetric() (*OrderMetric, error)

	// GenerateBatchMetrics calculates the relevant BatchMetric's for a list
	// of MatchedOrders.
	GenerateBatchMetrics() (*BatchMetric, error)

	// TODO(jbrill): proper comment.
	GetBatches() map[orderT.BatchID]*matching.BatchSnapshot

	// TODO(jbrill): proper comment.
	GetOrders() []*order.ServerOrder

	// TODO(jbrill): proper comment.
	GetTimeDurations() []time.Duration

	// TODO(jbrill): proper comment.
	GetRefreshRate() time.Duration

	// TODO(jbrill): proper comment.
	SetRefreshRate(newTimeDuration time.Duration)
}
