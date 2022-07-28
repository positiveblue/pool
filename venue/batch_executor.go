package venue

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/pool/chaninfo"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/feebump"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// ExecutionMsg is an interface that describes a message sent outbound from the
// executor to a trader.
type ExecutionMsg interface {
	// Batch returns the target batch ID this message refers to.
	Batch() orderT.BatchID
}

// PrepareMsg is the first message the executor sends to all active traders.
// All traders should then send an TraderAcceptMsg in return.
type PrepareMsg struct {
	// MatchedOrders is the map of each sub batch's lease duration and the
	// orders that a trader was matched with in the sub batch for the
	// trader. As we support partial matches, this maps an order nonce to
	// all the other orders it was matched with in the batch.
	MatchedOrders map[uint32]map[orderT.Nonce][]*matching.MatchedOrder

	// ClearingPrices is the map of each sub batch's lease duration and the
	// final clearing price of the sub batch.
	ClearingPrices map[uint32]orderT.FixedRatePremium

	// ChargedAccounts is the set of accounts that the trader used in this
	// batch.
	ChargedAccounts []*matching.AccountDiff

	// AccountOutPoints is the list of new outpoint of user's accounts on
	// the new batch execution transaction.
	AccountOutPoints []wire.OutPoint

	// ExecutionFee describes the execution fee used to craft this batch.
	ExecutionFee terms.FeeSchedule

	// BatchTx is the batch transaction itself, without any witnesses
	// populated.
	BatchTx []byte

	// FeeRate is the target fee rate of the batch execution transaction.
	FeeRate chainfee.SatPerKWeight

	// BatchID is the serialized compressed pubkey that comprises the batch
	// ID.
	BatchID orderT.BatchID

	// BatchVersion is the batch version of this batch.
	BatchVersion uint32

	// BatchHeightHint is the earliest absolute height in the chain in which
	// the batch transaction can be found within. This will be used by
	// traders to base off their absolute channel lease maturity height.
	BatchHeightHint uint32
}

// Batch returns the target batch ID this message refers to.
//
// NOTE: This method is a part of the ExecutionMsg interface.
func (m *PrepareMsg) Batch() orderT.BatchID {
	return m.BatchID
}

// A compile-time constraint to ensure PrepareMsg meets the ExecutionMsg
// interface.
var _ ExecutionMsg = (*PrepareMsg)(nil)

// SignBeginMsg is sent once all traders have replied with an accept message.
// It signifies that the traders should enter the funding flow for their
// matched channels, and send valid signatures for their account witness.
type SignBeginMsg struct {
	// BatchID is the serialized compressed pubkey that comprises the batch
	// ID.
	BatchID orderT.BatchID
}

// Batch returns the target batch ID this message refers to.
//
// NOTE: This method is a part of the ExecutionMsg interface.
func (m *SignBeginMsg) Batch() orderT.BatchID {
	return m.BatchID
}

// A compile-time constraint to ensure SignBeginMsg meets the ExecutionMsg
// interface.
var _ ExecutionMsg = (*SignBeginMsg)(nil)

// FinalizeMsg is the final message sent in the execution flow. We send it once
// the batch has valid signatures, and have been committed to disk.
type FinalizeMsg struct {
	// BatchID is the batch ID to be finalized.
	BatchID orderT.BatchID

	// BatchTxID is the serialized batch txid.
	BatchTxID chainhash.Hash
}

// Batch returns the target batch ID this message refers to.
//
// NOTE: This method is a part of the ExecutionMsg interface.
func (m *FinalizeMsg) Batch() orderT.BatchID {
	return m.BatchID
}

// A compile-time constraint to ensure FinalizeMsg meets the ExecutionMsg
// interface.
var _ ExecutionMsg = (*FinalizeMsg)(nil)

// TraderMsg is an interface that represents a message sent from the trader to
// the executor.
type TraderMsg interface {
	// Src returns the trader that sent us this message.
	Src() matching.AccountID
}

// TraderAcceptMsg is the message a Trader sends to accept the execution of a
// batch.
type TraderAcceptMsg struct {
	// BatchID is the batch ID of the pending batch.
	BatchID orderT.BatchID

	// Trader is the trader that's accepting this batch.
	Trader *ActiveTrader
}

// Src returns the trader that sent us this message.
//
// NOTE: This method is a part of the TraderMsg interface.
func (m *TraderAcceptMsg) Src() matching.AccountID {
	return m.Trader.AccountKey
}

// TraderRejectMsg is the message a Trader sends to reject the execution of a
// batch.
type TraderRejectMsg struct {
	// BatchID is the batch ID of the pending batch.
	BatchID orderT.BatchID

	// Trader is the trader that's rejecting this batch.
	Trader *ActiveTrader

	// Type denotes the type of the reject.
	Type RejectType

	// Reason is a human-readable string that describes why the batch was
	// rejected.
	Reason string
}

// Src returns the trader that sent us this message.
//
// NOTE: This method is a part of the TraderMsg interface.
func (m *TraderRejectMsg) Src() matching.AccountID {
	return m.Trader.AccountKey
}

// TraderPartialRejectMsg is the message a Trader sends to reject the execution
// of certain orders of a batch.
type TraderPartialRejectMsg struct {
	// BatchID is the batch ID of the pending batch.
	BatchID orderT.BatchID

	// Trader is the trader that's rejecting this batch.
	Trader *ActiveTrader

	// Orders is the map of orders they wish to reject and the reasons for
	// rejecting them.
	Orders map[orderT.Nonce]*Reject
}

// Src returns the trader that sent us this message.
//
// NOTE: This method is a part of the TraderMsg interface.
func (m *TraderPartialRejectMsg) Src() matching.AccountID {
	return m.Trader.AccountKey
}

// TraderSignMsg is the message a Trader sends when they have signed all account
// inputs of a batch transaction.
type TraderSignMsg struct {
	// BatchID is the batch ID of the pending batch.
	BatchID []byte

	// Trader is the trader that's signing this batch.
	Trader *ActiveTrader

	// Sigs is the set of account input signatures for each account the
	// trader has in this batch. This maps the trader's account ID to the
	// set of valid witnesses.
	Sigs map[string]*ecdsa.Signature

	// ChannelInfos tracks each channel's information relevant to the trader
	// that must be submitted to the auctioneer in order to enforce the
	// channel's service lifetime.
	ChannelInfos map[wire.OutPoint]*chaninfo.ChannelInfo
}

// Src returns the trader that sent us this message.
//
// NOTE: This method is a part of the TraderMsg interface.
func (m *TraderSignMsg) Src() matching.AccountID {
	return m.Trader.AccountKey
}

// ExecutionResult is the result of a batch execution attempt. If the Err field
// is non-nil, then the attempt failed, and can possibly be restarted depending
// on the nature of the error.
type ExecutionResult struct {
	// Err if non-nil, the then the batch execution failed.
	Err error

	// BatchID is the batch ID of the finalized batch.
	BatchID orderT.BatchID

	// Batch is the raw batch itself.
	Batch *matching.OrderBatch

	// MasterAccountDiff points to a diff of the master account state,
	// which described how the master account changed as a result of this
	// batch.
	MasterAccountDiff *batchtx.MasterAccountState

	// BatchTx is the finalized, fully signed batch transaction.
	BatchTx *wire.MsgTx

	// FeeInfo is the fee information for the fully signed batch
	// transaction.
	FeeInfo *feebump.TxFeeInfo

	// LifetimePackages contains the service level enforcement package for
	// each channel created as a result of the batch.
	LifetimePackages []*chanenforcement.LifetimePackage

	// TODO(roasbeef): other stats?
	//  * coin blocks created (lol, like BDD)
	//  * total fees paid to makers
	//  * clearing rate
	//  * transaction size
	//  * total amount of BTC cleared
}

// executionReq is an inbound request to execute a new batch.
type executionReq struct {
	// exeCtx is the ExecutionContext for the batch we want to execute.
	// This will include an assembled batch transaction, and everything
	// else we need to execute.
	exeCtx *batchtx.ExecutionContext

	// Result is a channel that will be used to send the final results of
	// the batch.
	//
	// NOTE: This chan MUST be buffered.
	Result chan *ExecutionResult
}

// ExecutorStore is a small wrapper around the subastadb.Store interface which
// adds methods for storing and reading the current execution state. This
// interface makes writing unit tests of expected state transitions easier.
type ExecutorStore interface {
	subastadb.Store

	// ExecutionState returns the current execution state.
	ExecutionState() (ExecutionState, error)

	// UpdateExecutionState updates the current execution state.
	UpdateExecutionState(newState ExecutionState) error
}

// AccountWatcher is an interface around the account manager's
// WatchMatchedAccounts method that makes writing unit tests of the executor
// easier.
type AccountWatcher interface {
	// WatchMatchedAccounts resumes accounts that were just matched in a
	// batch and are expecting the batch transaction to confirm as their
	// next account output. This will cancel all previous spend and conf
	// watchers of all accounts involved in the batch.
	WatchMatchedAccounts(context.Context, [][33]byte) error
}

// ExecutorConfig is a struct that holds all configuration items passed to an
// executor.
type ExecutorConfig struct {
	// Store is the store that can hold the execution state and other data.
	Store ExecutorStore

	// Signer can sign batch inputs.
	Signer lndclient.SignerClient

	// BatchStorer can persist a batch after it's been completed.
	BatchStorer BatchStorer

	// AccountWatcher can watch account outputs on chain for confirmations
	// and spends.
	AccountWatcher AccountWatcher

	// TraderMsgTimeout is the maximum time we allow a trader to take for
	// one individual step in the batch signing conversation. If a trader
	// takes longer than that it is timed out and removed from the current
	// batch.
	TraderMsgTimeout time.Duration

	// ActiveTraders is a closure that returns a map of all the current
	// active traders. An active trader is one that's online and has a live
	// communication channel with the BatchExecutor.
	ActiveTraders func() map[matching.AccountID]*ActiveTrader

	// DefaultAuctioneerVersion is the default version of the auctioneer
	// output we use when creating new outputs. If this is changed from one
	// restart to the next it means the account will be upgraded.
	DefaultAuctioneerVersion account.AuctioneerVersion
}

// BatchExecutor is the primary state machine that executes a cleared batch.
// Execution entails orchestrating the creation of hall the channels purchased
// in the batch, and also gathering the signatures of all the input in the
// batch transaction needed to broadcast it.
type BatchExecutor struct {
	started uint32 // To be used atomically.
	stopped uint32 // To be used atomically.

	// newBatches is a channel used to accept new incoming requests to
	// execute a batch.
	newBatches chan *executionReq

	// venueEvents is where any new event related to the current batch
	// execution is sent.
	venueEvents chan EventTrigger

	cfg *ExecutorConfig

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewBatchExecutor creates a new BatchExecutor given the execution
// configuration.
func NewBatchExecutor(cfg *ExecutorConfig) *BatchExecutor {
	return &BatchExecutor{
		cfg:         cfg,
		quit:        make(chan struct{}),
		newBatches:  make(chan *executionReq),
		venueEvents: make(chan EventTrigger),
	}
}

// Start launches all goroutines needed for execution.
func (b *BatchExecutor) Start() error {
	if !atomic.CompareAndSwapUint32(&b.started, 0, 1) {
		return nil
	}

	log.Infof("BatchExecutor starting...")

	b.wg.Add(1)
	go b.executor()

	return nil
}

// Stop signals that the executor start a graceful shutdown.
func (b *BatchExecutor) Stop() error {
	if !atomic.CompareAndSwapUint32(&b.stopped, 0, 1) {
		return nil
	}

	log.Infof("BatchExecutor stopping...")

	close(b.quit)

	b.wg.Wait()

	return nil
}

// validateTradersOnline ensures that all the traders included in this batch
// are currently online within the venue. If not, then the batch will be failed
// with ErrMissingTraders.
func (b *BatchExecutor) validateTradersOnline(
	activeTraders map[matching.AccountID]*ActiveTrader,
	batch *matching.OrderBatch) error {

	offlineTraders := make(map[matching.AccountID]struct{})
	offlineNonces := make(map[orderT.Nonce]struct{})

	// We'll run through all the active orders in this batch, if either
	// trader isn't online, then we'll mark both the order nonce and the
	// trader.
	for _, o := range batch.Orders {
		if _, ok := activeTraders[o.Asker.AccountKey]; !ok {
			offlineTraders[o.Asker.AccountKey] = struct{}{}
			offlineNonces[o.Details.Ask.Nonce()] = struct{}{}
		}

		bid := o.Details.Bid
		if _, ok := activeTraders[o.Bidder.AccountKey]; !ok {
			offlineTraders[o.Bidder.AccountKey] = struct{}{}
			offlineNonces[bid.Nonce()] = struct{}{}
		}

		// For sidecar orders, we also need to make sure the receiver of
		// the channel is online, otherwise we can't continue.
		if bid.IsSidecar {
			_, receiverOnline := activeTraders[bid.MultiSigKey]
			if !receiverOnline {
				offlineTraders[bid.MultiSigKey] = struct{}{}
				offlineNonces[bid.Nonce()] = struct{}{}
			}
		}
	}

	// If no traders were offline, then we're good to go!
	if len(offlineTraders) == 0 {
		return nil
	}

	log.Warnf("Cancelling batch, offline traders: %v",
		traderInfoToString(offlineTraders, offlineNonces))

	// Otherwise, we'll return the set of missing traders along with all
	// the order nonces involved.
	return &ErrMissingTraders{
		TraderKeys:  offlineTraders,
		OrderNonces: offlineNonces,
	}
}

// signAcctInput attempts to produce a valid signature which comprises one half
// of the sigs needed to spend a trader's account input. We also return the
// witness script as well, so the final witness can easily be fully verified.
func (b *BatchExecutor) signAcctInput(masterAcct *account.Auctioneer,
	trader *ActiveTrader, batchTx *wire.MsgTx,
	traderAcctInput *batchtx.BatchInput,
	prevOutputs []*wire.TxOut) (*ecdsa.Signature, []byte, error) {

	log.Debugf("Signing account input for trader=%x", trader.AccountKey[:])

	// First, we'll grab real structs for the trader's account key, and the
	// batch key for the last batch they participated in.
	traderKey, err := btcec.ParsePubKey(trader.AccountKey[:])
	if err != nil {
		return nil, nil, err
	}
	batchKey, err := btcec.ParsePubKey(trader.BatchKey[:])
	if err != nil {
		return nil, nil, err
	}

	// With the keys obtained, we'll now derive the tweak we need to obtain
	// our private key, as well as the full witness script of the trader's
	// account output.
	auctioneerKeyTweak := poolscript.AuctioneerKeyTweak(
		traderKey, masterAcct.AuctioneerKey.PubKey,
		batchKey, trader.VenueSecret,
	)
	witnessScript, err := poolscript.AccountWitnessScript(
		trader.AccountExpiry, traderKey,
		masterAcct.AuctioneerKey.PubKey, batchKey, trader.VenueSecret,
	)
	if err != nil {
		return nil, nil, err
	}

	// With all the information obtained, we'll now generate our half of
	// the multi-sig.
	signDesc := &lndclient.SignDescriptor{
		// The Signer API expects key locators _only_ when deriving keys
		// that are not within the wallet's default scopes.
		KeyDesc: keychain.KeyDescriptor{
			KeyLocator: masterAcct.AuctioneerKey.KeyLocator,
		},
		SingleTweak:   auctioneerKeyTweak,
		WitnessScript: witnessScript,
		Output:        &traderAcctInput.PrevOutput,
		HashType:      txscript.SigHashAll,
		InputIndex:    int(traderAcctInput.InputIndex),
	}
	auctioneerSigs, err := b.cfg.Signer.SignOutputRaw(
		context.Background(), batchTx,
		[]*lndclient.SignDescriptor{signDesc}, prevOutputs,
	)
	if err != nil {
		return nil, nil, err
	}

	sig, err := ecdsa.ParseDERSignature(auctioneerSigs[0])
	if err != nil {
		return nil, nil, err
	}
	return sig, witnessScript, nil
}

// stateStep takes the current state, environment and a trigger and attempts to
// advance the state machine to produce a new modified environment, and the
// next state we' should transition to.
func (b *BatchExecutor) stateStep(currentState ExecutionState, // nolint:gocyclo
	env environment, event EventTrigger) (ExecutionState, environment, error) {

	ctxb := context.Background()

	switch currentState {
	// This is our default state, if we get an event in this state, then we
	// need a valid OrderBatch. We'll ensure that all the traders are
	// online, otherwise we'll emit an error.
	case NoActiveBatch:
		// We don't expect to process any messages in this state, if a
		// client does send one, then its went rogue, but we don't want
		// that to mess up the entire batch.
		if event.Trigger() != NewBatch {
			log.Warnf("Received bad trigger (%v) in NoActiveBatch "+
				"state, ignoring...", event.Trigger())

			return NoActiveBatch, env, nil
		}

		// Before we attempt to process this event, we'll ensure that
		// all the trader's in the bach are actually online and
		// register. If one or more of them aren't, then we'll need to
		// reject this batch with the proper error.
		activeTraders := b.cfg.ActiveTraders()
		err := b.validateTradersOnline(
			activeTraders, env.exeCtx.OrderBatch,
		)
		if err != nil {
			env.tempErr = err
			return BatchTempError, env, nil
		}

		// To ensure that we have a fully up to date view of the state
		// of each of the trader's, we'll sync what we have here, with
		// the set of traders on disk.
		activeTraders, err = b.syncTraderState(activeTraders)
		if err != nil {
			return 0, env, err
		}

		// Now that we know this batch is valid, we'll populate the set
		// of active traders in the environment so we can begin our
		// message passing phase.
		for trader := range env.exeCtx.OrderBatch.FeeReport.AccountDiffs {
			env.traders[trader] = activeTraders[trader]
		}

		// We'll also need to add all sidecar traders to the env as
		// they'll also get prepare messages. We've already made sure
		// they're online.
		for _, o := range env.exeCtx.OrderBatch.Orders {
			if !o.Details.Bid.IsSidecar {
				continue
			}

			trader := o.Details.Bid.MultiSigKey
			env.sidecarTraders[trader] = activeTraders[trader]
			env.sidecarOrders[o.Details.Bid.Nonce()] = trader
		}

		// If there are no traders part of this batch, it means the
		// batch is empty and there is nothing to execute. In this case
		// we skip straight ahead to the finalization stage.
		if len(env.traders) == 0 {
			return BatchFinalize, env, nil
		}

		// With the execution context created, we'll now craft a
		// PrepareMsg to send to each trader to kick off the batch. If
		// we can't send a message to any of these traders, then we'll
		// mark it as an error to proceed to the BatchTempError state.
		if err := env.sendPrepareMsg(); err != nil {
			return 0, env, err
		}

		// Now that we've sent our prepare message, we'll start a timer
		// for each of the active traders. One the timer ticks, a new
		// event will be sent for its expiration.
		//
		// TODO(roasbeef): re-work? e.traders
		env.launchMsgTimers(b.venueEvents, env.traders)
		env.launchMsgTimers(b.venueEvents, env.sidecarTraders)

		return PrepareSent, env, nil

	// In this state, we'll grab the current temp error and send it to each
	// trader included within this batch.
	//
	// TODO(roasbeef): remove state?
	case BatchTempError:
		return NoActiveBatch, env, env.tempErr

	// In this state, we've sent the prepare message to all the current
	// active traders and we're waiting for them all to send back the
	// accept message.
	case PrepareSent:
		// In this state, we expect one of two events: either a msg
		// timeout as ticked, or we've received the expected message
		// from the end trader.
		switch event.Trigger() {
		// If we get a timeout event, then one of the trader's failed
		// to send us the proper message in time.
		case MsgTimeout:
			// This timeout could be a false positive, meaning it
			// came after we already processed the proper message
			// for the trader. If we have no error, then we'll just
			// remain in this state.
			err := env.processMsgTimeout(
				event, "OrderMatchAccept", env.traderToOrders,
			)
			if err != nil {
				env.tempErr = err
				return BatchTempError, env, nil
			}

			return PrepareSent, env, nil

		// In this state, we've received the proper message from a
		// peer, we can disable its timer and then see if this is the
		// last one we need.
		case MsgRecv:
			// As we've received a message from this trader, we can
			// cancel the timer as they're behaving as expected.
			msgRecv := event.(*msgRecvEvent)

			// React depending on the type of the message the trader
			// sent us.
			switch m := msgRecv.msg.(type) {
			// This is bad: The trader either disagrees with our
			// batch calculation or failed hard during the setup
			// part. We'll just exclude the trader completely from
			// this batch.
			case *TraderRejectMsg:
				log.Warnf("Received TraderRejectMsg from "+
					"trader=%x", m.Src())

				b.handleFullReject(m, &env)

			// The trader rejected part of the batch either because
			// of a problem or because of a preference. We don't
			// want to remove any orders from the match making
			// process completely. Instead we just note these
			// conflicts in our conflict trackers and give the match
			// maker a chance to find more suitable matches.
			case *TraderPartialRejectMsg:
				log.Warnf("Received TraderPartialRejectMsg from "+
					"trader=%x", m.Src())

				// Add the rejected orders/peers to our conflict
				// trackers according to the reject reason sent
				// by the trader. This also marks this trader
				// as having responded while giving all other
				// traders a chance to still respond.
				b.handlePartialReject(m, &env)

			// The trader accepted the batch.
			case *TraderAcceptMsg:
				log.Debugf("Received OrderMatchAccept from "+
					"trader=%x", m.Src())

				env.cancelTimerForTrader(m.Src())

			// We got a message that we don't expect at this time.
			default:
				log.Errorf("expected accept or reject msg, "+
					"instead got %T", msgRecv.msg)

				return PrepareSent, env, nil
			}

			// If there're no more active timers, then we can
			// advance to the next phase, as we received all the
			// intended messages.
			if env.noTimersActive() {
				// In case we have any rejects, we need to
				// signal now that the match making has to be
				// started over.
				if len(env.rejectingTraders) > 0 {
					rejects := env.rejectingTraders
					env.tempErr = &ErrReject{
						RejectingTraders: rejects,
					}
					return BatchTempError, env, nil
				}

				// Otherwise we have a successful batch
				// preparation.
				log.Infof("All traders accepted batch, " +
					"entering signing phase")

				// At this point we've received all the accept
				// messages, so we'll send SignBegin, launch a
				// new batch of messages timers for the
				// response, and head to our next state.
				err := env.sendSignBeginMsg()
				if err != nil {
					return 0, env, err
				}

				env.launchMsgTimers(b.venueEvents, env.traders)
				env.launchMsgTimers(
					b.venueEvents, env.sidecarTraders,
				)
				return BatchSigning, env, nil
			}
		}

		return PrepareSent, env, nil

	// In this phase, we wait for all the traders to send us the
	// TraderSignMsg which includes a signature for their account output.
	case BatchSigning:
		// In this phase, like the phase above, we'll wait for the
		// traders to send us TraderSignMsg which includes the
		// signatures for each of their account outputs.
		switch event.Trigger() {
		case MsgTimeout:
			err := env.processMsgTimeout(
				event, "OrderMatchSign", env.traderToOrders,
			)
			if err != nil {
				env.tempErr = err
				return BatchTempError, env, nil
			}

			return BatchSigning, env, nil

		// In this state, a peer has sent us the signatures for their
		// account input, we'll validate it to ensure it's what we
		// expect, then stash it away unless this is the final
		// signature we need.
		case MsgRecv:
			msgRecv := event.(*msgRecvEvent)
			src := msgRecv.msg.Src()

			// Now that we've stopped their message timer, we'll
			// check to see that this is the message we expect, and
			// the user specified a witness for their account.
			// React depending on the type of the message the trader
			// sent us.
			switch m := msgRecv.msg.(type) {
			// The trader rejected part of the batch either because
			// of a problem or because of a preference. We don't
			// want to remove any orders from the match making
			// process completely. Instead we just note these
			// conflicts in our conflict trackers and give the match
			// maker a chance to find more suitable matches.
			case *TraderPartialRejectMsg:
				// Add the rejected orders/peers to our conflict
				// trackers according to the reject reason sent
				// by the trader. This also marks this trader
				// as having responded while giving all other
				// traders a chance to still respond.
				b.handlePartialReject(m, &env)

				// We can't continue here. Are we done yet or
				// do we need to wait for more messages?
				if env.noTimersActive() {
					// As this is the last message and it's
					// a reject, we can now return the
					// temporary failure causing the batch
					// to go back into the match making
					// phase.
					rejects := env.rejectingTraders
					env.tempErr = &ErrReject{
						RejectingTraders: rejects,
					}
					return BatchTempError, env, nil
				}

				// Give the other traders a chance to respond.
				return BatchSigning, env, nil

			// The trader accepted the batch.
			case *TraderSignMsg:
				log.Debugf("Received OrderMatchSign from "+
					"trader=%x", src)

				// As we've received the expected message from
				// this trader, we'll cancel it timer now,
				// draining out the channel if it already
				// ticked. Then we continue with extracting the
				// signature below.
				env.cancelTimerForTrader(src)

			// If a trader sent the wrong message, then we'll treat
			// that as a no-op to ensure a single trader can't
			// disrupt the entire state machine.
			default:
				log.Errorf("expected OrderMatchSign, instead "+
					"got %T from trader %x", msgRecv.msg,
					msgRecv.msg.Src())

				return BatchSigning, env, nil
			}

			// If we've gotten all messages and there were some
			// rejects, we need to restart at matchmaking.
			if env.noTimersActive() && len(env.rejectingTraders) > 0 {
				rejects := env.rejectingTraders
				env.tempErr = &ErrReject{
					RejectingTraders: rejects,
				}
				return BatchTempError, env, nil
			}

			// We get to the spicy sauce of the sidecar channel
			// funding process now. The provider (=trader with Pool
			// account that submitted the bid) will send the
			// signature for the account input. The recipient
			// (=trader with no account and possibly a light client)
			// sends the channel info as they receive the resulting
			// channel.
			signMsg := msgRecv.msg.(*TraderSignMsg)
			state, err := b.handleSignMsg(signMsg, src, &env)
			if err != nil {
				return state, env, err
			}
			if state == BatchTempError {
				return state, env, nil
			}

			// If we have all the witnesses we need and also got all
			// channel info from all traders, then we'll proceed to
			// the batch finalize phase.
			numChans := len(env.exeCtx.AllChanOutputs())
			if len(env.acctWitnesses) == len(env.traders) &&
				len(env.chanInfoComplete) == numChans {

				log.Infof("Received valid signatures and " +
					"chan info from all traders, " +
					"finalizing batch")

				return BatchFinalize, env, nil
			}

			// Otherwise, we'll remain in this state and fall through.
		}

		return BatchSigning, env, nil

	// This is the final state. If we get to this point, then a have a
	// valid batch that's ready for broadcast.
	case BatchFinalize:
		exeCtx := env.exeCtx
		batchTx := exeCtx.ExeTx

		// Now that we have all valid a account signatures, we'll
		// create the witness for the auctioneer's account point.
		auctioneerInputIndex := exeCtx.MasterAccountDiff.InputIndex
		auctioneerWitness, err := env.exeCtx.MasterAcct.AccountWitness(
			b.cfg.Signer, batchTx, auctioneerInputIndex,
			exeCtx.MasterAcct.Version, env.exeCtx.PrevOutputs(),
		)
		if err != nil {
			return 0, env, err
		}

		// Now that we have our final witness, we'll populate all
		// witnesses in the batch execution transaction.
		for inputIndex, witness := range env.acctWitnesses {
			batchTx.TxIn[inputIndex].Witness = witness
		}
		batchTx.TxIn[auctioneerInputIndex].Witness = auctioneerWitness

		// If there were any extra inputs added to the batch
		// transaction by the auctioneer, we'll sign these now.
		extraInputs := make(map[int]*lnwallet.Utxo)
		for _, in := range env.exeCtx.ExtraInputs() {
			index := int(in.InputIndex)
			extraInputs[index] = &lnwallet.Utxo{
				Value:    btcutil.Amount(in.PrevOutput.Value),
				PkScript: in.PrevOutput.PkScript,
			}
		}

		witnesses, err := account.InputWitnesses(
			b.cfg.Signer, batchTx, extraInputs,
		)
		if err != nil {
			return 0, env, err
		}

		for index, w := range witnesses {
			batchTx.TxIn[index].SignatureScript = w.SigScript
			batchTx.TxIn[index].Witness = w.Witness
		}

		// We have the fully signed transaction, and can find its final
		// weight.
		finalTxWeight := blockchain.GetTransactionWeight(
			btcutil.NewTx(batchTx),
		)

		// The fee paid won't change from the unsigned batch tx, so use
		// the fee from the fee info estimate, together with the final
		// tx weight for the final fee info.
		feeInfo := &feebump.TxFeeInfo{
			Fee:    exeCtx.FeeInfoEstimate.Fee,
			Weight: finalTxWeight,
		}

		// With the batch now finalized, we'll commit the batch to
		// disk, as we're now able to broadcast a valid multi-funding
		// transaction.
		env.result = &ExecutionResult{
			BatchID:           exeCtx.BatchID,
			Batch:             exeCtx.OrderBatch,
			MasterAccountDiff: exeCtx.MasterAccountDiff,
			BatchTx:           batchTx,
			FeeInfo:           feeInfo,
			LifetimePackages:  env.lifetimePkgs,
		}
		if err := b.cfg.BatchStorer.Store(ctxb, env.result); err != nil {
			log.Errorf("Failed to store batch: %v", err)
			return 0, env, err
		}

		log.Infof("Batch(%x) finalized and committed!", exeCtx.BatchID[:])

		diffs := exeCtx.OrderBatch.FeeReport.AccountDiffs
		matchedAccounts := make([][33]byte, 0, len(diffs))
		for id := range diffs {
			matchedAccounts = append(matchedAccounts, id)
		}
		err = b.cfg.AccountWatcher.WatchMatchedAccounts(
			ctxb, matchedAccounts,
		)
		if err != nil {
			log.Errorf("Failed to watch matched accounts: %v", err)
			return 0, env, err
		}

		// Next, we'll send the finalize message to all the active
		// traders.
		if err := env.sendFinalizeMsg(batchTx.TxHash()); err != nil {
			return 0, env, err
		}

		return BatchComplete, env, nil

	// This is our terminal state, we'll end in this state until we
	// reset and await a new batch.
	case BatchComplete:
		return BatchComplete, env, nil

	default:
		return 0, env, fmt.Errorf("unknown state: %v", currentState)
	}
}

// handleSignMsg handles a single incoming sign message from a trader. This is
// the most complex part of the batch execution because in the case of a sidecar
// channel, we'll get two messages from different traders for the same orders in
// the batch. We'll need to check them depending on their role in the process.
func (b *BatchExecutor) handleSignMsg(signMsg *TraderSignMsg,
	src matching.AccountID, env *environment) (ExecutionState, error) {

	// Is this message coming from the sidecar stream, meaning the trader
	// that sent the message is a sidecar channel recipient?
	_, isSidecarRecipient := env.sidecarTraders[src]

	// Sidecar recipients are the only traders that don't send signatures.
	// Everyone else must always send signatures in this step.
	if !isSidecarRecipient {
		acctSig, ok := signMsg.Sigs[hex.EncodeToString(src[:])]
		if !ok {
			return 0, fmt.Errorf("account witness for %x not "+
				"found", src)
		}

		// As we want to fully validate the witness, we'll generate our
		// own signature for his input to ensure we can properly spend
		// it with broadcast of the batch transaction.
		trader := env.traders[src]
		traderAcctInput, ok := env.exeCtx.AcctInputForTrader(
			trader.AccountKey,
		)
		if !ok {
			return 0, fmt.Errorf("unable to find input for trader "+
				"%x", trader.AccountKey[:])
		}
		auctioneerSig, witnessScript, err := b.signAcctInput(
			env.exeCtx.MasterAcct, trader, env.exeCtx.ExeTx,
			traderAcctInput, env.exeCtx.PrevOutputs(),
		)
		if err != nil {
			return 0, fmt.Errorf("unable to generate auctioneer "+
				"sig: %v", err)
		}

		// We'll now full validate the witness to ensure we'll be able
		// to broadcast the funding transaction. If the trader gives us
		// an invalid witness, then we'll return an error identifying
		// them as rogue.
		err = env.validateAccountWitness(
			witnessScript, traderAcctInput, acctSig, auctioneerSig,
		)
		if err != nil {
			log.Warnf("trader=%x sent invalid signature: %v",
				src[:], err)

			env.tempErr = &ErrInvalidWitness{
				VerifyErr:   err,
				Trader:      src,
				OrderNonces: env.traderToOrders[src],
			}
			return BatchTempError, nil
		}
	}

	// As a sidecar receiver, their account isn't registered to receive a
	// channel output, at least not in the indexed batch TX. But we know the
	// order they are sending the channel info for and can therefore look up
	// the channel outputs that way.
	chanOutputs, _ := env.exeCtx.ChanOutputsForTrader(src)
	if isSidecarRecipient {
		// We need to look up the channel outputs by the bid order's
		// nonce. Here it's a 1:1 relationship between the trader key
		// (channel funding multisig in this case) and the nonce. This
		// isn't the case for normal orders where one account can have
		// multiple orders in the same batch. We don't want to add
		// another index map just for this lookup as there probably
		// won't be a large number of sidecar
		// orders in any batch. So we loop instead.
		for nonce, trader := range env.sidecarOrders {
			if trader == src {
				chanOutputs, _ = env.exeCtx.OutputsForOrder(
					nonce,
				)

				break
			}
		}
	}

	// With the witness validated, we'll also validate the channel info
	// submitted with the matched trader to ensure we have the correct base
	// point keys to enforce the channel's service level agreement.
	for _, chanOutput := range chanOutputs {
		chanPoint := chanOutput.OutPoint
		chanOrderNonce := chanOutput.Order.Nonce()

		// The ChannelInfos map is always initialized when we parse the
		// RPC message. Therefore we shouldn't run into a nil pointer
		// error here, even if the trader didn't send any infos. This is
		// nice since we can then also return the error struct that
		// includes the channel point that is missing the channel infos.
		chanInfo, ok := signMsg.ChannelInfos[chanPoint]
		if !ok {
			// The only trader that doesn't need to send channel
			// information is the sidecar channel provider. For
			// everyone else we fail if the info is missing.
			_, isSidecarOrder := env.sidecarOrders[chanOrderNonce]
			if isSidecarOrder && !isSidecarRecipient {
				continue
			}

			// The trader didn't provide information for one of
			// their channels, so we'll ignore their orders and
			// retry the batch.
			env.tempErr = &ErrMissingChannelInfo{
				Trader:       src,
				ChannelPoint: chanPoint,
				OrderNonces:  env.traderToOrders[src],
			}
			log.Warn(env.tempErr)
			return BatchTempError, nil
		}

		err := env.validateChanInfo(src, chanOutput, chanInfo)
		if err != nil {
			// If the information submitted between both parties of
			// the to be created channel don't match, then one must
			// be lying. The bidder doesn't have any incentive to
			// lie, but we cannot be sure, so we'll punish both
			// anyway.
			matchingTrader := env.matchingChanTrader[chanPoint].
				AccountID
			orders := env.traderToOrders[src]
			orders = append(
				orders, env.traderToOrders[matchingTrader]...,
			)
			env.tempErr = &ErrNonMatchingChannelInfo{
				Err:          err,
				Trader1:      src,
				Trader2:      matchingTrader,
				ChannelPoint: chanPoint,
				OrderNonces:  orders,
			}
			log.Warn(env.tempErr)
			return BatchTempError, nil
		}
	}

	return BatchSigning, nil
}

// handleFullReject processes the full reject message of a trader. All orders
// of the trader that were involved in the batch are added to the map of
// rejected orders as the trader likely won't be able to participate in this or
// future batches because of technical issues.
func (b *BatchExecutor) handleFullReject(msg *TraderRejectMsg,
	env *environment) {

	// The source of the message is the trader rejecting the orders.
	reporter := msg.Src()

	// We mark this trader as having responded and give all other traders
	// also a chance to maybe reject parts of the order too. But we track
	// the reject to flag we have to restart match making and also to detect
	// potential DoS attacks from too many rejects.
	env.cancelTimerForTrader(reporter)

	// We simply store all orders for this trader and the reject reason.
	// The auctioneer will do the more detailed check and remove the orders
	// from matchmaking.
	env.rejectingTraders[reporter] = &OrderRejectMap{
		FullReject: &Reject{
			Type:   msg.Type,
			Reason: msg.Reason,
		},
		OwnOrders: env.traderToOrders[reporter],
	}
}

// handlePartialReject processes the partial reject message of a trader. The
// local and remote node of the two matched orders are extracted and, depending
// on the type of reported conflict, the two nodes are reported to the conflict
// handler responsible for that type of conflict.
func (b *BatchExecutor) handlePartialReject(msg *TraderPartialRejectMsg,
	env *environment) {

	// The source of the message is the trader rejecting the orders.
	reporter := msg.Src()

	// We mark this trader as having responded and give all other traders
	// also a chance to maybe reject parts of the order too. But we track
	// the reject to flag we have to restart match making and also to detect
	// potential DoS attacks from too many rejects.
	env.cancelTimerForTrader(reporter)

	// The isInvolved closure returns true if the reporter of a reject is
	// somehow involved in a matched order pair. This is the case if the
	// reporter is the asker, the bidder or the recipient of a sidecar
	// channel.
	isInvolved := func(pair matching.MatchedOrder) bool {
		isSidecar := pair.Details.Bid.IsSidecar
		recipientKey := pair.Details.Bid.MultiSigKey
		return pair.Asker.AccountKey == reporter ||
			pair.Bidder.AccountKey == reporter ||
			(isSidecar && recipientKey == reporter)
	}

	// The executor gets de-multiplexed messages from the RPC. That means if
	// a trader daemon has multiple accounts, we get the same message for
	// all those accounts. We first need to make sure the reporter is even
	// involved in the batch. If not, we simply skip adding an entry.
	var involvedOrders []matching.MatchedOrder
	for _, orderPair := range env.exeCtx.OrderBatch.Orders {
		if isInvolved(orderPair) {
			involvedOrders = append(involvedOrders, orderPair)
		}
	}
	if len(involvedOrders) == 0 {
		return
	}

	// If the reporter was involved, we simply store the rejected orders
	// with the reasons. The auctioneer will do the more detailed check and
	// rate limiting of the actual reject messages.
	env.rejectingTraders[reporter] = &OrderRejectMap{
		PartialRejects: msg.Orders,
	}

	log.Debugf("Trader %x rejected orders %v", msg.Src(),
		spew.Sdump(msg.Orders))
}

// executor is the primary goroutine of the BatchExecutor. It accepts new
// requests for batches, and then attempts to drive the state machine either to
// batch completion, or a terminal failure of the batch.
func (b *BatchExecutor) executor() {
	defer b.wg.Done()

	var (
		exeState = NoActiveBatch
		env      environment
	)

	for {
		select {
		// A new event has arrived, we'll now attempt to advance the
		// state machine until either we finish the batch, or end up at
		// the same start as before.
		case event := <-b.venueEvents:
			// If this is a message from a trader that's not part
			// of this current batch (or there is no current
			// batch), then we'll ignore it.
			if m, ok := event.(*msgRecvEvent); ok {
				src := m.msg.Src()
				if !env.traderPartOfBatch(src) {
					log.Warnf("Ignoring message from "+
						"trader=%x, not part of batch",
						src)
					continue
				}
			}

			var err error
		out:
			for {
				log.Infof("New incoming event: %v", event.Trigger())

				priorState := exeState

				exeState, env, err = b.stateStep(
					exeState, env, event,
				)
				if err != nil {
					env.resultChan <- &ExecutionResult{
						Err: err,
					}

					env.cancel()
					env = environment{}

					// Error was encountered during batch
					// execution, go back to NoActiveBatch
					// state.
					exeState = NoActiveBatch
					log.Infof("Error during batch "+
						"execution: %v. State "+
						"transition: %v -> %v", err,
						priorState, exeState)

					err := b.cfg.Store.UpdateExecutionState(
						exeState,
					)
					if err != nil {
						log.Errorf("unable to update "+
							"execution state: %v",
							err)
						break out
					}

					break out
				}

				log.Infof("State transition: %v -> %v",
					priorState, exeState)

				// We transitioned successfully, so we'll go
				// ahead and store that new state now.
				err := b.cfg.Store.UpdateExecutionState(exeState)
				if err != nil {
					log.Errorf("unable to update "+
						"execution state: %v", err)
					break out
				}

				switch {
				// If we end up at the same state that we
				// started, then we'll break out and wait for
				// another event.
				case exeState == priorState:
					break out

				// If this is the finalize state, then we're
				// done here, so we'll reset our env+state and
				// send the result to the caller.
				case exeState == BatchComplete:

					env.resultChan <- env.result

					env.cancel()
					env = environment{}

					// Now that the batch was completed, we
					// reset the state machine by
					// transitioning back to NoActiveState.
					exeState = NoActiveBatch
					log.Infof("Batch execution completed. "+
						"State transition: %v -> %v",
						BatchComplete, exeState)

					err := b.cfg.Store.UpdateExecutionState(
						exeState,
					)
					if err != nil {
						log.Errorf("unable to update "+
							"execution state: %v",
							err)
						break out
					}
					break out
				}
			}

		// A new batch has just arrived, we'll now attempt to construct
		// the batch execution transaction, sign it amongst all the
		// traders and finally commit the finalized bach to disk.
		case newBatch := <-b.newBatches:
			log.Infof("New OrderBatch(id=%x)",
				newBatch.exeCtx.BatchID)

			msgTimers := newMsgTimers(b.cfg.TraderMsgTimeout)
			env = newEnvironment(newBatch, msgTimers)

			// With a fresh environment constructed, we'll now send
			// ourselves a new trigger to kick off the batch
			// execution.
			b.wg.Add(1)
			go func() {
				defer b.wg.Done()

				// TODO(roasbeef): carry the env along with it?
				select {
				case b.venueEvents <- &newBatchEvent{
					batch: newBatch.exeCtx.OrderBatch,
				}:
				case <-b.quit:
					return
				}
			}()

		case <-b.quit:
			env.cancel()

			return
		}
	}
}

// Submit submits a new batch for execution to the main state machine.
func (b *BatchExecutor) Submit(exeCtx *batchtx.ExecutionContext) (
	chan *ExecutionResult, error) {

	exeReq := &executionReq{
		exeCtx: exeCtx,
		Result: make(chan *ExecutionResult, 1),
	}

	select {
	case b.newBatches <- exeReq:

	case <-b.quit:
		return nil, fmt.Errorf("executor shutting down")
	}

	return exeReq.Result, nil
}

// HandleTraderMsg sends a new message from the target to the main batch
// executor.
func (b *BatchExecutor) HandleTraderMsg(m TraderMsg) error {
	select {
	case b.venueEvents <- &msgRecvEvent{
		msg: m,
	}:
	case <-b.quit:
		return fmt.Errorf("venue shutting down")
	}

	return nil
}

// syncTraderState syncs the passed state with the resulting account state
// after the batch has been applied for all traders involved in the executed
// batch. The set of refreshed traders is returned.
func (b *BatchExecutor) syncTraderState(
	activeTraders map[matching.AccountID]*ActiveTrader) (
	map[matching.AccountID]*ActiveTrader, error) {

	log.Debugf("Syncing account state for %v traders", len(activeTraders))

	// For each active trader, we'll attempt to sync the state of our
	// in-memory representation with the resulting state after the batch
	// has been applied.
	//
	// TODO(roasbeef): optimize by only refreshing traders that were in a
	// recent batch? for account updates send them to the executor
	refreshed := make(map[matching.AccountID]*ActiveTrader, len(activeTraders))
	for acctID, trader := range activeTraders {
		// We don't refresh sidecar traders as they don't have an
		// account state themselves so there's nothing to load from the
		// database.
		if trader.IsSidecar {
			refreshed[acctID] = trader

			continue
		}

		acctKey, err := btcec.ParsePubKey(acctID[:])
		if err != nil {
			return nil, err
		}
		diskTraderAcct, err := b.cfg.Store.Account(
			context.Background(), acctKey, false,
		)
		if err != nil {
			return nil, err
		}

		// Now that we have the fresh trader state from disk, we'll
		// update our in-memory map with the latest state.
		refreshedTrader := matching.NewTraderFromAccount(
			diskTraderAcct,
		)
		refreshed[acctID] = &ActiveTrader{
			Trader:       &refreshedTrader,
			TokenID:      trader.TokenID,
			CommLine:     trader.CommLine,
			BatchVersion: trader.BatchVersion,
		}
	}

	return refreshed, nil
}
