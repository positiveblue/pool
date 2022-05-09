package subastadb

import (
	"context"
	"testing"
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/stretchr/testify/require"
)

const (
	fixtureExpiry = 120 * time.Second
)

// TestOrdersSQL tests that writing and reading back order.Bid and order.Ask
// gives identical results.
func TestOrdersSQL(t *testing.T) {
	t.Parallel()

	f := NewTestPgFixture(t, fixtureExpiry)
	defer f.TearDown(t)

	f.ClearDB(t)
	store := f.NewSQLGORMStore(t)

	testBid := &order.Bid{
		Bid: orderT.Bid{
			Kit:             *dummyClientOrder(t, 500000, 1337),
			MinNodeTier:     9,
			SelfChanBalance: 12345,
		},
		Kit:       *dummyOrder(t),
		IsSidecar: true,
	}

	testAsk := &order.Ask{
		Ask: orderT.Ask{
			Kit: *dummyClientOrder(t, 500000, 1337),
		},
		Kit: *dummyOrder(t),
	}

	err := store.Transaction(context.TODO(), func(tx *SQLTransaction) error {
		require.NoError(t, tx.UpdateOrder(testBid))
		require.NoError(t, tx.UpdateOrder(testAsk))

		return nil
	})
	require.NoError(t, err)

	err = store.Transaction(context.TODO(), func(tx *SQLTransaction) error {
		ask, err := tx.GetAsk(testAsk.Nonce())
		require.NoError(t, err)
		assertJSONDeepEqual(t, testAsk, ask)

		bid, err := tx.GetBid(testBid.Nonce())
		require.NoError(t, err)
		assertJSONDeepEqual(t, testBid, bid)

		return nil
	})
	require.NoError(t, err)
}
