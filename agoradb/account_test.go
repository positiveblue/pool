package agoradb

import (
	"context"
	"reflect"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/loop/lsat"
)

var (
	testTokenID       = lsat.TokenID{1, 2, 3}
	testAccountSubKey = [33]byte{4, 5, 6}
	testAccount       = account.Account{
		TokenID:       testTokenID,
		Value:         1337,
		Expiry:        100,
		TraderKey:     [33]byte{7, 8, 9},
		AuctioneerKey: [33]byte{10, 11, 12},
		State:         account.StateOpen,
		HeightHint:    100,
		OutPoint:      wire.OutPoint{Index: 1},
	}
)

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
	err = store.ReserveAccount(ctx, testTokenID, testAccountSubKey)
	if err != nil {
		t.Fatalf("unable to reserve account: %v", err)
	}
	accountSubKey, err := store.HasReservation(ctx, testTokenID)
	if err != nil {
		t.Fatalf("unable to determine existing reservation: %v", err)
	}
	if accountSubKey != testAccountSubKey {
		t.Fatalf("expected account subkey %x, got %x",
			testAccountSubKey, accountSubKey)
	}

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
	t.Parallel()

	ctx := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// Start by adding a new account.
	a := testAccount
	err := store.ReserveAccount(ctx, testTokenID, testAccountSubKey)
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
