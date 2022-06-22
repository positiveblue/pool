package subastadb

import (
	"context"
	"fmt"

	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb/postgres"
)

// StoreLeaseDuration persists to disk the given lease duration bucket.
func (s *SQLStore) StoreLeaseDuration(ctx context.Context, duration uint32,
	marketState order.DurationBucketState) error {

	params := postgres.UpsertLeaseDurationParams{
		Duration: int64(duration),
		State:    int16(marketState),
	}
	return s.queries.UpsertLeaseDuration(ctx, params)
}

// LeaseDurations retrieves all lease duration buckets.
func (s *SQLStore) LeaseDurations(
	ctx context.Context) (map[uint32]order.DurationBucketState, error) {

	params := postgres.GetLeaseDurationsParams{}
	rows, err := s.queries.GetLeaseDurations(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("unable to get lease durations: %v", err)
	}

	leasesDurations := make(map[uint32]order.DurationBucketState, len(rows))
	for _, row := range rows {
		duration := uint32(row.Duration)
		state := order.DurationBucketState(row.State)
		leasesDurations[duration] = state
	}

	return leasesDurations, nil
}

// RemoveLeaseDuration removes a single lease duration bucket from the
// database.
func (s *SQLStore) RemoveLeaseDuration(ctx context.Context,
	duration uint32) error {

	_, err := s.queries.DeleteLeaseDuration(ctx, int64(duration))
	if err != nil {
		return fmt.Errorf("unable to delete lease duration: %v", err)
	}

	return nil
}
