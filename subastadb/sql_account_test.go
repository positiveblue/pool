package subastadb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAccountsSQL tests that writing and reading back an account.Account gives
// identical result.
func TestAccountsSQL(t *testing.T) {
	t.Parallel()

	f := NewTestPgFixture(t, fixtureExpiry)
	defer f.TearDown(t)

	f.ClearDB(t)
	store := f.NewSQLStore(t)

	err := store.Transaction(context.TODO(), func(tx *SQLTransaction) error {
		require.NoError(t, tx.UpdateAccount(&testAccount))

		return nil
	})
	require.NoError(t, err)

	traderKey, err := testAccount.TraderKey()
	require.NoError(t, err)

	err = store.Transaction(context.TODO(), func(tx *SQLTransaction) error {
		acc, err := tx.GetAccount(traderKey)
		require.NoError(t, err)
		assertJSONDeepEqual(t, testAccount, acc)

		return nil
	})
	require.NoError(t, err)
}
