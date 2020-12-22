package subastadb

import (
	"context"
	"testing"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/stretchr/testify/require"
)

const (
	testDuration = 1440
)

// TestLeaseDurations ensures that we are able to perform the different
// operations available for lease duration buckets.
func TestLeaseDurations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// We'll use a helper closure to correctly assert the state of our store
	// with respect to the lease duration.
	assertDurationInStore := func(numExpected int, duration uint32,
		state order.DurationBucketState) {

		t.Helper()

		durations, err := store.LeaseDurations(ctx)
		if err != nil {
			t.Fatalf("unable to retrieve lease duration buckets: "+
				"%v", err)
		}

		require.Len(t, durations, numExpected)

		if numExpected > 0 {
			require.Equal(t, durations[duration], state)
		}
	}

	// After the DB is initialized, we expect the default value to be added.
	assertDurationInStore(
		1, orderT.LegacyLeaseDurationBucket,
		order.BucketStateClearingMarket,
	)

	// We'll then continue by storing a sample lease duration.
	err := store.StoreLeaseDuration(
		ctx, testDuration, order.BucketStateClearingMarket,
	)
	require.NoError(t, err)

	// We should be able to retrieve it from store.
	assertDurationInStore(2, testDuration, order.BucketStateClearingMarket)

	// We should also be able to remove it.
	require.NoError(t, store.RemoveLeaseDuration(ctx, testDuration))
	assertDurationInStore(
		1, orderT.LegacyLeaseDurationBucket,
		order.BucketStateClearingMarket,
	)

	// We'll then store the package again with a different state.
	err = store.StoreLeaseDuration(
		ctx, testDuration, order.BucketStateAcceptingOrders,
	)
	require.NoError(t, err)
	assertDurationInStore(2, testDuration, order.BucketStateAcceptingOrders)
}
