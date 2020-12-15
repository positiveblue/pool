package account

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/btcsuite/btcutil/txsort"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/lndclient"
	accountT "github.com/lightninglabs/pool/account"
	"github.com/lightninglabs/pool/account/watcher"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/keychain"
)

const (
	// minConfs and maxConfs represent the thresholds at both extremes for
	// valid number of confirmations on an account before it is considered
	// open.
	minConfs = 3
	maxConfs = 6

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

	// Signer is responsible for deriving shared secrets for accounts
	// between the trader and auctioneer and signing account-related
	// transactions.
	Signer lndclient.SignerClient

	// ChainNotifier is responsible for requesting confirmation and spend
	// notifications for accounts.
	ChainNotifier lndclient.ChainNotifierClient

	// MaxAcctValue is the maximum account output value we currently allow.
	MaxAcctValue btcutil.Amount
}

// Manager is responsible for the management of accounts on-chain.
type Manager struct {
	started sync.Once
	stopped sync.Once

	cfg ManagerConfig

	// watcher is responsible for watching accounts on-chain for their
	// confirmation, spends, and expiration.
	watcher *watcher.Watcher

	// auctioneerKey is the base auctioneer key of new accounts in its
	// 2-of-2 multi-sig construction. Note the that actual auctioneer key
	// given to the user/trader is tweaked with their key in order to
	// achieve deterministic account creation.
	//
	// This may be nil if the auctioneer hasn't created its account yet.
	auctioneerKey *keychain.KeyDescriptor

	// modifyLocksMtx guards access to modifyLocks.
	modifyLocksMtx sync.Mutex

	// modifyLocks keeps track of an exclusive lock per account that is only
	// applied when handling account modifications requested by their
	// respective trader to ensure there is only one concurrent instance
	// modifying the account.
	//
	// NOTE: We use pointers to sync.Mutex to prevent copies when accessing
	// the map, which are not safe.
	modifyLocks map[[33]byte]*sync.Mutex

	wg   sync.WaitGroup
	quit chan struct{}
}

// NewManager instantiates a new Manager backed by the given config.
func NewManager(cfg *ManagerConfig) (*Manager, error) {
	m := &Manager{
		cfg:         *cfg,
		modifyLocks: make(map[[33]byte]*sync.Mutex),
		quit:        make(chan struct{}),
	}

	m.watcher = watcher.New(&watcher.Config{
		ChainNotifier:       cfg.ChainNotifier,
		HandleAccountConf:   m.handleAccountConf,
		HandleAccountSpend:  m.handleAccountSpend,
		HandleAccountExpiry: m.handleAccountExpiry,
	})

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
func (m *Manager) ReserveAccount(ctx context.Context, params *Parameters,
	tokenID lsat.TokenID, bestHeight uint32) (*Reservation, error) {

	// Check whether we have an existing reservation already.
	//
	// TODO(wilmer): Check whether we already have an account with the key
	// they're trying to make a reservation with?
	reservation, err := m.cfg.Store.HasReservation(ctx, tokenID)
	switch err {
	// If we do, make sure the existing reservation is for the same account
	// being reserved.
	case nil:
		var resTraderKey [33]byte
		copy(resTraderKey[:], params.TraderKey.SerializeCompressed())
		if reservation.TraderKeyRaw != resTraderKey {
			return nil, fmt.Errorf("unable to make new "+
				"reservation, found existing pending "+
				"reservation for account %x",
				reservation.TraderKeyRaw)
		}

		return reservation, nil

	// If we don't, proceed to make a new reservation below.
	case ErrNoReservation:
		break

	default:
		return nil, err
	}

	// Make sure we have valid parameters to create the account.
	if err := m.validateAccountParams(params, bestHeight); err != nil {
		return nil, err
	}

	log.Infof("Reserving new account for token %x and key %x", tokenID,
		params.TraderKey.SerializeCompressed())

	// If the base auctioneer key hasn't been cached yet, attempt to
	// do so now.
	if m.auctioneerKey == nil {
		auctioneer, err := m.cfg.Store.FetchAuctioneerAccount(ctx)
		if err != nil {
			return nil, err
		}
		m.auctioneerKey = auctioneer.AuctioneerKey
	}

	// We'll retrieve the current per-batch key, which serves as the initial
	// key we'll tweak the account's trader key with.
	batchKey, err := m.cfg.Store.BatchKey(ctx)
	if err != nil {
		return nil, err
	}

	// We'll account for the trader possibly being a few blocks ahead of us
	// by padding our known best height.
	heightHint := int64(bestHeight) + heightHintPadding
	if heightHint < 0 {
		heightHint = 0
	}

	var traderKeyRaw [33]byte
	copy(traderKeyRaw[:], params.TraderKey.SerializeCompressed())
	reservation = &Reservation{
		Value:           params.Value,
		AuctioneerKey:   m.auctioneerKey,
		InitialBatchKey: batchKey,
		Expiry:          params.Expiry,
		TraderKeyRaw:    traderKeyRaw,
		HeightHint:      uint32(heightHint),
	}
	err = m.cfg.Store.ReserveAccount(ctx, tokenID, reservation)
	if err != nil {
		return nil, err
	}

	return reservation, nil
}

// InitAccount handles a new account request from a trader identified by the
// provided token ID.
func (m *Manager) InitAccount(ctx context.Context, currentID lsat.TokenID,
	params *Parameters, bestHeight uint32) error {

	tokenID := &currentID

	// First, make sure we have valid parameters to create the account.
	if err := m.validateAccountParams(params, bestHeight); err != nil {
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
	reservation, err := m.cfg.Store.HasReservation(ctx, *tokenID)
	if err == ErrNoReservation {
		// In case the trader lost its state, including the LSAT, it
		// might be possible that there still is a reservation with
		// another token but the same key. We want to allow this so the
		// trader can coop close the account after recovering it.
		reservation, tokenID, err = m.cfg.Store.HasReservationForKey(
			ctx, params.TraderKey,
		)

		// Still no reservation? Then we don't care.
		if err == ErrNoReservation {
			return nil
		}
	}
	if err != nil {
		return err
	}

	// Make sure the trader uses the same key, value and expiry as declared
	// in the reservation.
	var traderKeyRaw [33]byte
	copy(traderKeyRaw[:], params.TraderKey.SerializeCompressed())
	if reservation.TraderKeyRaw != traderKeyRaw {
		return fmt.Errorf("trader key does not match reservation")
	}
	if reservation.Value != params.Value {
		return fmt.Errorf("value does not match reservation")
	}
	if reservation.Expiry != params.Expiry {
		return fmt.Errorf("expiry does not match reservation")
	}

	// With the reservation obtained, we'll need to derive the shared secret
	// of the account based on the trader's and our key. This secret, along
	// with the initial per-batch key, are used as the tweak to the trader's
	// key, and the resulting key is then used as the tweak to the
	// auctioneer's key. This prevents script reuse and provides plausible
	// deniability between account outputs to third parties.
	secret, err := m.cfg.Signer.DeriveSharedKey(
		ctx, params.TraderKey, &reservation.AuctioneerKey.KeyLocator,
	)
	if err != nil {
		return err
	}
	derivedScript, err := poolscript.AccountScript(
		params.Expiry, params.TraderKey,
		reservation.AuctioneerKey.PubKey, reservation.InitialBatchKey,
		secret,
	)
	if err != nil {
		return err
	}
	if !bytes.Equal(derivedScript, params.Script) {
		return fmt.Errorf("script mismatch: expected %x, got %x",
			derivedScript, params.Script)
	}

	// With all of the details gathered, we can persist the account to disk
	// and wait for its confirmation on-chain.
	//
	// Note that at this point, we're still missing a valid outpoint. We do
	// not have this information yet as this is only possible to obtain once
	// we have a transaction that funds the account. Once that's done, we'll
	// complete the remaining field.
	account := &Account{
		TokenID:       *tokenID,
		Value:         params.Value,
		Expiry:        params.Expiry,
		AuctioneerKey: reservation.AuctioneerKey,
		BatchKey:      reservation.InitialBatchKey,
		Secret:        secret,
		State:         StatePendingOpen,
		HeightHint:    reservation.HeightHint,
		OutPoint:      params.OutPoint,
	}
	copy(account.TraderKeyRaw[:], params.TraderKey.SerializeCompressed())
	if err := m.cfg.Store.CompleteReservation(ctx, account); err != nil {
		return err
	}

	log.Infof("Received new account request with outpoint=%v, value=%v, "+
		"expiry=%v", account.OutPoint, account.Value, account.Expiry)

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

	traderKey, err := account.TraderKey()
	if err != nil {
		return err
	}

	switch account.State {
	// If the account is in a pending state, we'll wait for its confirmation
	// on-chain.
	case StatePendingOpen, StatePendingUpdate, StatePendingBatch:
		numConfs := m.numConfsForValue(account.Value)
		log.Infof("Waiting for %v confirmation(s) of account %x",
			numConfs, account.TraderKeyRaw[:])

		err := m.watcher.WatchAccountConf(
			traderKey, account.OutPoint.Hash,
			accountOutput.PkScript, numConfs, account.HeightHint,
		)
		if err != nil {
			return fmt.Errorf("unable to watch for confirmation: "+
				"%v", err)
		}

	// If the account has already confirmed, we need to watch for its spend
	// and expiration on-chain.
	case StateOpen:
		log.Infof("Watching account %x for spend and expiration",
			account.TraderKeyRaw[:])
		err := m.watcher.WatchAccountSpend(
			traderKey, account.OutPoint, accountOutput.PkScript,
			account.HeightHint,
		)
		if err != nil {
			return fmt.Errorf("unable to watch for spend: %v", err)
		}
		err = m.watcher.WatchAccountExpiration(
			traderKey, account.Expiry,
		)
		if err != nil {
			return fmt.Errorf("unable to watch for expiration: %v", err)
		}

	// In StateExpired, we'll wait for a spend to determine if
	// the account has been renewed or fully closed.
	case StateExpired:
		log.Infof("Watching expired account %x for spend",
			account.TraderKeyRaw[:])
		err := m.watcher.WatchAccountSpend(
			traderKey, account.OutPoint, accountOutput.PkScript,
			account.HeightHint,
		)
		if err != nil {
			return fmt.Errorf("unable to watch for spend: %v", err)
		}

	// If the account has already been closed, there's nothing to be done.
	case StateClosed:
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
	account, err := m.cfg.Store.Account(ctx, traderKey, true)
	if err != nil {
		return err
	}

	// Ensure the client provided us with the correct output.
	//
	// TODO(wilmer): If invalid, ban corresponding token? Add new state for
	// invalid accounts?
	if err := validateAccountOutput(account, confDetails.Tx); err != nil {
		return err
	}

	// Once all validation is completed, we can mark the account as
	// confirmed and proceed with the rest of the flow.
	log.Infof("Account %x is now confirmed at height %v!",
		traderKey.SerializeCompressed(), confDetails.BlockHeight)

	// If this is the account's first confirmation (i.e., the confirmation
	// of the account's funding transaction), then we'll tack on the
	// additional LatestTx modifier as required.
	mods := []Modifier{StateModifier(StateOpen)}
	if account.State == StatePendingOpen {
		mods = append(mods, LatestTxModifier(confDetails.Tx))
	}
	if err := m.cfg.Store.UpdateAccount(ctx, account, mods...); err != nil {
		return err
	}

	return m.resumeAccount(account)
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

	// Request a version of the account that includes its pending diff, if
	// any.
	ctx := context.Background()
	account, err := m.cfg.Store.Account(ctx, traderKey, true)
	if err != nil {
		return err
	}

	// We'll need to perform different operations based on the witness  of
	// the spending input of the account.
	spendTx := spendDetails.SpendingTx
	spendWitness := spendTx.TxIn[spendDetails.SpenderInputIndex].Witness

	switch {
	// If the witness is for a spend of the account expiration path, then
	// we'll mark the account as closed as the account has expired and all
	// the funds have been withdrawn.
	case poolscript.IsExpirySpend(spendWitness):
		break

	// If the witness is for a multi-sig spend, then either an order by the
	// trader was matched, the trader has made an account modification, or
	// the account was closed. If it was closed, then the account output
	// shouldn't have been recreated.
	case poolscript.IsMultiSigSpend(spendWitness):
		// An account cannot be spent without our knowledge, so we'll
		// assume we always persist account updates before a broadcast
		// of the spending transaction. Therefore, since we should
		// already have the updates applied, we can just look for our
		// current output in the transaction.
		accountOutput, err := account.Output()
		if err != nil {
			return err
		}
		_, ok := poolscript.LocateOutputScript(
			spendTx, accountOutput.PkScript,
		)
		if !ok {
			// Proceed further below to mark the account closed.
			break
		}

		// Since we requested the account's diff to be returned above,
		// if any, we'll attempt to commit it now that we've seen the
		// spend for the account. This will act as a no-op in the event
		// of a diff not being present.
		err = m.cfg.Store.CommitAccountDiff(ctx, traderKey)
		if err != nil && err != ErrNoDiff {
			return err
		}

		log.Debugf("Account %x was recreated in transaction %v",
			traderKey.SerializeCompressed(), spendTx.TxHash())

		return m.resumeAccount(account)

	default:
		return fmt.Errorf("unknown spend witness %v", spendWitness)
	}

	log.Infof("Account %x has been closed on-chain with transaction %v",
		account.TraderKeyRaw, spendTx.TxHash())

	return m.cfg.Store.UpdateAccount(
		ctx, account, ValueModifier(0), StateModifier(StateClosed),
		LatestTxModifier(spendTx),
	)
}

// WatchMatchedAccounts resumes accounts that were just matched in a batch and
// are expecting the batch transaction to confirm as their next account output.
// This will cancel all previous spend and conf watchers of all accounts
// involved in the batch.
func (m *Manager) WatchMatchedAccounts(ctx context.Context,
	matchedAccounts [][33]byte) error {

	for _, rawAcctKey := range matchedAccounts {
		acctKey, err := btcec.ParsePubKey(rawAcctKey[:], btcec.S256())
		if err != nil {
			return fmt.Errorf("error parsing account key: %v", err)
		}

		acct, err := m.cfg.Store.Account(ctx, acctKey, false)
		if err != nil {
			return fmt.Errorf("error reading account: %v", err)
		}

		// The account was just involved in a batch. That means the
		// account output was spent by a batch transaction. Since we
		// know that we don't roll back or replace a batch transaction,
		// we know that the batch TX will eventually confirm. To handle
		// the case where an account is involved in multiple consecutive
		// batches that are all unconfirmed, we make sure we only track
		// the latest state by canceling all previous spend and
		// confirmation watchers. We then only watch the latest batch
		// and once it confirms, create a new spend watcher on that.
		m.watcher.CancelAccountSpend(acctKey)
		m.watcher.CancelAccountConf(acctKey)

		// After taking part in a batch, the account is either pending
		// closed because it was used up or pending batch update because
		// it was recreated. Either way, let's resume it now by creating
		// the appropriate watchers again if necessary.
		err = m.resumeAccount(acct)
		if err != nil {
			return fmt.Errorf("error resuming account: %v", err)
		}
	}

	return nil
}

// handleAccountExpiry handles the expiration of an account.
func (m *Manager) handleAccountExpiry(traderKey *btcec.PublicKey,
	height uint32) error {

	// Mark the account as expired.
	//
	// TODO(wilmer): Cancel any remaining active orders at this point?
	ctx := context.Background()
	account, err := m.cfg.Store.Account(ctx, traderKey, true)
	if err != nil {
		return err
	}

	// If the account has already been closed, there's no need to mark it as
	// expired.
	if account.State == StateClosed {
		return nil
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

// ModifyAccount handles a trader's intent to modify an account. Since accounts
// are based on a multi-sig, the trader requires our signature before the
// account expires in order to modify it. This method is abstracted such that it
// can handle any type of account modification requested by the trader.
//
// The lockedValue should be set to the value locked up in orders, including
// fees that must be reserved in case the orders match.
func (m *Manager) ModifyAccount(ctx context.Context, traderKey *btcec.PublicKey,
	lockedValue btcutil.Amount, newInputs []*wire.TxIn,
	newOutputs []*wire.TxOut, modifiers []Modifier,
	bestHeight uint32) ([]byte, error) {

	// Obtain the account's modification lock to ensure there are no other
	// concurrent instances attempting to modify it as well.
	var rawTraderKey [33]byte
	copy(rawTraderKey[:], traderKey.SerializeCompressed())

	m.modifyLocksMtx.Lock()
	modifyLock, ok := m.modifyLocks[rawTraderKey]
	if !ok {
		modifyLock = &sync.Mutex{}
		m.modifyLocks[rawTraderKey] = modifyLock
	}
	m.modifyLocksMtx.Unlock()

	modifyLock.Lock()
	defer modifyLock.Unlock()

	// Retrieve the details of the asked account.
	account, err := m.cfg.Store.Account(ctx, traderKey, true)
	if err != nil {
		return nil, err
	}

	// Is the account banned? Don't allow modifications.
	isBanned, expiration, err := m.cfg.Store.IsAccountBanned(
		ctx, traderKey, bestHeight,
	)
	if err != nil {
		return nil, err
	}
	if isBanned {
		return nil, NewErrBannedAccount(expiration)
	}

	// If there aren't new account parameters to update, then we'll
	// interpret this as the trader wishing to close their account.
	//
	// TODO(wilmer): Create PendingClosed state to prevent any future orders
	// since we're giving them a valid signature?
	if len(modifiers) == 0 {
		if account.State == StateClosed {
			return nil, errors.New("account has already been closed")
		}

		// Accounts with a non-zero locked value cannot be closed.
		if lockedValue > 0 {
			return nil, newErrAccountLockedValue(lockedValue)
		}

		sig, _, _, err := m.signAccountSpend(
			ctx, account, lockedValue, newInputs, newOutputs, nil,
		)
		if err != nil {
			return nil, err
		}

		return sig, nil
	}

	// Otherwise, the trader wishes to modify their account. It can only be
	// modified in StateOpen.
	if account.State != StateOpen {
		return nil, fmt.Errorf("account must be in %v to be modified",
			StateOpen)
	}

	// Create the spending transaction for the account including any
	// additional inputs and outputs provided by the trader, along with any
	// account modifications.
	sig, tx, newAccountPoint, err := m.signAccountSpend(
		ctx, account, lockedValue, newInputs, newOutputs, modifiers,
	)
	if err != nil {
		return nil, err
	}

	// With the transaction crafted, store a pending diff of the account and
	// broadcast the transaction. We want to avoid updating the account
	// itself, as we can't guarantee that the trader will end up using our
	// signature.
	modifiers = append(modifiers, StateModifier(StatePendingUpdate))
	modifiers = append(modifiers, OutPointModifier(*newAccountPoint))
	modifiers = append(modifiers, IncrementBatchKey())
	modifiers = append(modifiers, LatestTxModifier(tx))
	err = m.cfg.Store.StoreAccountDiff(ctx, traderKey, modifiers)
	if err != nil {
		return nil, err
	}

	return sig, nil
}

// signAccountSpend signs the spending transaction of an account that includes
// the given inputs and outputs, along with the account output as an input, and
// a newly created account output if newAccountValue is not zero.
func (m *Manager) signAccountSpend(ctx context.Context, account *Account,
	lockedValue btcutil.Amount, inputs []*wire.TxIn, outputs []*wire.TxOut,
	modifiers []Modifier) ([]byte, *wire.MsgTx, *wire.OutPoint, error) {

	// Construct the spending transaction that we'll sign.
	tx := wire.NewMsgTx(2)
	for _, input := range inputs {
		tx.AddTxIn(input)
	}
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: account.OutPoint})
	for _, output := range outputs {
		tx.AddTxOut(output)
	}

	// If the account output must be recreated, then apply the new
	// parameters and include it with the next output script in the
	// sequence.
	var modifiedAccount *Account
	if len(modifiers) > 0 {
		modifiedAccount = account.Copy(modifiers...)
		nextAccountScript, err := modifiedAccount.NextOutputScript()
		if err != nil {
			return nil, nil, nil, err
		}

		// To ensure the account's locked value is enforced, we'll make
		// sure that its value after a withdrawal is still greater or
		// equal to the locked value.
		isWithdrawal := account.Value > modifiedAccount.Value
		if isWithdrawal && modifiedAccount.Value < lockedValue {
			return nil, nil, nil,
				newErrAccountLockedValue(lockedValue)
		}

		tx.AddTxOut(&wire.TxOut{
			Value:    int64(modifiedAccount.Value),
			PkScript: nextAccountScript,
		})
	}

	// The transaction should have its inputs and outputs sorted according
	// to BIP-69.
	txsort.InPlaceSort(tx)

	// Ensure the crafted transaction passes some basic sanity checks.
	err := blockchain.CheckTransactionSanity(btcutil.NewTx(tx))
	if err != nil {
		return nil, nil, nil, err
	}

	// Locate the outpoint of the new account output if it was recreated.
	var newAccountPoint *wire.OutPoint
	if modifiedAccount != nil {
		nextAccountScript, err := modifiedAccount.NextOutputScript()
		if err != nil {
			return nil, nil, nil, err
		}
		outputIndex, ok := poolscript.LocateOutputScript(
			tx, nextAccountScript,
		)
		if !ok {
			return nil, nil, nil, errors.New("account output not found")
		}

		// Verify that the new account value is sane.
		err = m.validateAccountValue(
			btcutil.Amount(tx.TxOut[outputIndex].Value),
		)
		if err != nil {
			return nil, nil, nil, err
		}

		newAccountPoint = &wire.OutPoint{
			Hash:  tx.TxHash(),
			Index: outputIndex,
		}
	}

	// Gather the remaining components required to sign the transaction from
	// the auctioneer's point-of-view and sign it.
	traderKey, err := account.TraderKey()
	if err != nil {
		return nil, nil, nil, err
	}
	auctioneerKeyTweak := poolscript.AuctioneerKeyTweak(
		traderKey, account.AuctioneerKey.PubKey,
		account.BatchKey, account.Secret,
	)

	witnessScript, err := poolscript.AccountWitnessScript(
		account.Expiry, traderKey, account.AuctioneerKey.PubKey,
		account.BatchKey, account.Secret,
	)
	if err != nil {
		return nil, nil, nil, err
	}

	accountOutput, err := account.Output()
	if err != nil {
		return nil, nil, nil, err
	}

	accountInputIdx := -1
	for i, txIn := range tx.TxIn {
		if txIn.PreviousOutPoint == account.OutPoint {
			accountInputIdx = i
		}
	}
	if accountInputIdx == -1 {
		return nil, nil, nil, errors.New("account input not found")
	}

	signDesc := &lndclient.SignDescriptor{
		// The Signer API expects key locators _only_ when deriving keys
		// that are not within the wallet's default scopes.
		KeyDesc: keychain.KeyDescriptor{
			KeyLocator: account.AuctioneerKey.KeyLocator,
		},
		SingleTweak:   auctioneerKeyTweak,
		WitnessScript: witnessScript,
		Output:        accountOutput,
		HashType:      txscript.SigHashAll,
		InputIndex:    accountInputIdx,
	}

	if len(modifiers) == 0 {
		log.Infof("Signing closing transaction %v for account %x",
			tx.TxHash(), account.TraderKeyRaw)
	} else {
		modifiedAccount := account.Copy(modifiers...)
		valueDiff := modifiedAccount.Value - account.Value

		action := fmt.Sprintf("%v deposit into", valueDiff)
		if valueDiff < 0 {
			action = fmt.Sprintf("%v withdrawal from", -valueDiff)
		}

		log.Infof("Signing transaction %v for a %v account %x",
			tx.TxHash(), action, account.TraderKeyRaw)
	}

	sigs, err := m.cfg.Signer.SignOutputRaw(
		ctx, tx, []*lndclient.SignDescriptor{signDesc},
	)
	if err != nil {
		return nil, nil, nil, err
	}

	// We'll need to re-append the sighash flag since SignOutputRaw strips
	// it.
	return append(sigs[0], byte(signDesc.HashType)), tx, newAccountPoint, nil
}

// validateAccountValue ensures that a trader has requested a valid account
// output value.
func (m *Manager) validateAccountValue(value btcutil.Amount) error {
	if value < accountT.MinAccountValue {
		return newErrBelowMinAccountValue(accountT.MinAccountValue)
	}
	if value > m.cfg.MaxAcctValue {
		return newErrAboveMaxAccountValue(m.cfg.MaxAcctValue)
	}

	return nil
}

// validateAccountParams ensures that a trader has provided sane parameters for
// the creation of a new account.
func (m *Manager) validateAccountParams(params *Parameters,
	bestHeight uint32) error {

	if err := m.validateAccountValue(params.Value); err != nil {
		return err
	}
	if params.Expiry < bestHeight+minAccountExpiry {
		return fmt.Errorf("current minimum account expiry allowed is "+
			"height %v", bestHeight+minAccountExpiry)
	}
	if params.Expiry > bestHeight+maxAccountExpiry {
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
func (m *Manager) numConfsForValue(value btcutil.Amount) uint32 {
	confs := maxConfs * value / m.cfg.MaxAcctValue
	if confs < minConfs {
		confs = minConfs
	}
	if confs > maxConfs {
		confs = maxConfs
	}
	return uint32(confs)
}
