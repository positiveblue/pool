package agoradb

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/client/clmscript"
	orderT "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
)

// TestBatchKey tests the different database operations that can be performed on
// the per-batch key.
func TestBatchKey(t *testing.T) {
	ctx := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// The store should be initialized with the initial batch key.
	batchKey, err := store.BatchKey(ctx)
	if err != nil {
		t.Fatalf("unable to retrieve initial batch key: %v", err)
	}
	if !batchKey.IsEqual(initialBatchKey) {
		t.Fatalf("expected initial batch key %x, got %x",
			initialBatchKey.SerializeCompressed(),
			batchKey.SerializeCompressed())
	}

	// Increment the initial batch key by its curve's base point. We should
	// expect to see the new key in the store.
	nextBatchKey, err := store.NextBatchKey(ctx)
	if err != nil {
		t.Fatalf("unable to update current batch key: %v", err)
	}
	storedBatchKey, err := store.BatchKey(ctx)
	if err != nil {
		t.Fatalf("unable to retrieve current batch key: %v", err)
	}
	if !storedBatchKey.IsEqual(nextBatchKey) {
		t.Fatalf("expected updated batch key %x, got %x",
			nextBatchKey.SerializeCompressed(),
			storedBatchKey.SerializeCompressed())
	}

	expectedNextBatchKey := clmscript.IncrementKey(initialBatchKey)
	if !nextBatchKey.IsEqual(expectedNextBatchKey) {
		t.Fatalf("expected updated batch key %x, got %x",
			expectedNextBatchKey.SerializeCompressed(),
			nextBatchKey.SerializeCompressed())
	}
}

// TestPersistBatchResult tests the different database operations that are
// performed during the persisting phase of a batch.
func TestPersistBatchResult(t *testing.T) {
	ctx := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// First test the basic sanity tests that are performed.
	err := store.PersistBatchResult(
		ctx, []orderT.Nonce{{}}, nil, nil, nil, nil, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "order modifier") {
		t.Fatalf("expected order modifier length mismatch, got %v", err)
	}
	err = store.PersistBatchResult(
		ctx, nil, nil, []*btcec.PublicKey{{}}, nil, nil, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "account modifier") {
		t.Fatalf("expected account modifier length mismatch, got %v",
			err)
	}

	// Now prepare some test data that we are going to operate on. Begin
	// with an account.
	a1 := testAccount
	traderKey, _ := testAccount.TraderKey()
	reservation := testReservation
	err = store.ReserveAccount(ctx, testTokenID, &reservation)
	if err != nil {
		t.Fatalf("unable to reserve account: %v", err)
	}
	if err := store.CompleteReservation(ctx, &a1); err != nil {
		t.Fatalf("unable to complete reservation: %v", err)
	}

	// Now store a test order.
	o1 := &order.Bid{
		Bid: orderT.Bid{
			Kit:         *dummyClientOrder(t, 500000),
			MinDuration: 1337,
		},
		Kit: *dummyOrder(t),
	}
	err = store.SubmitOrder(ctx, o1)
	if err != nil {
		t.Fatalf("unable to store order: %v", err)
	}

	// And then last, store a dummy master account.
	ma1 := &account.Auctioneer{
		OutPoint: wire.OutPoint{
			Index: 5,
		},
		Balance:       1_000_000,
		AuctioneerKey: testAuctioneerKeyDesc,
	}
	copy(ma1.BatchKey[:], initialBatchKey.SerializeCompressed())
	err = store.UpdateAuctioneerAccount(ctx, ma1)
	if err != nil {
		t.Fatalf("unable to update auctioneer account: %v", err)
	}

	// Now prepare the updates we are going to apply.
	orderModifiers := [][]order.Modifier{
		{order.StateModifier(orderT.StatePartiallyFilled)},
	}
	accountModifiers := [][]account.Modifier{
		{account.StateModifier(account.StateClosed)},
	}
	ma1.Balance = 500_000

	// Then call the actual persist method on the store.
	err = store.PersistBatchResult(
		ctx, []orderT.Nonce{o1.Nonce()}, orderModifiers,
		[]*btcec.PublicKey{traderKey}, accountModifiers,
		ma1, testTraderKey,
	)
	if err != nil {
		t.Fatalf("error persisting batch result: %v", err)
	}

	// And finally validate that all changes have been written correctly.
	// Start with the account.
	a2, err := store.Account(ctx, traderKey)
	if err != nil {
		t.Fatalf("error getting account: %v", err)
	}
	if a2.State != account.StateClosed {
		t.Fatalf("unexpected account state, got %d wanted %d",
			a2.State, account.StateClosed)
	}

	// Next check the order.
	o2, err := store.GetOrder(ctx, o1.Nonce())
	if err != nil {
		t.Fatalf("error getting order: %v", err)
	}
	if o2.Details().State != orderT.StatePartiallyFilled {
		t.Fatalf("unexpected order state, got %d wanted %d",
			o2.Details().State, orderT.StatePartiallyFilled)
	}

	// And finally the auctioneer/master account and the batch key.
	ma2, err := store.FetchAuctioneerAccount(ctx)
	if err != nil {
		t.Fatalf("error getting master account: %v", err)
	}
	if ma2.Balance != 500_000 {
		t.Fatalf("unexpected master account balance, got %d wanted %d",
			ma2.Balance, 500_000)
	}
	if !bytes.Equal(ma2.BatchKey[:], testTraderKey.SerializeCompressed()) {
		t.Fatalf("unexpected batch key, got %x wanted %x", ma2.BatchKey,
			testTraderKey.SerializeCompressed())
	}
}

// TestPersistBatchResultRollback tests that the persisting operation of a batch
// is executed atomically and everything is rolled back if one operation fails.
func TestPersistBatchResultRollback(t *testing.T) {
	ctx := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// Create a test order that we are going to try to modify.
	o1 := &order.Bid{
		Bid: orderT.Bid{
			Kit:         *dummyClientOrder(t, 500000),
			MinDuration: 1337,
		},
		Kit: *dummyOrder(t),
	}
	err := store.SubmitOrder(ctx, o1)
	if err != nil {
		t.Fatalf("unable to store order: %v", err)
	}

	// We also need a master account, for completion's sake.
	ma1 := &account.Auctioneer{
		OutPoint: wire.OutPoint{
			Index: 5,
		},
		Balance:       1_000_000,
		AuctioneerKey: testAuctioneerKeyDesc,
	}
	copy(ma1.BatchKey[:], initialBatchKey.SerializeCompressed())
	err = store.UpdateAuctioneerAccount(ctx, ma1)
	if err != nil {
		t.Fatalf("unable to update auctioneer account: %v", err)
	}

	// Now prepare the updates we are going to try to apply.
	orderModifiers := [][]order.Modifier{
		{order.StateModifier(orderT.StatePartiallyFilled)},
	}
	accountModifiers := [][]account.Modifier{
		{account.StateModifier(account.StateClosed)},
	}
	ma1.Balance = 500_000

	// Then call the actual persist method on the store. We try to apply
	// account modifications to an account that does not exist. This should
	// result in an error and roll back all order modifications.
	traderKey, _ := testAccount.TraderKey()
	err = store.PersistBatchResult(
		ctx, []orderT.Nonce{o1.Nonce()}, orderModifiers,
		[]*btcec.PublicKey{traderKey}, accountModifiers,
		ma1, testTraderKey,
	)
	if err == nil {
		t.Fatal("expected error persisting batch result, got nil")
	}

	// Validate that the order has not in fact been modified.
	o2, err := store.GetOrder(ctx, o1.Nonce())
	if err != nil {
		t.Fatalf("error getting order: %v", err)
	}
	if o2.Details().State != orderT.StateExecuted {
		t.Fatalf("unexpected order state, got %d wanted %d",
			o2.Details().State, orderT.StateExecuted)
	}

	// And finally the auctioneer/master account and the batch key.
	ma2, err := store.FetchAuctioneerAccount(ctx)
	if err != nil {
		t.Fatalf("error getting master account: %v", err)
	}
	if ma2.Balance != 1_000_000 {
		t.Fatalf("unexpected master account balance, got %d wanted %d",
			ma2.Balance, 1_000_000)
	}
	if !bytes.Equal(ma2.BatchKey[:], initialBatchKey.SerializeCompressed()) {
		t.Fatalf("unexpected batch key, got %x wanted %x", ma2.BatchKey,
			initialBatchKey.SerializeCompressed())
	}
}
