package subastadb

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/subasta/traderterms"
	"github.com/stretchr/testify/require"
)

var (
	testBaseFee btcutil.Amount = 1337
	testFeeRate btcutil.Amount = 1234
)

// TestOrdersSQL tests that writing and reading back order.Bid and order.Ask
// gives identical results.
func TestTraderTermsSQL(t *testing.T) {
	t.Parallel()

	f := NewTestPgFixture(t, fixtureExpiry)
	defer f.TearDown(t)

	f.ClearDB(t)
	store := f.NewSQLStore(t)

	testTermsEmpty := &traderterms.Custom{
		TraderID: lsat.TokenID{},
	}
	testTermsFull := &traderterms.Custom{
		TraderID: lsat.TokenID{1, 2, 3, 4, 5, 6},
		BaseFee:  &testBaseFee,
		FeeRate:  &testFeeRate,
	}

	err := store.Transaction(
		context.Background(), func(tx *SQLTransaction) error {
			require.NoError(t, tx.UpdateTraderTerms(testTermsEmpty))
			require.NoError(t, tx.UpdateTraderTerms(testTermsFull))

			return nil
		},
	)
	require.NoError(t, err)

	err = store.Transaction(
		context.Background(), func(tx *SQLTransaction) error {
			t1, err := tx.GetTraderTerms(testTermsEmpty.TraderID)
			require.NoError(t, err)
			assertJSONDeepEqual(t, testTermsEmpty, t1)

			t2, err := tx.GetTraderTerms(testTermsFull.TraderID)
			require.NoError(t, err)
			assertJSONDeepEqual(t, testTermsFull, t2)

			return nil
		},
	)
	require.NoError(t, err)
}
