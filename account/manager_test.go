package account

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/aperture/lsat"
	accountT "github.com/lightninglabs/pool/account"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/ban"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/stretchr/testify/require"
)

const (
	timeout = time.Second
)

var (
	testTokenID  = lsat.TokenID{10, 11, 12}
	testOutPoint = wire.OutPoint{
		Hash: [chainhash.HashSize]byte{
			0x51, 0xb6, 0x37, 0xd8, 0xfc, 0xd2, 0xc6, 0xda,
			0x48, 0x59, 0xe6, 0x96, 0x31, 0x13, 0xa1, 0x17,
			0x2d, 0xe7, 0x93, 0xe4, 0xb7, 0x25, 0xb8, 0x4d,
			0x1f, 0xb, 0x4c, 0xf9, 0x9e, 0xc5, 0x8c, 0xe9,
		},
	}
	testAccountValue btcutil.Amount = btcutil.SatoshiPerBitcoin
)

type testHarness struct {
	t        *testing.T
	store    *mockStore
	notifier *MockChainNotifier
	wallet   *mockWallet
	manager  *Manager
}

func newTestHarness(t *testing.T) *testHarness {
	privKey, _ := btcec.PrivKeyFromBytes([]byte{0x1, 0x3, 0x3, 0x7})

	store := newMockStore()
	notifier := NewMockChainNotifier()
	wallet := newMockWallet(privKey)
	banStore := ban.NewStoreMock()
	banManager := ban.NewManager(&ban.ManagerConfig{Store: banStore})

	m, err := NewManager(&ManagerConfig{
		BanManager:    banManager,
		Store:         store,
		Wallet:        wallet,
		Signer:        wallet,
		ChainNotifier: notifier,
		MaxAcctValue:  testAccountValue,
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

func (h *testHarness) assertAccountExists(expected *Account, includeDiff bool) {
	h.t.Helper()

	ctx := context.Background()
	err := wait.NoError(func() error {
		traderKey, err := expected.TraderKey()
		if err != nil {
			return err
		}
		account, err := h.manager.cfg.Store.Account(
			ctx, traderKey, includeDiff,
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

	ctx := context.Background()
	tokenID := testTokenID

	heightHint := uint32(1)
	params := &Parameters{
		Value:     testAccountValue,
		Expiry:    uint32(maxAccountExpiry),
		TraderKey: testTraderKey,
	}
	reservation, err := h.manager.ReserveAccount(
		ctx, params, tokenID, heightHint,
	)
	if err != nil {
		h.t.Fatalf("unable to reserve account: %v", err)
	}
	h.assertNewReservation()

	params.OutPoint = testOutPoint
	script, err := poolscript.AccountScript(
		params.Expiry, params.TraderKey,
		reservation.AuctioneerKey.PubKey, reservation.InitialBatchKey,
		sharedSecret,
	)
	if err != nil {
		h.t.Fatalf("unable to construct new account script: %v", err)
	}
	params.Script = script

	err = h.manager.InitAccount(ctx, tokenID, params, heightHint)
	if err != nil {
		h.t.Fatalf("unable to init account: %v", err)
	}

	actualHeightHint := int64(heightHint) + heightHintPadding
	if actualHeightHint < 0 {
		actualHeightHint = 0
	}
	account := &Account{
		TokenID:       tokenID,
		Value:         params.Value,
		Expiry:        params.Expiry,
		AuctioneerKey: reservation.AuctioneerKey,
		BatchKey:      reservation.InitialBatchKey,
		Secret:        sharedSecret,
		State:         StatePendingOpen,
		HeightHint:    uint32(actualHeightHint),
		OutPoint:      params.OutPoint,
	}
	copy(account.TraderKeyRaw[:], params.TraderKey.SerializeCompressed())

	h.assertAccountExists(account, false)

	return account
}

func (h *testHarness) confirmAccount(a *Account, valid bool,
	confDetails *chainntnfs.TxConfirmation) {

	h.t.Helper()

	select {
	case h.notifier.ConfChan <- confDetails:
	case <-time.After(timeout):
		h.t.Fatalf("unable to notify confirmation of account %x",
			a.TraderKeyRaw)
	}

	if valid {
		a.State = StateOpen
		a.LatestTx = confDetails.Tx
	}

	h.assertAccountExists(a, false)
}

func (h *testHarness) expireAccount(a *Account) {
	h.t.Helper()

	select {
	case h.notifier.BlockChan <- int32(a.Expiry):
	case <-time.After(timeout):
		h.t.Fatalf("unable to notify expiration of account %x",
			a.TraderKeyRaw)
	}

	a.State = StateExpired
	h.assertAccountExists(a, false)
}

func (h *testHarness) spendAccount(a *Account, mods []Modifier,
	spendDetails *chainntnfs.SpendDetail) {

	h.t.Helper()

	select {
	case h.notifier.SpendChan <- spendDetails:
	case <-time.After(timeout):
		h.t.Fatalf("unable to notify spend of account %x",
			a.TraderKeyRaw)
	}

	for _, mod := range mods {
		mod(a)
	}

	h.assertAccountExists(a, false)
}

func (h *testHarness) obtainExpectedSig(account *Account,
	spendTx *wire.MsgTx) []byte {

	h.t.Helper()

	traderKey, err := account.TraderKey()
	if err != nil {
		h.t.Fatal(err)
	}
	witnessScript, err := poolscript.AccountWitnessScript(
		account.Expiry, traderKey, account.AuctioneerKey.PubKey,
		account.BatchKey, account.Secret,
	)
	if err != nil {
		h.t.Fatalf("unable to generate account witness script: %v", err)
	}

	privKey := h.wallet.signer.PrivKeys[0]
	tweak := poolscript.AuctioneerKeyTweak(
		traderKey, account.AuctioneerKey.PubKey, account.BatchKey,
		account.Secret,
	)

	sigHashes := input.NewTxSigHashesV0Only(spendTx)
	expectedSig, err := txscript.RawTxInWitnessSignature(
		spendTx, sigHashes, 0,
		int64(account.Value), witnessScript, txscript.SigHashAll,
		input.TweakPrivKey(privKey, tweak),
	)
	if err != nil {
		h.t.Fatalf("unable to generate expected sig: %v", err)
	}

	return expectedSig
}

// TestZeroHashDisallowed ensures that if the zero hash is passed into the
// InitAccount endpoint, then ErrZeroHashDisallowed is returned.
func TestZeroHashDisallowed(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	ctx := context.Background()
	heightHint := uint32(1)
	params := &Parameters{
		Value:     testAccountValue,
		Expiry:    uint32(maxAccountExpiry),
		TraderKey: testTraderKey,
	}

	reservation, err := h.manager.ReserveAccount(
		ctx, params, testTokenID, heightHint,
	)
	if err != nil {
		t.Fatalf("unable to reserve account: %v", err)
	}
	h.assertNewReservation()

	script, err := poolscript.AccountScript(
		params.Expiry, params.TraderKey,
		reservation.AuctioneerKey.PubKey, reservation.InitialBatchKey,
		sharedSecret,
	)
	if err != nil {
		t.Fatalf("unable to construct new account script: %v", err)
	}
	params.Script = script

	err = h.manager.InitAccount(ctx, testTokenID, params, heightHint)
	if err != ErrZeroHashDisallowed {
		t.Fatalf("unexpcted err: %v", err)
	}
}

// TestReserveAccount ensures that traders are able to reserve a single account
// at a time with an auctioneer.
func TestReserveAccount(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	params := &Parameters{
		Value:     testAccountValue,
		Expiry:    uint32(maxAccountExpiry),
		TraderKey: testTraderKey,
	}

	// The trader with the associated token ID should not have an existing
	// account reservation yet as it hasn't made one.
	ctx := context.Background()
	_, err := h.manager.cfg.Store.HasReservation(ctx, testTokenID)
	if err != ErrNoReservation {
		t.Fatalf("expected error ErrNoReservation, got \"%v\"", err)
	}

	// Try to create a reservation over an amount that is too big.
	params.Value = testAccountValue * 2
	_, err = h.manager.ReserveAccount(ctx, params, testTokenID, 1234)
	if err == nil {
		t.Fatalf("expected error on reservation with invalid amount")
	}

	// Proceed to make a valid reservation now. We should be able to query
	// for it.
	params.Value = testAccountValue
	_, err = h.manager.ReserveAccount(ctx, params, testTokenID, 1234)
	if err != nil {
		t.Fatalf("unable to reserve account: %v", err)
	}
	h.assertNewReservation()
	_, err = h.manager.cfg.Store.HasReservation(ctx, testTokenID)
	if err != nil {
		t.Fatalf("unable to determine existing reservation: %v", err)
	}

	// It's not possible to make a new reservation for the same account
	// while one's already in flight.
	_, err = h.manager.ReserveAccount(ctx, params, testTokenID, 1234)
	if err != nil {
		t.Fatalf("expected no error on idempotent reservation: %v", err)
	}
	h.assertExistingReservation()

	// It's not possible to make a reservation for another account with the
	// same LSAT token while one's already in flight.
	params.TraderKey = testAuctioneerKey
	_, err = h.manager.ReserveAccount(ctx, params, testTokenID, 1234)
	if err == nil || !strings.Contains(err.Error(),
		"found existing pending reservation") {

		t.Fatalf("expected new reservation attempt to fail, got err: "+
			"%v", err)
	}
	h.assertExistingReservation()

	// Complete the reservation so that we can attempt to create another
	// one.
	acct := &Account{
		TokenID: testTokenID,
	}
	copy(acct.TraderKeyRaw[:], testTraderKey.SerializeCompressed())
	err = h.manager.cfg.Store.CompleteReservation(ctx, acct)
	if err != nil {
		t.Fatalf("unable to complete reservation: %v", err)
	}
	_, err = h.manager.cfg.Store.HasReservation(ctx, testTokenID)
	if err != ErrNoReservation {
		t.Fatalf("expected error ErrNoReservation, got \"%v\"", err)
	}

	// A new reservation should be made.
	_, err = h.manager.ReserveAccount(ctx, params, testTokenID, 1234)
	if err != nil {
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

// TestAccountDifferentTraderKey makes sure that an account initialization is
// rejected if the trader key doesn't match the reservation.
func TestAccountDifferentTraderKey(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	params := &Parameters{
		Value:     testAccountValue,
		Expiry:    uint32(maxAccountExpiry),
		TraderKey: testTraderKey,
	}

	ctx := context.Background()
	tokenID := testTokenID
	reservation, err := h.manager.ReserveAccount(ctx, params, tokenID, 1234)
	if err != nil {
		h.t.Fatalf("unable to reserve account: %v", err)
	}

	heightHint := uint32(1)
	params.OutPoint = testOutPoint
	script, err := poolscript.AccountScript(
		params.Expiry, testAuctioneerKey,
		reservation.AuctioneerKey.PubKey, reservation.InitialBatchKey,
		sharedSecret,
	)
	if err != nil {
		h.t.Fatalf("unable to construct new account script: %v", err)
	}
	params.Script = script

	err = h.manager.InitAccount(ctx, tokenID, params, heightHint)
	if err == nil {
		h.t.Fatalf("expected account initialization to fail")
	}
}

// TestAccountAlreadyExists ensures that attempting to initialize an account that
// already exists will fail with ErrAccountExists.
func TestAccountAlreadyExists(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	_ = h.initAccount()

	// After the account is already initialized, call ReserveAccount and
	// assert that fails with ErrAccountExists.
	ctx := context.Background()
	heightHint := uint32(1)
	params := &Parameters{
		Value:     testAccountValue,
		Expiry:    uint32(maxAccountExpiry),
		TraderKey: testTraderKey,
	}

	_, err := h.manager.ReserveAccount(
		ctx, params, testTokenID, heightHint,
	)
	require.ErrorIs(t, err, ErrAccountExists)
	h.assertExistingReservation()
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
	h.confirmAccount(account, true, &chainntnfs.TxConfirmation{
		BlockHeight: account.Expiry,
		Tx: &wire.MsgTx{
			Version: 2,
			TxOut:   []*wire.TxOut{accountOutput},
		},
	})

	h.expireAccount(account)
}

// TestAccountExpirySpend ensures that the auctioneer properly recognizes an
// account as closed once an expiration spend has confirmed.
func TestAccountExpirySpend(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	// Create an account and confirm it.
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

	// Expire the account by notifying the expiration height.
	h.expireAccount(account)

	closeTx := &wire.MsgTx{
		TxIn: []*wire.TxIn{
			{
				Witness: wire.TxWitness{
					nil,
					[]byte("trader sig"),
					[]byte("witness script"),
				},
			},
		},
	}
	mods := []Modifier{
		ValueModifier(0),
		StateModifier(StateClosed),
		LatestTxModifier(closeTx),
	}
	h.spendAccount(account, mods, &chainntnfs.SpendDetail{
		SpendingTx:        closeTx,
		SpenderInputIndex: 0,
	})
}

// TestAccountMultiSigClose ensures that the auctioneer properly recognized an
// account as closed once a multi-sig spend that doesn't recreate an account's
// output has confirmed.
func TestAccountMultiSigClose(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	// Create an account and confirm it.
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

	// Spend the account with a transaction that doesn't recreate the
	// output. This should transition the account to StateClosed, since the
	// witness is that of a multi-sig spend.
	spendTx := &wire.MsgTx{
		TxIn: []*wire.TxIn{
			{
				Witness: wire.TxWitness{
					[]byte("auctioneer sig"),
					[]byte("trader sig"),
					[]byte("witness script"),
				},
			},
		},
	}
	mods := []Modifier{
		ValueModifier(0),
		StateModifier(StateClosed),
		LatestTxModifier(spendTx),
	}
	h.spendAccount(account, mods, &chainntnfs.SpendDetail{
		SpendingTx:        spendTx,
		SpenderInputIndex: 0,
	})
}

// TestAccountSpendRecreatesOutput ensures that the auctioneer properly
// recognizes an account is still open if a spend that recreates the account
// output is detected.
func TestAccountSpendRecreatesOutput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	h := newTestHarness(t)
	h.start()
	defer h.stop()

	// Create the account and confirm it.
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

	// Spend the account with a transaction that recreates the output. This
	// indicates that the account should remain open.
	nextAccountScript, err := account.NextOutputScript()
	if err != nil {
		t.Fatalf("unable to generate next account output: %v", err)
	}
	_, err = h.store.UpdateAccount(ctx, account, IncrementBatchKey())
	if err != nil {
		t.Fatalf("unable to update account: %v", err)
	}
	mods := []Modifier{StateModifier(StateOpen)}
	h.spendAccount(account, mods, &chainntnfs.SpendDetail{
		SpendingTx: &wire.MsgTx{
			TxIn: []*wire.TxIn{
				{
					Witness: wire.TxWitness{
						[]byte("auctioneer sig"),
						[]byte("trader sig"),
						[]byte("witness script"),
					},
				},
			},
			TxOut: []*wire.TxOut{{PkScript: nextAccountScript}},
		},
		SpenderInputIndex: 0,
	})
}

// TestModifyAccountBanned ensures that a signature is not provided to a trader
// when performing a modification to a banned account.
func TestModifyAccountBanned(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	h := newTestHarness(t)
	h.start()
	defer h.stop()

	const bestHeight = 100

	// Create the account and confirm it.
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

	// Ban it with an absolute expiration double the current height.
	traderKey, err := account.TraderKey()
	if err != nil {
		t.Fatalf("unable to retrieve trader key: %v", err)
	}
	expiration, err := h.manager.cfg.BanManager.BanAccount(
		traderKey, bestHeight,
	)
	require.NoError(t, err)

	// Attempt to obtain a signature while the account is still banned. We
	// should see the expected error.
	outputs := []*wire.TxOut{{
		Value:    int64(account.Value) / 2,
		PkScript: []byte{0x01, 0x02, 0x03},
	}}
	_, err = h.manager.ModifyAccount(
		ctx, traderKey, 0, nil, outputs, nil, bestHeight,
	)
	if _, ok := err.(ErrBannedAccount); !ok {
		t.Fatalf("expected ErrBannedAccount, got %v", err)
	}

	// Once the account is no longer banned, we should expect a signature.
	_, err = h.manager.ModifyAccount(
		ctx, traderKey, 0, nil, outputs, nil, expiration,
	)
	if err != nil {
		t.Fatalf("expected valid sig for unbanned account: %v", err)
	}
}

// TestModifyAccountValueBounds ensures that we will not process a trader
// modification if the new account's value is outside of the allowed bounds.
func TestModifyAccountValueBounds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	h := newTestHarness(t)
	h.start()
	defer h.stop()

	const bestHeight = 100

	// Create the account and confirm it.
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

	// Attempt to modify the account's value to be below the minimum. This
	// should result in a ErrBelowMinAccountValue error.
	traderKey, err := account.TraderKey()
	if err != nil {
		t.Fatalf("unable to retrieve trader key: %v", err)
	}
	mods := []Modifier{ValueModifier(accountT.MinAccountValue - 1)}
	_, err = h.manager.ModifyAccount(
		ctx, traderKey, 0, nil, nil, mods, bestHeight,
	)
	if err, ok := err.(ErrBelowMinAccountValue); !ok {
		t.Fatalf("expected ErrBelowMinAccountValue, got %T", err)
	}

	// Attempt to modify the account's value to be above the maximum. This
	// should result in a ErrAboveMaxAccountValue error.
	mods = []Modifier{ValueModifier(h.manager.cfg.MaxAcctValue + 1)}
	_, err = h.manager.ModifyAccount(
		ctx, traderKey, 0, nil, nil, mods, bestHeight,
	)
	if err, ok := err.(ErrAboveMaxAccountValue); !ok {
		t.Fatalf("expected ErrAboveMaxAccountValue, got %T", err)
	}
}

// TestModifyAccountWithdrawal ensures that:
//
// 1. The auctioneer provides the correct signature for a trader's withdrawal.
// 2. The auctioneer stages the account modifications of the withdrawal and
//    applies them once the spend is notified.
func TestModifyAccountWithdrawal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	h := newTestHarness(t)
	h.start()
	defer h.stop()

	const bestHeight = 100

	// Create the account and confirm it.
	accountBeforeWithdrawal := h.initAccount()
	accountOutput, err := accountBeforeWithdrawal.Output()
	if err != nil {
		t.Fatalf("unable to generate account output: %v", err)
	}
	h.confirmAccount(accountBeforeWithdrawal, true, &chainntnfs.TxConfirmation{
		Tx: &wire.MsgTx{
			Version: 2,
			TxOut:   []*wire.TxOut{accountOutput},
		},
	})

	// We'll assume the trader is attempting to withdraw to three different
	// outputs, each with a value of 1/4 of the current account. The trader
	// wishes to keep their account open with a value of 1/6 the current
	// account after creating the specified outputs and paying fees.
	valuePerOutput := accountBeforeWithdrawal.Value / 4
	newAccountValue := valuePerOutput / 2
	p2wpkh, _ := hex.DecodeString("0014ccdeffed4f9c91d5bf45c34e4b8f03a5025ec062")
	p2wsh, _ := hex.DecodeString("00208c2865c87ffd33fc5d698c7df9cf2d0fb39d93103c637a06dea32c848ebc3e1d")
	np2wpkh, _ := hex.DecodeString("a91458c11505b54582ab04e96d36908f85a8b689459787")
	outputs := []*wire.TxOut{
		{
			Value:    int64(valuePerOutput),
			PkScript: p2wpkh,
		},
		{
			Value:    int64(valuePerOutput),
			PkScript: p2wsh,
		},
		{
			Value:    int64(valuePerOutput),
			PkScript: np2wpkh,
		},
	}

	traderKey, err := accountBeforeWithdrawal.TraderKey()
	if err != nil {
		t.Fatal(err)
	}
	mods := []Modifier{ValueModifier(newAccountValue)}

	// Set the locked value to be all of the accounts balance. This should
	// prevent the withdrawal from being successful.
	lockedValue := accountBeforeWithdrawal.Value
	_, err = h.manager.ModifyAccount(
		ctx, traderKey, lockedValue, nil, outputs, mods, bestHeight,
	)
	if _, ok := err.(ErrAccountLockedValue); !ok {
		t.Fatalf("expected ErrAccountLockedValue, got %v", err)
	}

	// Process the withdrawal request from the trader with a zero locked
	// value. This should succeed and return the auctioneer's signature.
	lockedValue = 0
	sig, err := h.manager.ModifyAccount(
		ctx, traderKey, lockedValue, nil, outputs, mods, bestHeight,
	)
	if err != nil {
		t.Fatalf("unable to modify account: %v", err)
	}

	// To ensure the auctioneer signed the proper transaction, we'll obtain
	// the expected transaction for the trader's withdrawal and sign it.
	newPkScript, err := accountBeforeWithdrawal.NextOutputScript()
	if err != nil {
		t.Fatal(err)
	}
	newAccountOutput := &wire.TxOut{
		Value:    int64(newAccountValue),
		PkScript: newPkScript,
	}
	spendTx := &wire.MsgTx{
		Version: 2,
		TxIn: []*wire.TxIn{{
			PreviousOutPoint: accountBeforeWithdrawal.OutPoint,
		}},
		TxOut: append([]*wire.TxOut{newAccountOutput}, outputs...),
	}
	expectedSig := h.obtainExpectedSig(accountBeforeWithdrawal, spendTx)

	// The auctioneer's signature should match what we expect.
	if !bytes.Equal(sig, expectedSig) {
		t.Fatalf("expected sig %x for tx %v, got %x", expectedSig,
			spendTx.TxHash(), sig)
	}

	// We'll then want to make sure that the auctioneer staged the account
	// modifications due to the withdrawal, without applying them to the
	// main account state.
	h.assertAccountExists(accountBeforeWithdrawal, false)

	expectedMods := []Modifier{
		StateModifier(StatePendingUpdate),
		OutPointModifier(wire.OutPoint{
			Hash:  spendTx.TxHash(),
			Index: 0,
		}),
		IncrementBatchKey(),
		LatestTxModifier(spendTx),
	}
	expectedMods = append(expectedMods, mods...)
	accountAfterWithdrawal := accountBeforeWithdrawal.Copy(expectedMods...)
	h.assertAccountExists(accountAfterWithdrawal, true)

	// Then, we'll confirm the spending transaction, which should apply the
	// staged modifications to the main account state. We'll populate the
	// spend transaction with a dummy witness to ensure the multi-sig path
	// is detected.
	signedSpendTx := spendTx.Copy()
	signedSpendTx.TxIn[0].Witness = [][]byte{
		sig, []byte("trader sig"), []byte("witness script"),
	}
	h.spendAccount(
		accountAfterWithdrawal, nil,
		&chainntnfs.SpendDetail{SpendingTx: signedSpendTx},
	)

	// Finally, confirming the account should allow it to transition back to
	// StateOpen.
	h.confirmAccount(accountAfterWithdrawal, true, &chainntnfs.TxConfirmation{
		Tx: spendTx,
	})
}

// TestAccountConsecutiveBatches ensures that we can process an account update
// through multiple consecutive batches that only confirm after we've already
// updated our database state.
func TestAccountConsecutiveBatches(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	account := h.initAccount()
	accountOutput, err := account.Output()
	require.NoError(t, err)
	h.confirmAccount(account, true, &chainntnfs.TxConfirmation{
		Tx: &wire.MsgTx{
			Version: 2,
			TxOut:   []*wire.TxOut{accountOutput},
		},
	})

	// Then, we'll simulate the maximum number of unconfirmed batches to
	// happen that'll all confirm in the same block.
	newValue := testAccountValue / 2
	numBatches := 10
	batchTxs := make([]*wire.MsgTx, numBatches)
	for i := 0; i < numBatches; i++ {
		newPkScript, err := account.NextOutputScript()
		require.NoError(t, err)

		// Create an account spend which we'll notify later. This spend
		// should take the multi-sig path to trigger the logic to lookup
		// previous outpoints.
		batchTx := &wire.MsgTx{
			Version: 2,
			TxIn: []*wire.TxIn{{
				PreviousOutPoint: account.OutPoint,
				Witness: wire.TxWitness{
					{0x01}, // Use multi-sig path.
					{},
					{},
				},
			}},
			TxOut: []*wire.TxOut{{
				Value:    int64(newValue) - int64(i),
				PkScript: newPkScript,
			}},
		}
		batchTxs[i] = batchTx

		mods := []Modifier{
			ValueModifier(newValue - btcutil.Amount(i)),
			StateModifier(StatePendingBatch),
			OutPointModifier(wire.OutPoint{
				Hash:  batchTx.TxHash(),
				Index: 0,
			}),
			IncrementBatchKey(),
			LatestTxModifier(batchTx),
		}
		_, err = h.store.UpdateAccount(
			context.Background(), account, mods...,
		)
		require.NoError(t, err)

		// The batch executor will notify the manager each time a batch
		// is finalized, we do the same here.
		err = h.manager.WatchMatchedAccounts(
			context.Background(), [][33]byte{account.TraderKeyRaw},
		)
		require.NoError(t, err)
	}

	// Finally, confirming the account should allow it to transition back to
	// StateOpen.
	h.confirmAccount(account, true, &chainntnfs.TxConfirmation{
		Tx: batchTxs[len(batchTxs)-1],
	})
}
