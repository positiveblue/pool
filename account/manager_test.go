package account

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/agora/client/clmscript"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/lntest/wait"
)

const (
	timeout = time.Second
)

var (
	testTokenID  = lsat.TokenID{10, 11, 12}
	zeroOutPoint wire.OutPoint
)

type testHarness struct {
	t        *testing.T
	store    *mockStore
	notifier *mockChainNotifier
	wallet   *mockWallet
	manager  *Manager
}

func newTestHarness(t *testing.T) *testHarness {
	store := newMockStore()
	notifier := newMockChainNotifier()
	wallet := &mockWallet{}
	m, err := NewManager(&ManagerConfig{
		Store:         store,
		Wallet:        wallet,
		Signer:        wallet,
		ChainNotifier: notifier,
	})
	if err != nil {
		t.Fatalf("unable to create account manager: %v", err)
	}

	return &testHarness{
		t:        t,
		store:    store,
		notifier: notifier,
		wallet:   wallet,
		manager:  m,
	}
}

func (h *testHarness) start() {
	if err := h.manager.Start(); err != nil {
		h.t.Fatalf("unable to start account manager: %v", err)
	}
}

func (h *testHarness) stop() {
	h.manager.Stop()
}

func (h *testHarness) assertNewReservation() {
	h.t.Helper()

	select {
	case <-h.store.newReservations:
	case <-time.After(timeout):
		h.t.Fatal("expected new reservation")
	}
}

func (h *testHarness) assertExistingReservation() {
	h.t.Helper()

	select {
	case <-h.store.newReservations:
		h.t.Fatal("unexpected new reservation")
	case <-time.After(timeout):
	}
}

func (h *testHarness) assertAccountExists(expected *Account) {
	h.t.Helper()

	ctx := context.Background()
	err := wait.NoError(func() error {
		account, err := h.manager.cfg.Store.Account(
			ctx, expected.TraderKey,
		)
		if err != nil {
			return err
		}

		if !reflect.DeepEqual(account, expected) {
			return fmt.Errorf("expected account: %v\ngot: %v",
				spew.Sdump(expected), spew.Sdump(account))
		}

		return nil
	}, timeout)
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *testHarness) initAccount() *Account {
	h.t.Helper()

	var (
		tokenID    = testTokenID
		value      = maxAccountValue
		expiry     = uint32(maxAccountExpiry)
		traderKey  = testTraderKey
		heightHint = uint32(1)
		op         = zeroOutPoint
	)

	ctx := context.Background()
	reservation, err := h.manager.ReserveAccount(ctx, tokenID)
	if err != nil {
		h.t.Fatalf("unable to reserve account: %v", err)
	}

	script, err := clmscript.AccountScript(
		expiry, traderKey, reservation.AuctioneerKey.PubKey,
		reservation.InitialBatchKey, sharedSecret,
	)
	if err != nil {
		h.t.Fatalf("unable to construct new account script: %v", err)
	}

	err = h.manager.InitAccount(
		ctx, tokenID, op, value, script, expiry, traderKey,
		heightHint,
	)
	if err != nil {
		h.t.Fatalf("unable to init account: %v", err)
	}

	actualHeightHint := int64(heightHint) + heightHintPadding
	if actualHeightHint < 0 {
		actualHeightHint = 0
	}
	account := &Account{
		TokenID:       tokenID,
		Value:         value,
		Expiry:        expiry,
		TraderKey:     traderKey,
		AuctioneerKey: reservation.AuctioneerKey,
		BatchKey:      reservation.InitialBatchKey,
		Secret:        sharedSecret,
		State:         StatePendingOpen,
		HeightHint:    uint32(actualHeightHint),
		OutPoint:      zeroOutPoint,
	}

	h.assertAccountExists(account)

	return account
}

func (h *testHarness) confirmAccount(a *Account, valid bool,
	confDetails *chainntnfs.TxConfirmation) {

	h.t.Helper()

	select {
	case h.notifier.confChan <- confDetails:
	case <-time.After(timeout):
		h.t.Fatalf("unable to notify confirmation of account %x",
			a.TraderKey)
	}

	if valid {
		a.State = StateOpen
	}

	h.assertAccountExists(a)
}

func (h *testHarness) expireAccount(a *Account) {
	h.t.Helper()

	select {
	case h.notifier.blockChan <- int32(a.Expiry):
	case <-time.After(timeout):
		h.t.Fatalf("unable to notify expiration of account %x",
			a.TraderKey)
	}

	a.State = StateExpired
	h.assertAccountExists(a)
}

// TestReserveAccount ensures that traders are able to reserve a single account
// at a time with an auctioneer.
func TestReserveAccount(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	// The trader with the associated token ID should not have an existing
	// account reservation yet as it hasn't made one.
	ctx := context.Background()
	_, err := h.manager.cfg.Store.HasReservation(ctx, testTokenID)
	if err != ErrNoReservation {
		t.Fatalf("expected error ErrNoReservation, got \"%v\"", err)
	}

	// Proceed to make a reservation now. We should be able to query for it.
	if _, err := h.manager.ReserveAccount(ctx, testTokenID); err != nil {
		t.Fatalf("unable to reserve account: %v", err)
	}
	h.assertNewReservation()
	_, err = h.manager.cfg.Store.HasReservation(ctx, testTokenID)
	if err != nil {
		t.Fatalf("unable to determine existing reservation: %v", err)
	}

	// It's not possible to make a reservation for another account while
	// one's already in flight.
	if _, err := h.manager.ReserveAccount(ctx, testTokenID); err != nil {
		t.Fatalf("unable to reserve account: %v", err)
	}
	h.assertExistingReservation()

	// Complete the reservation so that we can attempt to create another
	// one.
	err = h.manager.cfg.Store.CompleteReservation(ctx, &Account{
		TokenID:   testTokenID,
		TraderKey: testTraderKey,
	})
	if err != nil {
		t.Fatalf("unable to complete reservation: %v", err)
	}
	_, err = h.manager.cfg.Store.HasReservation(ctx, testTokenID)
	if err != ErrNoReservation {
		t.Fatalf("expected error ErrNoReservation, got \"%v\"", err)
	}

	// A new reservation should be made.
	if _, err := h.manager.ReserveAccount(ctx, testTokenID); err != nil {
		t.Fatalf("unable to reserve account: %v", err)
	}
	h.assertNewReservation()
}

// TestNewAccount ensures that we can recognize the creation and confirmation of
// a new account.
func TestNewAccount(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	account := h.initAccount()

	accountOutput, err := account.Output()
	if err != nil {
		t.Fatalf("unable to generate account output: %v", err)
	}
	h.confirmAccount(account, true, &chainntnfs.TxConfirmation{
		Tx: &wire.MsgTx{
			Version: 2,
			TxOut:   []*wire.TxOut{accountOutput},
		},
	})
}

// TestAccountInvalidChainOutput ensures that we detect an account as invalid if
// the account details that the client provided do not match what's reflected in
// the chain.
func TestAccountInvalidChainOutput(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	account := h.initAccount()

	// Retrieve the account output and cut the value by half. Since the
	// client provided us with details for an account of 1 BTC, we should
	// fail validation as the on-chain output only reflects half of that.
	accountOutput, err := account.Output()
	if err != nil {
		t.Fatalf("unable to generate account output: %v", err)
	}
	accountOutput.Value /= 2

	h.confirmAccount(account, false, &chainntnfs.TxConfirmation{
		Tx: &wire.MsgTx{
			Version: 2,
			TxOut:   []*wire.TxOut{accountOutput},
		},
	})
}

// TestAccountExpiry ensures that we properly detect and handle the expiration
// of an account.
func TestAccountExpiry(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	account := h.initAccount()

	accountOutput, err := account.Output()
	if err != nil {
		t.Fatalf("unable to generate account output: %v", err)
	}
	h.confirmAccount(account, true, &chainntnfs.TxConfirmation{
		BlockHeight: account.Expiry - 1,
		Tx: &wire.MsgTx{
			Version: 2,
			TxOut:   []*wire.TxOut{accountOutput},
		},
	})

	h.expireAccount(account)
}

// TestAccountConfirmsAtExpiry ensures that we properly mark an account as
// expired if it confirms at the same height it expires.
func TestAccountConfirmsAtExpiry(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	account := h.initAccount()

	// If the account confirms at the same height that it expires, the
	// account should never transition to StateOpen and should just
	// immediately go to StateExpired once the expiry notification is
	// received.
	accountOutput, err := account.Output()
	if err != nil {
		t.Fatalf("unable to generate account output: %v", err)
	}
	h.confirmAccount(account, false, &chainntnfs.TxConfirmation{
		BlockHeight: account.Expiry,
		Tx: &wire.MsgTx{
			Version: 2,
			TxOut:   []*wire.TxOut{accountOutput},
		},
	})

	h.expireAccount(account)
}
