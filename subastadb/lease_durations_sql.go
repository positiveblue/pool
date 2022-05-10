package subastadb

import (
	"context"

	"github.com/lightninglabs/subasta/order"
)

// StoreLeaseDuration persists to disk the given lease duration bucket.
func (s *SQLStore) StoreLeaseDuration(ctx context.Context, duration uint32,
	marketState order.DurationBucketState) error {

	return ErrNotImplemented
}

// LeaseDurations retrieves all lease duration buckets.
func (s *SQLStore) LeaseDurations(
	ctx context.Context) (map[uint32]order.DurationBucketState, error) {

	return nil, ErrNotImplemented
}

// RemoveLeaseDuration removes a single lease duration bucket from the
// database.
func (s *SQLStore) RemoveLeaseDuration(ctx context.Context,
	duration uint32) error {

	return ErrNotImplemented
}
