package matching

import (
	"github.com/lightninglabs/subasta/order"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// OrderFilter is an interface that implements a generic filter that skips all
// orders from being included in the matchmaking process that don't meet the
// current criteria.
type OrderFilter interface {
	// IsSuitable returns true if this specific predicate doesn't have any
	// objection about an order being included in the matchmaking process.
	IsSuitable(order.ServerOrder) bool
}

// OrderFilterFunc is a simple function type that implements the OrderFilter
// interface.
type OrderFilterFunc func(order.ServerOrder) bool

// IsSuitable returns true if this specific predicate doesn't have any objection
// about an order being included in the matchmaking process.
//
// NOTE: This is part of the OrderFilter interface.
func (f OrderFilterFunc) IsSuitable(o order.ServerOrder) bool {
	return f(o)
}

// SuitsFilterChain returns true if all filters in the given chain see the
// given order as suitable.
func SuitsFilterChain(o order.ServerOrder, chain ...OrderFilter) bool {
	for _, filter := range chain {
		if !filter.IsSuitable(o) {
			return false
		}
	}

	return true
}

// NewBatchFeeRateFilter returns a new filter that filters out orders based on
// the current batch fee rate. All orders that have a max batch fee rate lower
// than the current estimated batch fee rate are skipped.
func NewBatchFeeRateFilter(batchFeeRate chainfee.SatPerKWeight) OrderFilter {
	return OrderFilterFunc(func(o order.ServerOrder) bool {
		if o.Details().MaxBatchFeeRate < batchFeeRate {
			log.Debugf("Filtered out order %v with max fee rate %v",
				o.Nonce(), o.Details().MaxBatchFeeRate)
			return false
		}

		return true
	})
}

// NewLeaseDurationFilter returns a new filter that filters out all orders that
// don't have the given lease duration.
func NewLeaseDurationFilter(leaseDuration uint32) OrderFilter {
	return OrderFilterFunc(func(o order.ServerOrder) bool {
		if o.Details().LeaseDuration != leaseDuration {
			log.Debugf("Filtered out order %v with lease duration "+
				"%v", o.Nonce(), o.Details().LeaseDuration)
			return false
		}

		return true
	})
}
