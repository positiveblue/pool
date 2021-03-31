package order

import "sync"

// DurationBucketState is an enum-like variable that denotes the current state
// of a given duration bucket within the market.
type DurationBucketState uint8

const (
	// BucketStateNoMarket indicates that this bucket doesn't actually
	// exist, in that no market is present for this market.
	BucketStateNoMarket = 0

	// BucketStateMarketClosed indicates that this market exists, but that
	// it isn't currently running.
	BucketStateMarketClosed = 1

	// BucketStateAcceptingOrders indicates that we're accepting orders for
	// this bucket, but not yet clearing for this duration.
	BucketStateAcceptingOrders = 2

	// BucketStateClearingMarket indicates that we're accepting orders,
	// and fully clearing the market for this duration.
	BucketStateClearingMarket = 3
)

// String returns a human-readable version of the target DurationBucketState
// value.
func (d DurationBucketState) String() string {
	switch d {
	case BucketStateNoMarket:
		return "BucketStateNoMarket"

	case BucketStateMarketClosed:
		return "BucketStateMarketClosed"

	case BucketStateAcceptingOrders:
		return "BucketStateAcceptingOrders"

	case BucketStateClearingMarket:
		return "BucketStateClearingMarket"

	default:
		return "<UnknownMarketState>"
	}
}

// DurationBuckets is a central registry that indicates which duration buckets
// are currently active in the market as is.
type DurationBuckets struct {
	sync.RWMutex

	// buckets maps a duration expressed in blocks to the current state of
	// the market.
	buckets map[uint32]DurationBucketState
}

// NewDurationBuckets returns a new instance of the DurationBuckets struct.
func NewDurationBuckets() *DurationBuckets {
	return &DurationBuckets{
		buckets: make(map[uint32]DurationBucketState),
	}
}

// QueryMarketState returns the current market state for a given duration.
func (d *DurationBuckets) QueryMarketState(durationBlocks uint32,
) DurationBucketState {

	d.RLock()
	defer d.RUnlock()

	marketState, ok := d.buckets[durationBlocks]
	if !ok {
		return BucketStateNoMarket
	}

	return marketState
}

// PutMarket adds a new duration market with the given state to the set of
// duration buckets or updates the state of the existing bucket if it already
// exists.
func (d *DurationBuckets) PutMarket(durationBlocks uint32,
	state DurationBucketState) {

	d.Lock()
	defer d.Unlock()

	d.buckets[durationBlocks] = state
}

// ValidDurations returns the set of currently valid durations.
func (d *DurationBuckets) ValidDurations() []uint32 {
	d.RLock()
	defer d.RUnlock()

	durations := make([]uint32, 0, len(d.buckets))
	for duration := range d.buckets {
		durations = append(durations, duration)
	}

	return durations
}

// IterBuckets is a concurrent-safe method that can be used to iterate over the
// current set of active duration markets along with their state.
func (d *DurationBuckets) IterBuckets(f func(durationBlocks uint32,
	marketState DurationBucketState) error) error {

	d.RLock()
	defer d.RUnlock()

	for duration, state := range d.buckets {
		err := f(duration, state)
		if err != nil {
			return err
		}
	}

	return nil
}

// RemoveMarket completely removes the market with the given duration.
func (d *DurationBuckets) RemoveMarket(durationBlocks uint32) {
	d.Lock()
	defer d.Unlock()

	delete(d.buckets, durationBlocks)
}
