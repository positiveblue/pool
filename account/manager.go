package account

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/client/account/watcher"
	"github.com/lightninglabs/agora/client/clmscript"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

const (
	// minConfs and maxConfs represent the thresholds at both extremes for
	// valid number of confirmations on an account before it is considered
	// open.
	minConfs = 3
	maxConfs = 6

	// minAccountValue and maxAccountValue represent the thresholds at both
	// extremes for valid account values. The maximum value is based on the
	// maximum channel size plus some leeway to account for chain fees.
	minAccountValue btcutil.Amount = 100000
	maxAccountValue btcutil.Amount = minAccountValue + (1 << 24) - 1

	// minAccountExpiry and maxAccountExpiry represent the thresholds at
	// both extremes for valid account expirations.
	minAccountExpiry = 144       // One day worth of blocks.
	maxAccountExpiry = 144 * 365 // A year worth of blocks.

	// heightHintPadding is the padding we add from our best known height
	// when receiving a new account request to ensure we can find its output
	// on-chain.
	heightHintPadding = -3
)

var (
	// LongTermKeyLocator is the key locator of our long term key that we'll
	// use as the base auctioneer key of new accounts in its 2-of-2
	// multi-sig construction. Note the that actual auctioneer key given to
	// the user/trader is tweaked with their key in order to achieve
	// deterministic account creation.
	LongTermKeyLocator = keychain.KeyLocator{
		Family: clmscript.AccountKeyFamily,
		Index:  0,
	}

	// errAccountAmountMismatch is an error returned if the account amount
	// the client provided us does not match what's reflected on-chain.
	errAccountAmountMismatch = errors.New("account amount does not match " +
		"on-chain")

	// errAccountScriptMismatch is an error returned if the account script
	// the client provided us does not match what's reflected on-chain.
	errAccountScriptMismatch = errors.New("account script does not match " +
		"on-chain")
)

// ManagerConfig contains all of the required dependencies for the Manager to
// carry out its duties.
type ManagerConfig struct {
	// Store is responsible for storing and retrieving account information
	// reliably.
	Store Store

	// Wallet handles all of our on-chain transaction interaction, whether
	// that is deriving keys, creating transactions, etc.
	Wallet lndclient.WalletKitClient

	// ChainNotifier is responsible for requesting confirmation and spend
	// notifications for accounts.
	ChainNotifier lndclient.ChainNotifierClient
}

// Manager is responsible for the management of accounts on-chain.
type Manager struct {
	started sync.Once
	stopped sync.Once

	cfg ManagerConfig

	// watcher is responsible for watching accounts on-chain for their
	// confirmation, spends, and expiration.
	watcher *watcher.Watcher

	// longTermKey that we'll use as the base auctioneer key of new accounts
	// in its 2-of-2 multi-sig construction. Note the that actual auctioneer
	// key given to the user/trader is tweaked with their key in order to
	// achieve deterministic account creation.
	longTermKey *btcec.PublicKey

	wg   sync.WaitGroup
	quit chan struct{}
}

// NewManager instantiates a new Manager backed by the given config.
func NewManager(cfg *ManagerConfig) (*Manager, error) {
	m := &Manager{
		cfg:  *cfg,
		quit: make(chan struct{}),
	}

	m.watcher = watcher.New(&watcher.Config{
		ChainNotifier:       cfg.ChainNotifier,
		HandleAccountConf:   m.handleAccountConf,
		HandleAccountSpend:  m.handleAccountSpend,
		HandleAccountExpiry: m.handleAccountExpiry,
	})

	// Derive our long term key from its locator.
	ctx := context.Background()
	keyDesc, err := m.cfg.Wallet.DeriveKey(ctx, &LongTermKeyLocator)
	if err != nil {
		return nil, err
	}
	m.longTermKey = keyDesc.PubKey

	return m, nil
}

// Start resumes all account on-chain operation after a restart.
func (m *Manager) Start() error {
	var err error
	m.started.Do(func() {
		err = m.start()
	})
	return err
}

// start resumes all account on-chain operation after a restart.
func (m *Manager) start() error {
	if err := m.watcher.Start(); err != nil {
		return fmt.Errorf("unable to start account watcher: %v", err)
	}

	ctx := context.Background()
	accounts, err := m.cfg.Store.Accounts(ctx)
	if err != nil {
		return err
	}

	for _, account := range accounts {
		if err := m.resumeAccount(account); err != nil {
			return fmt.Errorf("unable to resume account %v: %v",
				account, err)
		}
	}

	return nil
}

// Stop safely stops any ongoing operations within the Manager.
func (m *Manager) Stop() {
	m.stopped.Do(func() {
		m.watcher.Stop()

		close(m.quit)
		m.wg.Wait()
	})
}

// ReserveAccount reserves a new account for a trader associated with the given
// token ID.
func (m *Manager) ReserveAccount(ctx context.Context,
	tokenID lsat.TokenID, traderKey *btcec.PublicKey) (*btcec.PublicKey, error) {

	// Check whether we have an existing reservation already.
	//
	// TODO(wilmer): Check whether we already have an account with the key
	// they're trying to make a reservation with?
	pubKey, err := m.cfg.Store.HasReservation(ctx, tokenID)
	switch err {
	// If we do, return the associated key.
	case nil:
		return pubKey, nil

	// If we don't, proceed to make a new reservation below.
	case ErrNoReservation:
		break

	default:
		return nil, err
	}

	log.Infof("Reserving new account for token %x", tokenID)

	pubKey = input.TweakPubKey(m.longTermKey, traderKey)
	if err := m.cfg.Store.ReserveAccount(ctx, tokenID, pubKey); err != nil {
		return nil, err
	}

	return pubKey, nil
}

// InitAccount handles a new account request from a trader identified by the
// provided token ID.
func (m *Manager) InitAccount(ctx context.Context, tokenID lsat.TokenID,
	op wire.OutPoint, value btcutil.Amount, script []byte, expiry uint32,
	traderKey *btcec.PublicKey, bestHeight uint32) error {

	// First, make sure we have valid parameters to create the account.
	if err := validateAccountParams(value, expiry, bestHeight); err != nil {
		return err
	}

	// We'll start by ensuring we can arrive at the script the trader has
	// provided us with by using their key and ours in the 2-of-2 multi-sig
	// construction. To retrieve our key, we should have an active
	// reservation for the trader. If we don't, this can signal two things:
	//
	//   1. A trader attempting to open a new account without making a
	//      reservation first and obtaining a key from us. We're not
	//      concerned with this as we never provided them with a key, so we
	//      can just ignore it.
	//
	//   2. A trader is resubmitting a valid request to ensure we've
	//      received it.
	//      TODO(wilmer): Verify that we have the account on-disk?
	ourKey, err := m.cfg.Store.HasReservation(ctx, tokenID)
	if err == ErrNoReservation {
		return nil
	}
	if err != nil {
		return err
	}
	derivedScript, err := clmscript.AccountScript(expiry, traderKey, ourKey)
	if err != nil {
		return err
	}
	if !bytes.Equal(derivedScript, script) {
		return fmt.Errorf("script mismatch: expected %x, got %x",
			derivedScript, script)
	}

	// We'll account for the trader possibly being a few blocks ahead of us
	// by padding our known best height.
	heightHint := int64(bestHeight) + heightHintPadding
	if heightHint < 0 {
		heightHint = 0
	}

	// With all of the details gathered, we can persist the account to disk
	// and wait for its confirmation on-chain.
	//
	// Note that at this point, we're still missing a valid outpoint. We do
	// not have this information yet as this is only possible to obtain once
	// we have a transaction that funds the account. Once that's done, we'll
	// complete the remaining field.
	account := &Account{
		TokenID:   tokenID,
		Value:     value,
		Expiry:    expiry,
		TraderKey: traderKey,
		AuctioneerKey: &keychain.KeyDescriptor{
			KeyLocator: LongTermKeyLocator,
			PubKey:     ourKey,
		},
		State:      StatePendingOpen,
		HeightHint: uint32(heightHint),
		OutPoint:   op,
	}
	if err := m.cfg.Store.CompleteReservation(ctx, account); err != nil {
		return err
	}

	log.Infof("Received new account request with outpoint=%v, value=%v, "+
		"expiry=%v", op, value, expiry)

	return m.resumeAccount(account)
}

// resumeAccount performs different operations based on the account's state.
// This method serves as a way to consolidate the logic of resuming accounts on
// startup and during normal operation.
func (m *Manager) resumeAccount(account *Account) error {
	accountOutput, err := account.Output()
	if err != nil {
		return fmt.Errorf("unable to construct account output: %v", err)
	}

	switch account.State {
	// If the account is in it's initial state, we'll watch for the account
	// on-chain.
	case StatePendingOpen:
		numConfs := numConfsForValue(account.Value)
		log.Infof("Waiting for %v confirmation(s) of account %x",
			numConfs, account.TraderKey.SerializeCompressed())
		err := m.watcher.WatchAccountConf(
			account.TraderKey, account.OutPoint.Hash,
			accountOutput.PkScript, numConfs, account.HeightHint,
		)
		if err != nil {
			return fmt.Errorf("unable to watch for confirmation: "+
				"%v", err)
		}

		// Fallthrough to register a spend and expiry notification as
		// well.
		fallthrough

	// If the account has already confirmed, we need to watch for its spend
	// and expiration on-chain.
	case StateOpen:
		log.Infof("Watching account %x for spend and expiration",
			account.TraderKey.SerializeCompressed())
		err := m.watcher.WatchAccountSpend(
			account.TraderKey, account.OutPoint,
			accountOutput.PkScript, account.HeightHint,
		)
		if err != nil {
			return fmt.Errorf("unable to watch for spend: %v", err)
		}
		err = m.watcher.WatchAccountExpiration(
			account.TraderKey, account.Expiry,
		)
		if err != nil {
			return fmt.Errorf("unable to watch for expiration: %v",
				err)
		}

	// If the account has already expired, there's nothing to be done.
	case StateExpired:
		break

	default:
		return fmt.Errorf("unhandled account state %v", account.State)
	}

	return nil
}

// handleAccountConf takes the necessary steps after detecting the confirmation
// of an account on-chain. This involves validation, updating disk state, among
// other things.
func (m *Manager) handleAccountConf(traderKey *btcec.PublicKey,
	confDetails *chainntnfs.TxConfirmation) error {

	ctx := context.Background()
	account, err := m.cfg.Store.Account(ctx, traderKey)
	if err != nil {
		return err
	}

	// To not rely on the order of confirmation and block notifications, if
	// the account confirms at the same height as it expires, we can exit
	// now and let the account be marked as expired by the watcher.
	if confDetails.BlockHeight == account.Expiry {
		return nil
	}

	// Ensure the client provided us with the correct output.
	//
	// TODO(wilmer): If invalid, ban corresponding token? Add new state for
	// invalid accounts?
	if err := validateAccountOutput(account, confDetails.Tx); err != nil {
		return err
	}

	// Once all validation is completed, we can mark the account as
	// confirmed.
	log.Infof("Account %x is now confirmed at height %v!",
		traderKey.SerializeCompressed(), confDetails.BlockHeight)

	return m.cfg.Store.UpdateAccount(ctx, account, StateModifier(StateOpen))
}

// validateAccountOutput ensures that the on-chain account output matches what
// the client has provided us with.
func validateAccountOutput(account *Account, chainTx *wire.MsgTx) error {
	outputIndex := account.OutPoint.Index
	if outputIndex > uint32(len(chainTx.TxOut)) {
		return fmt.Errorf("account output index %v is greater than "+
			"number of transaction outputs %v", outputIndex,
			len(chainTx.TxOut))
	}

	chainOutput := chainTx.TxOut[outputIndex]
	accountOutput, err := account.Output()
	if err != nil {
		return fmt.Errorf("unable to construct account output: %v", err)
	}
	if chainOutput.Value != accountOutput.Value {
		return errAccountAmountMismatch
	}
	if !bytes.Equal(chainOutput.PkScript, accountOutput.PkScript) {
		return errAccountScriptMismatch
	}

	return nil
}

// handleAccountSpend handles the different spend paths of an account.
func (m *Manager) handleAccountSpend(traderKey *btcec.PublicKey,
	spendDetails *chainntnfs.SpendDetail) error {

	// TODO(wilmer): Handle different spend paths.
	return nil
}

// handleAccountExpiry handles the expiration of an account.
func (m *Manager) handleAccountExpiry(traderKey *btcec.PublicKey) error {
	// Mark the account as expired.
	//
	// TODO(wilmer): Cancel any remaining active orders at this point?
	ctx := context.Background()
	account, err := m.cfg.Store.Account(ctx, traderKey)
	if err != nil {
		return err
	}

	err = m.cfg.Store.UpdateAccount(
		ctx, account, StateModifier(StateExpired),
	)
	if err != nil {
		return err
	}

	log.Infof("Account %x has expired as of height %v",
		traderKey.SerializeCompressed(), account.Expiry)

	return nil
}

// validateAccountParams ensures that a trader has provided sane parameters for
// the creation of a new account.
func validateAccountParams(value btcutil.Amount, expiry, bestHeight uint32) error {
	if value < minAccountValue {
		return fmt.Errorf("minimum account value allowed is %v",
			minAccountValue)
	}
	if value > maxAccountValue {
		return fmt.Errorf("maximum account value allowed is %v",
			maxAccountValue)
	}

	if expiry < bestHeight+minAccountExpiry {
		return fmt.Errorf("current minimum account expiry allowed is "+
			"height %v", bestHeight+minAccountExpiry)
	}
	if expiry > bestHeight+maxAccountExpiry {
		return fmt.Errorf("current maximum account expiry allowed is "+
			"height %v", bestHeight+maxAccountExpiry)
	}

	return nil
}

// numConfsForValue chooses an appropriate number of confirmations to wait for
// an account based on its initial value.
//
// TODO(wilmer): Determine the recommend number of blocks to wait for a
// particular output size given the current block reward and a user's "risk
// threshold" (basically a multiplier for the amount of work/fiat-burnt that
// would need to be done to undo N blocks).
func numConfsForValue(value btcutil.Amount) uint32 {
	confs := maxConfs * value / maxAccountValue
	if confs < minConfs {
		confs = minConfs
	}
	if confs > maxConfs {
		confs = maxConfs
	}
	return uint32(confs)
}
