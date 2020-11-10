package subastadb

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightningnetwork/lnd/keychain"
)

var (
	testRawAuctioneerKey, _ = hex.DecodeString("02187d1a0e30f4e5016fc1137363ee9e7ed5dde1e6c50f367422336df7a108b716")
	testAuctioneerKey, _    = btcec.ParsePubKey(testRawAuctioneerKey, btcec.S256())
	testAuctioneerKeyDesc   = &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: account.AuctioneerKeyFamily,
		},
		PubKey: testAuctioneerKey,
	}

	testRawTraderKey, _ = hex.DecodeString("036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0af58e0c9395446ba09")
	testTraderKey, _    = btcec.ParsePubKey(testRawTraderKey, btcec.S256())

	testReservation = account.Reservation{
		Value:           1337,
		AuctioneerKey:   testAuctioneerKeyDesc,
		InitialBatchKey: InitialBatchKey,
		TraderKeyRaw:    toRawKey(testTraderKey),
		Expiry:          100,
		HeightHint:      12345,
	}

	testTokenID = lsat.TokenID{1, 2, 3}

	testAccount = account.Account{
		TokenID:       testTokenID,
		Value:         1337,
		Expiry:        100,
		TraderKeyRaw:  toRawKey(testTraderKey),
		AuctioneerKey: testAuctioneerKeyDesc,
		State:         account.StateOpen,
		BatchKey:      InitialBatchKey,
		Secret:        [32]byte{0x73, 0x65, 0x63, 0x72, 0x65, 0x74},
		HeightHint:    100,
		OutPoint:      wire.OutPoint{Index: 1},
		LatestTx: &wire.MsgTx{
			Version: 2,
			TxIn: []*wire.TxIn{
				{
					PreviousOutPoint: wire.OutPoint{
						Index: 1,
					},
					SignatureScript: []byte{0x40},
				},
			},
			TxOut: []*wire.TxOut{
				{
					PkScript: []byte{0x40},
					Value:    1_000,
				},
			},
		},
	}
)

func assertEqualReservation(t *testing.T, exp, got *account.Reservation) {
	t.Helper()

	if got.Value != exp.Value {
		t.Fatalf("expected value %d, got %d", exp.Value, got.Value)
	}
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
	if got.TraderKeyRaw != exp.TraderKeyRaw {
		t.Fatalf("expected trader key %x, got %x", exp.TraderKeyRaw,
			got.TraderKeyRaw)
	}
	if got.Expiry != exp.Expiry {
		t.Fatalf("expected expiry %d, got %d", exp.Expiry, got.Expiry)
	}
	if got.HeightHint != exp.HeightHint {
		t.Fatalf("expected height hint %d, got %d", exp.HeightHint,
			got.HeightHint)
	}
}

// TestAccountReservation ensures that the account manager properly honors
// account reservations.
func TestAccountReservation(t *testing.T) {
	t.Parallel()

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

	// A reservation should also be found with the trader key in case it
	// needs to be recovered.
	storedReservation, storedToken, err := store.HasReservationForKey(
		ctx, testTraderKey,
	)
	if err != nil {
		t.Fatalf("unable to determine existing reservation: %v", err)
	}
	assertEqualReservation(t, &reservation, storedReservation)
	if storedToken == nil || *storedToken != testTokenID {
		t.Fatalf("reservation found under wrong token: %v", storedToken)
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

	traderKey, _ := testAccount.TraderKey()
	acct, err := store.Account(ctx, traderKey, false)
	if err != nil {
		t.Fatalf("unable to retrieve account: %v", err)
	}
	if !reflect.DeepEqual(acct, &testAccount) {
		t.Fatalf("expected account: %v\ngot: %v",
			spew.Sdump(testAccount), spew.Sdump(acct))
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

	// Make sure we can't update an account that does not exist. Just flip
	// the trader key's sign bit to create a valid point that does not exist
	// in the database.
	a.TraderKeyRaw[0] ^= 0x01
	err = store.UpdateAccount(
		ctx, &a, account.StateModifier(account.StateOpen),
	)
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("expected AccountNotFoundError, got \"%v\"", err)
	}

	// Also make sure we can properly serialize an account that has a
	// closing transaction.
	a.TraderKeyRaw[0] ^= 0x01
	correctTraderKey, err := a.TraderKey()
	if err != nil {
		t.Fatalf("could not parse correct trader key: %v", err)
	}
	closeTx := &wire.MsgTx{
		Version: 2,
		TxIn: []*wire.TxIn{{
			PreviousOutPoint: testAccount.OutPoint,
			SignatureScript:  []byte{},
		}},
		TxOut: []*wire.TxOut{},
	}
	err = store.UpdateAccount(
		ctx, &a, account.StateModifier(account.StateClosed),
		account.LatestTxModifier(closeTx),
	)
	if err != nil {
		t.Fatalf("could not update account to be closed: %v", err)
	}
	closedAcct, err := store.Account(ctx, correctTraderKey, true)
	if err != nil {
		t.Fatalf("could not read closed account: %v", err)
	}
	if !reflect.DeepEqual(closedAcct.LatestTx, closeTx) {
		t.Fatalf("expected close TX: %v\ngot: %v",
			spew.Sdump(closeTx), spew.Sdump(closedAcct.LatestTx))
	}
}

// TestAccountDiffs ensures that we can properly stage and commit account diffs
// within the store.
func TestAccountDiffs(t *testing.T) {
	t.Parallel()

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

	traderKey, err := a.TraderKey()
	if err != nil {
		t.Fatal(err)
	}

	// ErrNoDiff should be returned if we call CommitAccountDiff without a
	// diff being present.
	err = store.CommitAccountDiff(ctx, traderKey)
	if err != account.ErrNoDiff {
		t.Fatalf("expected error %q, got %q", account.ErrNoDiff, err)
	}

	// Proceed to store a pending diff.
	mods := []account.Modifier{
		account.StateModifier(account.StatePendingUpdate),
	}
	if err := store.StoreAccountDiff(ctx, traderKey, mods); err != nil {
		t.Fatalf("unable to store account diff: %v", err)
	}

	// Requesting the account without the diff should return what we expect.
	accountWithoutDiff, err := store.Account(ctx, traderKey, false)
	if err != nil {
		t.Fatalf("unable to retrieve account: %v", err)
	}
	if !reflect.DeepEqual(accountWithoutDiff, &a) {
		t.Fatal("stored account does not match expected")
	}

	// Similarly, requesting the account with the diff should return the
	// account with the diff applied.
	accountWithDiff, err := store.Account(ctx, traderKey, true)
	if err != nil {
		t.Fatalf("unable to retrieve account diff: %v", err)
	}
	aDiff := a.Copy(mods...)
	if !reflect.DeepEqual(accountWithDiff, aDiff) {
		t.Fatal("stored account diff does not match expected")
	}

	// Storing a diff while we already have one should result in
	// ErrAccountDiffAlreadyExists.
	err = store.StoreAccountDiff(ctx, traderKey, mods)
	if err != ErrAccountDiffAlreadyExists {
		t.Fatalf("expected error %q, got %q",
			ErrAccountDiffAlreadyExists, err)
	}

	// Commit the diff. We should expect to see the diff applied
	// when requesting the account without its diff.
	if err := store.CommitAccountDiff(ctx, traderKey); err != nil {
		t.Fatalf("unable to commit account diff: %v", err)
	}
	committedAccount, err := store.Account(ctx, traderKey, false)
	if err != nil {
		t.Fatalf("unable to retrieve account: %v", err)
	}
	if !reflect.DeepEqual(committedAccount, aDiff) {
		t.Fatal("committed account does not match expected")
	}

	// Attempt to store another diff, to ensure the previous one was
	// cleared.
	if err := store.StoreAccountDiff(ctx, traderKey, mods); err != nil {
		t.Fatalf("unable to store account diff: %v", err)
	}

	// Update the account diff and ensure it was updated correctly.
	diffMods := []account.Modifier{
		account.ValueModifier(btcutil.SatoshiPerBitcoin),
	}
	if err := store.UpdateAccountDiff(ctx, traderKey, diffMods); err != nil {
		t.Fatalf("unable to update account diff: %v", err)
	}
	accountWithUpdatedDiff, err := store.Account(ctx, traderKey, true)
	if err != nil {
		t.Fatalf("unable to retrieve account diff: %v", err)
	}
	aWithUpdatedDiff := a.Copy(append(mods, diffMods...)...)
	if !reflect.DeepEqual(accountWithUpdatedDiff, aWithUpdatedDiff) {
		t.Fatal("stored account diff does not match expected")
	}

	// Finally, delete the diff and ensure it wasn't applied.
	if err := store.DeleteAccountDiff(ctx, traderKey); err != nil {
		t.Fatalf("unable to delete account diff: %v", err)
	}
	finalAccount, err := store.Account(ctx, traderKey, true)
	if err != nil {
		t.Fatalf("unable to retrieve account: %v", err)
	}
	if !reflect.DeepEqual(finalAccount, committedAccount) {
		t.Fatal("expected account to not have deleted diff applied")
	}

	// We shouldn't be able to update an account diff after it no longer
	// exists.
	err = store.UpdateAccountDiff(ctx, traderKey, diffMods)
	if err != account.ErrNoDiff {
		t.Fatalf("expteced ErrNoDiff, got %v", err)
	}
}
