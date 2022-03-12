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
	testAmount1 btcutil.Amount = 123
	testAmount2 btcutil.Amount = 34567890
)

// TestStoreTraderTerms makes sure trader specific custom terms can be stored,
// read and deleted correctly.
func TestStoreTraderTerms(t *testing.T) {
	t.Parallel()

	ctxb := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	terms := &traderterms.Custom{
		TraderID: lsat.TokenID{1, 2, 3, 4, 5, 6},
		BaseFee:  &testAmount1,
	}

	// Try reading a non-existent entry.
	_, err := store.GetTraderTerms(ctxb, terms.TraderID)
	require.Error(t, err)
	require.Equal(t, ErrNoTerms, err)

	// Store the terms entry then read it again.
	require.NoError(t, store.PutTraderTerms(ctxb, terms))
	dbTerms, err := store.GetTraderTerms(ctxb, terms.TraderID)
	require.NoError(t, err)
	require.Equal(t, terms, dbTerms)

	// Store a second entry.
	terms2 := &traderterms.Custom{
		TraderID: lsat.TokenID{9, 8, 7, 6, 5, 4, 3, 2},
		BaseFee:  &testAmount1,
		FeeRate:  &testAmount2,
	}
	require.NoError(t, store.PutTraderTerms(ctxb, terms2))

	// Load all terms.
	allDbTerms, err := store.AllTraderTerms(ctxb)
	require.NoError(t, err)
	require.Len(t, allDbTerms, 2)
	require.Contains(t, allDbTerms, terms)
	require.Contains(t, allDbTerms, terms2)

	// Delete both terms.
	require.NoError(t, store.DelTraderTerms(ctxb, terms.TraderID))
	require.NoError(t, store.DelTraderTerms(ctxb, terms2.TraderID))

	// Store should be empty now.
	allDbTerms, err = store.AllTraderTerms(ctxb)
	require.NoError(t, err)
	require.Len(t, allDbTerms, 0)
}
