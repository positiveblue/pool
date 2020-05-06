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

	// Signer is responsible for deriving shared secrets for accounts
	// between the trader and auctioneer and signing account-related
	// transactions.
	Signer lndclient.SignerClient

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
	tokenID lsat.TokenID) (*Reservation, error) {

	// Check whether we have an existing reservation already.
	//
	// TODO(wilmer): Check whether we already have an account with the key
	// they're trying to make a reservation with?
	reservation, err := m.cfg.Store.HasReservation(ctx, tokenID)
	switch err {
	// If we do, return the associated key.
	case nil:
		return reservation, nil

	// If we don't, proceed to make a new reservation below.
	case ErrNoReservation:
		break

	default:
		return nil, err
	}

	log.Infof("Reserving new account for token %x", tokenID)

	// We'll retrieve the current per-batch key, which serves as the initial
	// key we'll tweak the account's trader key with.
	batchKey, err := m.cfg.Store.BatchKey(ctx)
	if err != nil {
		return nil, err
	}

	reservation = &Reservation{
		AuctioneerKey: &keychain.KeyDescriptor{
			KeyLocator: LongTermKeyLocator,
			PubKey:     m.longTermKey,
		},
		InitialBatchKey: batchKey,
	}
	err = m.cfg.Store.ReserveAccount(ctx, tokenID, reservation)
	if err != nil {
		return nil, err
	}

	return reservation, nil
}

// InitAccount handles a new account request from a trader identified by the
// provided token ID.
func (m *Manager) InitAccount(ctx context.Context, tokenID lsat.TokenID,
	params *Parameters, bestHeight uint32) error {

	// First, make sure we have valid parameters to create the account.
	if err := validateAccountParams(params, bestHeight); err != nil {
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
	reservation, err := m.cfg.Store.HasReservation(ctx, tokenID)
	if err == ErrNoReservation {
		return nil
	}
	if err != nil {
		return err
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
	derivedScript, err := clmscript.AccountScript(
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
		TokenID:       tokenID,
		Value:         params.Value,
		Expiry:        params.Expiry,
		AuctioneerKey: reservation.AuctioneerKey,
		BatchKey:      reservation.InitialBatchKey,
		Secret:        secret,
		State:         StatePendingOpen,
		HeightHint:    uint32(heightHint),
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
	// If the account is in it's initial state, we'll watch for the account
	// on-chain.
	case StatePendingOpen:
		numConfs := numConfsForValue(account.Value)
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
			traderKey, account.OutPoint,
			accountOutput.PkScript, account.HeightHint,
		)
		if err != nil {
			return fmt.Errorf("unable to watch for spend: %v", err)
		}
		err = m.watcher.WatchAccountExpiration(
			traderKey, account.Expiry,
		)
		if err != nil {
			return fmt.Errorf("unable to watch for expiration: %v",
				err)
		}

	// If the account has already expired or has already been closed,
	// there's nothing to be done.
	case StateExpired, StateClosed:
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

	err = m.cfg.Store.UpdateAccount(ctx, account, StateModifier(StateOpen))
	if err != nil {
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

	ctx := context.Background()
	account, err := m.cfg.Store.Account(ctx, traderKey)
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
	case clmscript.IsExpirySpend(spendWitness):
		break

	// If the witness is for a multi-sig spend, then either an order by the
	// trader was matched, or the account was closed. If it was closed, then
	// the account output shouldn't have been recreated.
	//
	// TODO(wilmer): What do we do when an order matched and the account was
	// fully drained?
	case clmscript.IsMultiSigSpend(spendWitness):
		// If the account output was recreated, then there's nothing
		// left for us to do. We'll defer updating the account here, as
		// we'll want to update it atomically along with the matched
		// order, which we don't have information of.
		nextAccountScript, err := account.NextOutputScript()
		if err != nil {
			return err
		}
		_, ok := clmscript.LocateOutputScript(spendTx, nextAccountScript)
		if ok {
			// The account is still open so don't mark it as closed
			// below.
			return nil
		}

	default:
		return fmt.Errorf("unknown spend witness %v", spendWitness)
	}

	log.Infof("Account %x has been closed on-chain with transaction %v",
		account.TraderKey, spendTx.TxHash())

	return m.cfg.Store.UpdateAccount(
		ctx, account, StateModifier(StateClosed),
	)
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
func (m *Manager) ModifyAccount(ctx context.Context, traderKey *btcec.PublicKey,
	newInputs []*wire.TxIn, newOutputs []*wire.TxOut, newParams *Parameters,
	bestHeight uint32) ([]byte, error) {

	account, err := m.cfg.Store.Account(ctx, traderKey)
	if err != nil {
		return nil, err
	}

	// We can only modify accounts in a StateOpen state.
	if account.State != StateOpen {
		return nil, fmt.Errorf("account found in %v must be in %v to "+
			"be modified", account.State, StateOpen)
	}

	// TODO(wilmer): Reject if account has pending orders.

	// If there aren't new account parameters to update, then we'll
	// interpret this as the trader wishing to close their account.
	//
	// TODO(wilmer): Create PendingClosed state to prevent any future orders
	// since we're giving them a valid signature?
	if newParams == nil {
		return m.signAccountSpend(ctx, account, newInputs, newOutputs, 0)
	}

	// TODO(wilmer): Handle modification parameters.
	return nil, fmt.Errorf("unimplemented")
}

// signAccountSpend signs the spending transaction of an account that includes
// the given inputs and outputs, along with the account output as an input, and
// a newly created account output if newAccountValue is not zero.
func (m *Manager) signAccountSpend(ctx context.Context, account *Account,
	inputs []*wire.TxIn, outputs []*wire.TxOut,
	newAccountValue btcutil.Amount) ([]byte, error) {

	// Construct the spending transaction that we'll sign.
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: account.OutPoint})
	for _, input := range inputs {
		tx.AddTxIn(input)
	}
	for _, output := range outputs {
		tx.AddTxOut(output)
	}

	// If the account output must be recreated, then include it with the
	// next output script in the sequence.
	if newAccountValue != 0 {
		nextAccountScript, err := account.NextOutputScript()
		if err != nil {
			return nil, err
		}
		tx.AddTxOut(&wire.TxOut{
			Value:    int64(newAccountValue),
			PkScript: nextAccountScript,
		})
	}

	// The transaction should have its inputs and outputs sorted according
	// to BIP-69.
	txsort.InPlaceSort(tx)

	// Ensure the crafted transaction passes some sanity checks.
	err := blockchain.CheckTransactionSanity(btcutil.NewTx(tx))
	if err != nil {
		return nil, err
	}

	// Gather the remaining components required to sign the transaction from
	// the auctioneer's point-of-view and sign it.
	traderKey, err := account.TraderKey()
	if err != nil {
		return nil, err
	}
	auctioneerKeyTweak := clmscript.AuctioneerKeyTweak(
		traderKey, account.AuctioneerKey.PubKey,
		account.BatchKey, account.Secret,
	)

	witnessScript, err := clmscript.AccountWitnessScript(
		account.Expiry, traderKey, account.AuctioneerKey.PubKey,
		account.BatchKey, account.Secret,
	)
	if err != nil {
		return nil, err
	}

	accountOutput, err := account.Output()
	if err != nil {
		return nil, err
	}

	accountInputIdx := -1
	for i, txIn := range tx.TxIn {
		if txIn.PreviousOutPoint == account.OutPoint {
			accountInputIdx = i
		}
	}
	if accountInputIdx == -1 {
		return nil, errors.New("account input not found")
	}

	signDesc := &input.SignDescriptor{
		KeyDesc: keychain.KeyDescriptor{
			KeyLocator: account.AuctioneerKey.KeyLocator,
		},
		SingleTweak:   auctioneerKeyTweak,
		WitnessScript: witnessScript,
		Output:        accountOutput,
		HashType:      txscript.SigHashAll,
		InputIndex:    accountInputIdx,
		SigHashes:     txscript.NewTxSigHashes(tx),
	}

	log.Infof("Signing closing transaction %v for account %x", tx.TxHash(),
		account.TraderKeyRaw[:])

	sigs, err := m.cfg.Signer.SignOutputRaw(
		ctx, tx, []*input.SignDescriptor{signDesc},
	)
	if err != nil {
		return nil, err
	}

	// We'll need to re-append the sighash flag since SignOutputRaw strips
	// it.
	return append(sigs[0], byte(signDesc.HashType)), nil
}

// validateAccountParams ensures that a trader has provided sane parameters for
// the creation of a new account.
func validateAccountParams(params *Parameters, bestHeight uint32) error {
	if params.Value < minAccountValue {
		return fmt.Errorf("minimum account value allowed is %v",
			minAccountValue)
	}
	if params.Value > maxAccountValue {
		return fmt.Errorf("maximum account value allowed is %v",
			maxAccountValue)
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
