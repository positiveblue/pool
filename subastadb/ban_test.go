package subastadb

import (
	"context"
	"testing"

	"github.com/lightninglabs/subasta/ban"
	"github.com/stretchr/testify/require"
)

// TestBanTrader ensures that we can properly determine a trader's ban status.
func TestBanTrader(t *testing.T) {
	t.Parallel()

	// We'll start the test by initializing our store and some variables
	// we'll use throughout.
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	nodeKey := fromHex("03fa26bab2220c62ea5dea00c157c7bd76714fa48d75d1787df7b266a25d30a563")
	accountKey := fromHex("020c80b8d6fa00197fd467943162459aae79e062b53208a05f0fe6cbdea6ca1bd0")
	currentHeight := uint32(100)

	// Check that there are no bans for the current account/node keys.
	banInfo, err := store.GetAccountBan(ctx, accountKey, currentHeight)
	if err != nil {
		t.Fatalf("unable to get account ban: %v", err)
	}
	require.Nil(t, banInfo)

	banInfo, err = store.GetNodeBan(ctx, nodeKey, currentHeight)
	if err != nil {
		t.Fatalf("unable to get node ban: %v", err)
	}
	require.Nil(t, banInfo)

	// Add bans for the keys we created above.
	banDuration := uint32(144)
	newBanInfo := &ban.Info{Height: currentHeight, Duration: banDuration}

	if err := store.BanAccount(ctx, accountKey, newBanInfo); err != nil {
		t.Fatalf("unable to ban trader account: %v", err)
	}

	if err := store.BanNode(ctx, nodeKey, newBanInfo); err != nil {
		t.Fatalf("unable to ban trader node: %v", err)
	}

	// Check that the bans where stored properly.
	banInfo, err = store.GetAccountBan(ctx, accountKey, currentHeight)
	if err != nil {
		t.Fatalf("unable to get account ban: %v", err)
	}
	require.Equal(t, newBanInfo, banInfo)

	banInfo, err = store.GetNodeBan(ctx, nodeKey, currentHeight)
	if err != nil {
		t.Fatalf("unable to get node ban: %v", err)
	}
	require.Equal(t, newBanInfo, banInfo)

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

	// Check that the bans where removed.
	banInfo, err = store.GetAccountBan(ctx, accountKey, currentHeight)
	if err != nil {
		t.Fatalf("unable to get account ban: %v", err)
	}
	require.Nil(t, banInfo)

	banInfo, err = store.GetNodeBan(ctx, nodeKey, currentHeight)
	if err != nil {
		t.Fatalf("unable to get node ban: %v", err)
	}
	require.Nil(t, banInfo)
}
