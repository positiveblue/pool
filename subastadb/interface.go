package subastadb

import (
	"context"

	"github.com/lightninglabs/subasta/order"
)

// LeaseDurationStore is responsible for storing and retrieving information
// about lease durations reliably.
type LeaseDurationStore interface {
	// StoreLeaseDuration persists to disk the given lease duration bucket.
	StoreLeaseDuration(ctx context.Context, duration uint32,
		marketState order.DurationBucketState) error

	// LeaseDurations retrieves all lease duration buckets.
	LeaseDurations(ctx context.Context) (
		map[uint32]order.DurationBucketState, error)

	// RemoveLeaseDuration removes a single lease duration bucket from the
	// database.
	RemoveLeaseDuration(ctx context.Context, duration uint32) error
}
