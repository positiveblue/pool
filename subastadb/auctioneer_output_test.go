package subastadb

import (
	"context"
	"reflect"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/subasta/account"
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
