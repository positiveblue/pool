package subastadb

import (
	"bytes"
	"context"
	"sort"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/stretchr/testify/require"
)

func sortBatchOrders(batch *BatchSnapshot) {
	// We need some kind of canonical ordering to ensure deterministic
	// object comparison in tests.
	sort.Slice(batch.OrderBatch.Orders, func(i, j int) bool {
		p1 := batch.OrderBatch.Orders[i].Details.Ask.Preimage[:]
		p2 := batch.OrderBatch.Orders[j].Details.Ask.Preimage[:]

		return bytes.Compare(p1, p2) < 0
	})

	subBatches := batch.OrderBatch.SubBatches
	for duration := range subBatches {
		// We can do this since this is a slice.
		orders := subBatches[duration]

		sort.Slice(orders, func(i, j int) bool {
			p1 := orders[i].Details.Ask.Preimage[:]
			p2 := orders[j].Details.Ask.Preimage[:]

			return bytes.Compare(p1, p2) < 0
		})
	}
}

// TestBatchesSQL tests that writing and reading back batches gives identical
// results.
func TestBatchesSQL(t *testing.T) {
	t.Parallel()

	f := NewTestPgFixture(t, fixtureExpiry)
	defer f.TearDown(t)

	f.ClearDB(t)
	store := f.NewSQLStore(t)

	batches := makeTestOrderBatches(context.TODO(), t, nil)
	// Postgres doesn't have nanosec timestamps, so we truncate our test
	// timestamps to microsec here.
	batches[0].CreationTimestamp = batches[0].CreationTimestamp.Truncate(time.Microsecond)
	batches[1].CreationTimestamp = batches[1].CreationTimestamp.Truncate(time.Microsecond)

	batchID1 := orderT.BatchID{1, 2, 3}
	batchSnapshot1 := &BatchSnapshot{
		BatchTx:    batchTx,
		BatchTxFee: btcutil.Amount(911),
		OrderBatch: batches[0],
	}

	batchID2 := orderT.BatchID{4, 5, 6}
	batchSnapshot2 := &BatchSnapshot{
		BatchTx:    batchTx,
		BatchTxFee: btcutil.Amount(1000),
		OrderBatch: batches[1],
	}

	sortBatchOrders(batchSnapshot1)
	sortBatchOrders(batchSnapshot2)
	err := store.Transaction(context.TODO(), func(tx *SQLTransaction) error {
		for i := range []int{0, 1} {
			for _, matchedOrder := range batches[i].Orders {
				require.NoError(
					t, tx.UpdateOrder(
						matchedOrder.Details.Ask,
					),
				)
				require.NoError(
					t, tx.UpdateOrder(
						matchedOrder.Details.Bid,
					),
				)
			}
		}

		require.NoError(t, tx.UpdateBatch(batchID1, batchSnapshot1))
		require.NoError(t, tx.UpdateBatch(batchID2, batchSnapshot2))

		return nil
	})
	require.NoError(t, err)

	err = store.Transaction(context.TODO(), func(tx *SQLTransaction) error {
		dbBatchSnapshot1, err := tx.GetBatch(batchID1)
		require.NoError(t, err)
		sortBatchOrders(dbBatchSnapshot1)
		assertJSONDeepEqual(t, batchSnapshot1, dbBatchSnapshot1)

		dbBatchSnapshot2, err := tx.GetBatch(batchID2)
		require.NoError(t, err)
		sortBatchOrders(dbBatchSnapshot2)
		assertJSONDeepEqual(t, batchSnapshot2, dbBatchSnapshot2)

		return nil
	})
	require.NoError(t, err)
}
