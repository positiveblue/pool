package subastadb

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/btcec"
)

// TestBanTrader ensures that we can properly determine a trader's ban status.
func TestBanTrader(t *testing.T) {
	t.Parallel()

	// We'll start the test by initializing our store and some variables
	// we'll use throughout.
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	ctx := context.Background()
	nodeKey := fromHex("03fa26bab2220c62ea5dea00c157c7bd76714fa48d75d1787df7b266a25d30a563")
	accountKey := fromHex("020c80b8d6fa00197fd467943162459aae79e062b53208a05f0fe6cbdea6ca1bd0")
	currentHeight := uint32(100)

	// Accounts and nodes not yet banned should be reflected within the
	// store.
	assertAccountBanStatus(t, store, accountKey, currentHeight, false, 0)
	assertNodeBanStatus(t, store, nodeKey, currentHeight, false, 0)

	// We'll use this closure to group our assertions on the trader's state.
	// When a trader is banned, it should remain banned until the expiration
	// height is reached.
	assertTraderBanStatus := func(expiration uint32) {
		t.Helper()

		beforeExpiration := expiration - 1
		assertAccountBanStatus(
			t, store, accountKey, beforeExpiration, true, expiration,
		)
		assertNodeBanStatus(
			t, store, nodeKey, beforeExpiration, true, expiration,
		)

		assertAccountBanStatus(
			t, store, accountKey, expiration, false, expiration,
		)
		assertNodeBanStatus(
			t, store, nodeKey, expiration, false, expiration,
		)
	}

	// Ban our trader with the keys we created above.
	err := store.BanTrader(ctx, accountKey, nodeKey, currentHeight)
	if err != nil {
		t.Fatalf("unable to ban trader: %v", err)
	}

	// Since this is their first ban, they should only be banned for 144
	// blocks.
	assertTraderBanStatus(currentHeight + initialBanDuration)

	// Ban the trader once again. This is their second ban, so the duration
	// should double.
	err = store.BanTrader(ctx, accountKey, nodeKey, currentHeight)
	if err != nil {
		t.Fatalf("unable to ban trader: %v", err)
	}

	assertTraderBanStatus(currentHeight + (initialBanDuration * 2))

	// Overwrite the bans manually to make sure the admin RPC can interfere
	// if necessary.
	err = store.SetNodeBanInfo(ctx, nodeKey, currentHeight, 4)
	if err != nil {
		t.Fatalf("unable to set node ban info: %v", err)
	}
	err = store.SetAccountBanInfo(ctx, accountKey, currentHeight, 4)
	if err != nil {
		t.Fatalf("unable to set account ban info: %v", err)
	}

	assertTraderBanStatus(currentHeight + 4)

	// Remove the ban for both the account and node, then check they're not
	// banned anymore.
	err = store.RemoveAccountBan(ctx, accountKey)
	if err != nil {
		t.Fatalf("unable to remove account ban: %v", err)
	}
	err = store.RemoveNodeBan(ctx, nodeKey)
	if err != nil {
		t.Fatalf("unable to remove node ban: %v", err)
	}
	assertAccountBanStatus(t, store, accountKey, 0, false, 0)
	assertNodeBanStatus(t, store, nodeKey, 0, false, 0)
}

func assertAccountBanStatus(t *testing.T, store *EtcdStore, // nolint
	key *btcec.PublicKey, height uint32, expBanned bool,
	expExpiration uint32) {

	t.Helper()

	ctx := context.Background()
	banned, expiration, err := store.IsAccountBanned(ctx, key, height)
	if err != nil {
		t.Fatalf("unable to determine account ban: %v", err)
	}
	assertBanStatus(t, expBanned, banned, expExpiration, expiration)

	// Only check that there is a ban entry if we actually expect the
	// account to be banned.
	if !expBanned {
		return
	}
	assertBanInList(
		t, key, height, expBanned, expExpiration,
		store.ListBannedAccounts,
	)
}

func assertNodeBanStatus(t *testing.T, store *EtcdStore, key *btcec.PublicKey, // nolint
	height uint32, expBanned bool, expExpiration uint32) {

	t.Helper()

	ctx := context.Background()
	banned, expiration, err := store.IsNodeBanned(ctx, key, height)
	if err != nil {
		t.Fatalf("unable to determine account ban: %v", err)
	}
	assertBanStatus(t, expBanned, banned, expExpiration, expiration)

	// Only check that there is a ban entry if we actually expect the node
	// to be banned.
	if !expBanned {
		return
	}
	assertBanInList(
		t, key, height, expBanned, expExpiration,
		store.ListBannedNodes,
	)
}

func assertBanInList(t *testing.T, key *btcec.PublicKey, height uint32,
	expBanned bool, expExpiration uint32,
	listFn func(context.Context) (map[[33]byte]*BanInfo, error)) {

	t.Helper()

	var (
		acctKeyRaw [33]byte
		ctx        = context.Background()
	)
	copy(acctKeyRaw[:], key.SerializeCompressed())
	bannedItems, err := listFn(ctx)
	if err != nil {
		t.Fatalf("unable to list banned items: %v", err)
	}
	banInfo, ok := bannedItems[acctKeyRaw]
	if !ok {
		t.Fatalf("did not find entry for banned item %x", acctKeyRaw)
	}
	banned := !banInfo.ExceedsBanExpiration(height)
	expiration := banInfo.Expiration()
	assertBanStatus(t, expBanned, banned, expExpiration, expiration)
}

func assertBanStatus(t *testing.T, expBanned, banned bool, expExpiration,
	expiration uint32) {

	t.Helper()

	switch {
	case expBanned && !banned:
		t.Fatalf("expected banned account")
	case !expBanned && banned:
		t.Fatalf("expected unbanned account")
	case expiration != expExpiration:
		t.Fatalf("expected ban expiration %v, got %v", expExpiration,
			expiration)
	}
}
