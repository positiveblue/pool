package subastadb

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/wire"
	"github.com/davecgh/go-spew/spew"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/stretchr/testify/require"
)

// TestFetchUpdateAuctioneerAccount tests that we're able to retrieve and
// update the auctioneer's account state on disk.
func TestFetchUpdateAuctioneerAccount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, cleanup := newTestStore(t)
	defer cleanup()

	acct := &account.Auctioneer{
		OutPoint: wire.OutPoint{
			Index: 5,
		},
		Balance:       1_000_000,
		AuctioneerKey: testAuctioneerKeyDesc,
	}
	copy(acct.BatchKey[:], testRawTraderKey)

	err := store.UpdateAuctioneerAccount(ctx, acct)
	if err != nil {
		t.Fatalf("unable to update auctioneer account: %v", err)
	}

	diskAcct, err := store.FetchAuctioneerAccount(ctx)
	if err != nil {
		t.Fatalf("unable to fetch acct from disk; %v", err)
	}

	if !reflect.DeepEqual(acct, diskAcct) {
		t.Fatalf("acct mismatch: expected %v got %v",
			spew.Sdump(acct), spew.Sdump(diskAcct))
	}
}

// TestFetchAuctioneerBalances tests that we're able to retrieve the auctioneer
// balance at different points in time.
func TestFetchAuctioneerBalances(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, cleanup := newTestStore(t)
	defer cleanup()

	sqlStore, ok := store.(*SQLStore)
	require.True(t, ok)

	// We cannot create an auctioneer snapshot without inserting the
	// related batch first.
	// persistBatch emulates the store.PersistBatch behaviour.
	persistBatch := func(acct *account.Auctioneer) {
		time.Sleep(time.Second)
		batch := &matching.BatchSnapshot{
			BatchTx:    wire.NewMsgTx(2),
			BatchTxFee: 0,
			OrderBatch: matching.EmptyBatch(orderT.DefaultBatchVersion),
		}
		err := sqlStore.StoreBatch(ctx, acct.BatchKey, batch, true)
		require.NoError(t, err)

		err = sqlStore.CreateAuctioneerSnapshot(ctx, acct)
		require.NoError(t, err)
		time.Sleep(time.Second)
	}

	// Store a snapshot with balance 1 000 000.
	currentBatch := InitialBatchKey
	initialBalance := btcutil.Amount(1_000_000)
	acct := &account.Auctioneer{
		OutPoint: wire.OutPoint{
			Index: 5,
		},
		Balance:       initialBalance,
		AuctioneerKey: testAuctioneerKeyDesc,
	}
	copy(acct.BatchKey[:], currentBatch.SerializeCompressed())

	persistBatch(acct)

	// Record the time right after the first update.
	t1 := time.Now()

	// Store a snapshot with balance 2 000 000.
	currentBatch = poolscript.IncrementKey(currentBatch)
	copy(acct.BatchKey[:], currentBatch.SerializeCompressed())
	acct.Balance = btcutil.Amount(2_000_000)

	persistBatch(acct)

	// Record the time after the second update.
	t2 := time.Now()

	// The balance right after the first update should be the initial
	// balance. We call the underlying method so we can compare the
	// batch key too.
	params := sql.NullTime{
		Time:  t1,
		Valid: true,
	}
	batch, err := sqlStore.queries.GetAuctioneerSnapshotByDate(ctx, params)
	require.NoError(t, err)
	require.Equal(t, int64(initialBalance), batch.Balance)
	require.Equal(t, InitialBatchKey.SerializeCompressed(), batch.BatchKey)

	// The balance right after the second update should be the updated
	// balance.
	amt, err := store.GetAuctioneerBalance(ctx, t2)
	require.NoError(t, err)
	require.Equal(t, acct.Balance, amt)
}
