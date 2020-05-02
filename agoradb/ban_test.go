package agoradb

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/btcec"
)

// TestBanTrader ensures that we can properly determine a trader's ban status.
func TestBanTrader(t *testing.T) {
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
}

func assertAccountBanStatus(t *testing.T, store *EtcdStore,
	key *btcec.PublicKey, height uint32, expBanned bool,
	expExpiration uint32) {

	t.Helper()

	ctx := context.Background()
	banned, expiration, err := store.IsAccountBanned(ctx, key, height)
	if err != nil {
		t.Fatalf("unable to determine account ban: %v", err)
	}
	assertBanStatus(t, expBanned, banned, expExpiration, expiration)
}

func assertNodeBanStatus(t *testing.T, store *EtcdStore, key *btcec.PublicKey,
	height uint32, expBanned bool, expExpiration uint32) {

	t.Helper()

	ctx := context.Background()
	banned, expiration, err := store.IsNodeBanned(ctx, key, height)
	if err != nil {
		t.Fatalf("unable to determine account ban: %v", err)
	}
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
