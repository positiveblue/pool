package agoradb

import (
	"context"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/keychain"
)

var (
	testRawAuctioneerKey, _ = hex.DecodeString("02187d1a0e30f4e5016fc1137363ee9e7ed5dde1e6c50f367422336df7a108b716")
	testAuctioneerKey, _    = btcec.ParsePubKey(testRawAuctioneerKey, btcec.S256())
	testAuctioneerKeyDesc   = &keychain.KeyDescriptor{
		KeyLocator: account.LongTermKeyLocator,
		PubKey:     testAuctioneerKey,
	}

	testRawTraderKey, _ = hex.DecodeString("036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0af58e0c9395446ba09")
	testTraderKey, _    = btcec.ParsePubKey(testRawTraderKey, btcec.S256())

	testReservation = account.Reservation{
		AuctioneerKey:   testAuctioneerKeyDesc,
		InitialBatchKey: initialBatchKey,
	}

	testTokenID = lsat.TokenID{1, 2, 3}

	testAccount = account.Account{
		TokenID:       testTokenID,
		Value:         1337,
		Expiry:        100,
		TraderKey:     testTraderKey,
		AuctioneerKey: testAuctioneerKeyDesc,
		State:         account.StateOpen,
		BatchKey:      initialBatchKey,
		Secret:        [32]byte{0x73, 0x65, 0x63, 0x72, 0x65, 0x74},
		HeightHint:    100,
		OutPoint:      wire.OutPoint{Index: 1},
	}
)

func assertEqualReservation(t *testing.T, exp, got *account.Reservation) {
	t.Helper()

	if got.AuctioneerKey.KeyLocator != exp.AuctioneerKey.KeyLocator {
		t.Fatalf("expected auctioneer key locator: %v\ngot: %v",
			spew.Sdump(exp.AuctioneerKey.KeyLocator),
			spew.Sdump(got.AuctioneerKey.KeyLocator))
	}
	if !got.AuctioneerKey.PubKey.IsEqual(exp.AuctioneerKey.PubKey) {
		t.Fatalf("expected auctioneer key %x, got %x",
			exp.AuctioneerKey.PubKey.SerializeCompressed(),
			got.AuctioneerKey.PubKey.SerializeCompressed())
	}
	if !got.InitialBatchKey.IsEqual(exp.InitialBatchKey) {
		t.Fatalf("expected initial batch key %x, got %x",
			exp.InitialBatchKey.SerializeCompressed(),
			got.InitialBatchKey.SerializeCompressed())
	}
}

// TestAccountReservation ensures that the account manager properly honors
// account reservations.
func TestAccountReservation(t *testing.T) {
	ctx := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// A reservation hasn't been created yet, so we shouldn't find one.
	_, err := store.HasReservation(ctx, testTokenID)
	if err != account.ErrNoReservation {
		t.Fatalf("expected ErrNoReservation, got \"%v\"", err)
	}

	// Make a new reservation for the user with the associated token.
	reservation := testReservation
	err = store.ReserveAccount(ctx, testTokenID, &reservation)
	if err != nil {
		t.Fatalf("unable to reserve account: %v", err)
	}
	storedReservation, err := store.HasReservation(ctx, testTokenID)
	if err != nil {
		t.Fatalf("unable to determine existing reservation: %v", err)
	}
	assertEqualReservation(t, &reservation, storedReservation)

	// Complete the reservation with an account. The reservation should no
	// longer exist after this.
	if err := store.CompleteReservation(ctx, &testAccount); err != nil {
		t.Fatalf("unable to complete reservation: %v", err)
	}
	_, err = store.HasReservation(ctx, testTokenID)
	if err != account.ErrNoReservation {
		t.Fatalf("expected ErrNoReservation, got \"%v\"", err)
	}

	account, err := store.Account(ctx, testAccount.TraderKey)
	if err != nil {
		t.Fatalf("unable to retrieve account: %v", err)
	}
	if !reflect.DeepEqual(account, &testAccount) {
		t.Fatalf("expected account: %v\ngot: %v",
			spew.Sdump(testAccount), spew.Sdump(account))
	}
}

// TestAccounts ensures we can properly add, update, and retrieve accounts from
// the store.
func TestAccounts(t *testing.T) {
	ctx := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// Start by adding a new account.
	a := testAccount
	reservation := testReservation
	err := store.ReserveAccount(ctx, testTokenID, &reservation)
	if err != nil {
		t.Fatalf("unable to reserve account: %v", err)
	}
	if err := store.CompleteReservation(ctx, &a); err != nil {
		t.Fatalf("unable to complete reservation: %v", err)
	}

	// Ensure it exists within the store.
	accounts, err := store.Accounts(ctx)
	if err != nil {
		t.Fatalf("unable to retrieve accounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, found %d", len(accounts))
	}
	if !reflect.DeepEqual(accounts[0], &a) {
		t.Fatalf("expected account: %v\ngot: %v",
			spew.Sdump(a), spew.Sdump(accounts[0]))
	}

	// Update some of the fields of the account.
	err = store.UpdateAccount(
		ctx, &a, account.StateModifier(account.StateExpired),
	)
	if err != nil {
		t.Fatalf("unable to update account: %v", err)
	}

	// The store should now reflect the updated fields.
	accounts, err = store.Accounts(ctx)
	if err != nil {
		t.Fatalf("unable to retrieve accounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, found %d", len(accounts))
	}
	if !reflect.DeepEqual(accounts[0], &a) {
		t.Fatalf("expected account: %v\ngot: %v",
			spew.Sdump(a), spew.Sdump(accounts[0]))
	}
}
