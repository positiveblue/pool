package venue

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/llm/chaninfo"
	"github.com/lightninglabs/llm/clmscript"
	"github.com/lightninglabs/llm/order"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/llm/terms"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// ExecutionMsg is an interface that describes a message sent outbound from the
// executor to a trader.
type ExecutionMsg interface {
	// Batch returns the target batch ID this message refers to.
	Batch() order.BatchID
}

// PrepareMsg is the first message the executor sends to all active traders.
// All traders should then send an TraderAcceptMsg in return.
type PrepareMsg struct {
	// MatchedOrders is the set of orders that a trader was matched with in
	// the batch for the trader. As we support partial matches, this maps
	// an order nonce to all the other orders it was matched with in the
	// batch.
	MatchedOrders map[orderT.Nonce][]*matching.MatchedOrder

	// ClearingPrice is the final clearing price of the batch.
	ClearingPrice orderT.FixedRatePremium

	// ChargedAccounts is the set of accounts that the trader used in this
	// batch.
	ChargedAccounts []matching.AccountDiff

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
	BatchID order.BatchID

	// BatchVersion is the batch version of this batch.
	BatchVersion uint32
}

// Batch returns the target batch ID this message refers to.
//
// NOTE: This method is a part of the ExecutionMsg interface.
func (m *PrepareMsg) Batch() order.BatchID {
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
	BatchID order.BatchID
}

// Batch returns the target batch ID this message refers to.
//
// NOTE: This method is a part of the ExecutionMsg interface.
func (m *SignBeginMsg) Batch() order.BatchID {
	return m.BatchID
}

// A compile-time constraint to ensure SignBeginMsg meets the ExecutionMsg
// interface.
var _ ExecutionMsg = (*SignBeginMsg)(nil)

// FinalizeMsg is the final message sent in the execution flow. We send it once
// the batch has valid signatures, and have been committed to disk.
type FinalizeMsg struct {
	// BatchID is the batch ID to be finalized.
	BatchID order.BatchID

	// BatchTxID is the serialized batch txid.
	BatchTxID chainhash.Hash
}

// Batch returns the target batch ID this message refers to.
//
// NOTE: This method is a part of the ExecutionMsg interface.
func (m *FinalizeMsg) Batch() order.BatchID {
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
	BatchID order.BatchID

	// Trader is the trader that's accepting this batch.
	Trader *ActiveTrader

	// Orders is the set of orders they wish to accept.
	//
	// TODO(roasbeef): remove? no way to accept only part of them, as they
	// should accept if all are valid
	Orders []order.Nonce
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
	BatchID order.BatchID

	// Trader is the trader that's rejecting this batch.
	Trader *ActiveTrader

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
	Sigs map[string]*btcec.Signature

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
	BatchID order.BatchID

	// Batch is the raw batch itself.
	Batch *matching.OrderBatch

	// MasterAccountDiff points to a diff of the master account state,
	// which described how the master account changed as a result of this
	// batch.
	MasterAccountDiff *batchtx.MasterAccountState

	// BatchTx is the finalized, fully signed batch transaction.
	BatchTx *wire.MsgTx

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
	// OrderBatch is the target batch to be executed.
	*matching.OrderBatch

	// feeSchedule is the fee schedule that was used to construct this
	// batch.
	feeSchedule terms.FeeSchedule

	// batchFeeRate is the target fee rate of the batch execution
	// transaction.
	batchFeeRate chainfee.SatPerKWeight

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

// BatchExecutor is the primary state machine that executes a cleared batch.
// Execution entails orchestrating the creation of hall the channels purchased
// in the batch, and also gathering the signatures of all the input in the
// batch transaction needed to broadcast it.
type BatchExecutor struct {
	started uint32 // To be used atomically.
	stopped uint32 // To be used atomically.

	// activeTraders is a map of all the current active traders. An active
	// trader is one that's online and has a live communication channel
	// with the BatchExecutor.
	activeTraders map[matching.AccountID]*ActiveTrader

	// newBatches is a channel used to accept new incoming requests to
	// execute a batch.
	newBatches chan *executionReq

	// venueEvents is where any new event related to the current batch
	// execution is sent.
	venueEvents chan EventTrigger

	// traderMsgTimeout is the amount of time we'll wait for a trader to
	// send us an expected message.
	traderMsgTimeout time.Duration

	sync.RWMutex

	store ExecutorStore

	batchStorer BatchStorer

	accountWatcher AccountWatcher

	signer lndclient.SignerClient

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewBatchExecutor creates a new BatchExecutor given the database, and a
// signer that's able to sign for the master account output.
func NewBatchExecutor(store ExecutorStore, signer lndclient.SignerClient,
	traderMsgTimeout time.Duration, batchStorer BatchStorer,
	accountWatcher AccountWatcher) *BatchExecutor {

	return &BatchExecutor{
		quit:             make(chan struct{}),
		activeTraders:    make(map[matching.AccountID]*ActiveTrader),
		signer:           signer,
		newBatches:       make(chan *executionReq),
		store:            store,
		venueEvents:      make(chan EventTrigger),
		traderMsgTimeout: traderMsgTimeout,
		batchStorer:      batchStorer,
		accountWatcher:   accountWatcher,
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
func (b *BatchExecutor) validateTradersOnline(batch *matching.OrderBatch) error {
	offlineTraders := make(map[matching.AccountID]struct{})
	offlineNonces := make(map[orderT.Nonce]struct{})

	// We'll run through all the active orders in this batch, if either
	// trader isn't online, then we'll mark both the order nonce and the
	// trader.
	for _, order := range batch.Orders {

		if _, ok := b.activeTraders[order.Asker.AccountKey]; !ok {
			offlineTraders[order.Asker.AccountKey] = struct{}{}
			offlineNonces[order.Details.Ask.Nonce()] = struct{}{}
		}

		if _, ok := b.activeTraders[order.Bidder.AccountKey]; !ok {
			offlineTraders[order.Bidder.AccountKey] = struct{}{}
			offlineNonces[order.Details.Bid.Nonce()] = struct{}{}
		}

	}

	// If no traders were offline, then we're good to go!
	if len(offlineTraders) == 0 {
		return nil
	}

	log.Warnf("Cancelling batch, offline traders: %v",
		spew.Sdump(offlineTraders))

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
	traderAcctInput *batchtx.AcctInput) (*btcec.Signature, []byte, error) {

	log.Debugf("Signing account input for trader=%x", trader.AccountKey[:])

	// First, we'll grab real structs for the trader's account key, and the
	// batch key for the last batch they participated in.
	traderKey, err := btcec.ParsePubKey(trader.AccountKey[:], btcec.S256())
	if err != nil {
		return nil, nil, err
	}
	batchKey, err := btcec.ParsePubKey(trader.BatchKey[:], btcec.S256())
	if err != nil {
		return nil, nil, err
	}

	// With the keys obtained, we'll now derive the tweak we need to obtain
	// our private key, as well as the full witness script of the trader's
	// account output.
	auctioneerKeyTweak := clmscript.AuctioneerKeyTweak(
		traderKey, masterAcct.AuctioneerKey.PubKey,
		batchKey, trader.VenueSecret,
	)
	witnessScript, err := clmscript.AccountWitnessScript(
		trader.AccountExpiry, traderKey,
		masterAcct.AuctioneerKey.PubKey, batchKey, trader.VenueSecret,
	)
	if err != nil {
		return nil, nil, err
	}

	// With all the information obtained, we'll now generate our half of
	// the multi-sig.
	signDesc := &input.SignDescriptor{
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
		SigHashes:     txscript.NewTxSigHashes(batchTx),
	}
	auctioneerSigs, err := b.signer.SignOutputRaw(
		context.Background(), batchTx, []*input.SignDescriptor{signDesc},
	)
	if err != nil {
		return nil, nil, err
	}

	sig, err := btcec.ParseDERSignature(auctioneerSigs[0], btcec.S256())
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
		err := b.validateTradersOnline(env.batch)
		if err != nil {
			env.tempErr = err
			return BatchTempError, env, nil
		}

		b.Lock()

		// To ensure that we have a fully up to date view of the state
		// of each of the trader's, we'll sync what we have here, with
		// the set of traders on disk.
		if err := b.syncTraderState(); err != nil {
			b.Unlock()
			return 0, env, err
		}

		// Now that we know this batch is valid, we'll populate the set
		// of active traders in the environment so we can begin our
		// message passing phase.
		for trader := range env.batch.FeeReport.AccountDiffs {
			env.traders[trader] = b.activeTraders[trader]
		}

		b.Unlock()

		// Now that we know the batch is valid, we'll construct the
		// execution context we need to push things forward, which
		// includes the final batch execution transaction.
		masterAcct, err := b.store.FetchAuctioneerAccount(ctxb)
		if err != nil {
			return 0, env, err
		}
		env.masterAcct = masterAcct

		// When we create this master account state, we'll ensure that
		// we provide the "next" batch key, as this is what will be
		// used to create the outputs in the BET.
		masterAcctState := &batchtx.MasterAccountState{
			PriorPoint:     masterAcct.OutPoint,
			AccountBalance: masterAcct.Balance,
		}
		copy(
			masterAcctState.AuctioneerKey[:],
			masterAcct.AuctioneerKey.PubKey.SerializeCompressed(),
		)

		batchKeyPub, err := btcec.ParsePubKey(
			env.batchID[:], btcec.S256(),
		)
		if err != nil {
			return 0, env, err
		}
		nextBatchKey := clmscript.IncrementKey(batchKeyPub)
		copy(
			masterAcctState.BatchKey[:],
			nextBatchKey.SerializeCompressed(),
		)

		env.exeCtx, err = batchtx.New(
			env.batch, masterAcctState, env.batchFeeRate,
		)
		if err != nil {
			return 0, env, err
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
			// cancel the timer as they've behaving as expected.
			//
			// TODO(roasbeef): if it's a reject message
			//  * hard bail out?
			//  * instruct to remove then try again? (as could be
			//    malicious)
			msgRecv := event.(*msgRecvEvent)
			_, ok := msgRecv.msg.(*TraderAcceptMsg)
			if !ok {
				log.Errorf("expected "+
					"OrdrMatchAccept, instead got %T",
					msgRecv.msg)

				return PrepareSent, env, nil
			}

			log.Debugf("Received OrderMatchAccept from trader=%x",
				msgRecv.msg.Src())

			env.cancelTimerForTrader(msgRecv.msg.Src())

			// If there're no more active timers, then we can
			// advance to the next phase, as we received all the
			// intended messages.
			if env.noTimersActive() {
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
			// the user specified a witness for their account. If a
			// trader sent the wrong message, then we'll treat that
			// as a no-op to ensure a single trader can't disrupt
			// the entire state machine.
			signMsg, ok := msgRecv.msg.(*TraderSignMsg)
			if !ok {
				log.Errorf("expected "+
					"OrderMatchSign, instead got %T",
					msgRecv.msg)

				return BatchSigning, env, nil
			}

			log.Debugf("Received OrderMatchSign from trader=%x",
				msgRecv.msg.Src())

			// As we've received the expected message from this
			// trader, we'll cancel it timer now, draining out the
			// channel if it already ticked.
			env.cancelTimerForTrader(src)

			b.RLock()
			trader := b.activeTraders[src]
			b.RUnlock()
			acctSig, ok := signMsg.Sigs[hex.EncodeToString(src[:])]
			if !ok {
				return 0, env, fmt.Errorf("account witness "+
					"for %x not found", src)
			}

			// As we want to fully validate the witness, we'll
			// generate our own signature for his input to ensure
			// we can properly spend it with broadcast of the batch
			// transaction.
			traderAcctInput, ok := env.exeCtx.AcctInputForTrader(
				trader.AccountKey,
			)
			if !ok {
				return 0, env, fmt.Errorf("unable to find "+
					"input for trader %x",
					trader.AccountKey[:])
			}
			auctioneerSig, witnessScript, err := b.signAcctInput(
				env.masterAcct, trader, env.exeCtx.ExeTx,
				traderAcctInput,
			)
			if err != nil {
				return 0, env, fmt.Errorf("unable to "+
					"generate auctioneer sig: %v", err)
			}

			// We'll now full validate the witness to ensure we'll
			// be able to broadcast the funding transaction. If the
			// trader gives us an invalid witness, then we'll
			// return an error identifying them as rogue.
			err = env.validateAccountWitness(
				witnessScript, traderAcctInput,
				acctSig, auctioneerSig,
			)
			if err != nil {
				log.Warnf("trader=%x sent invalid "+
					"signature: %v", src[:], err)

				orderNonces := env.traderToOrders[src]
				env.tempErr = &ErrInvalidWitness{
					VerifyErr:   err,
					Trader:      src,
					OrderNonces: orderNonces,
				}
				return BatchTempError, env, nil
			}

			// With the witness validated, we'll also validate the
			// channel info submitted with the matched trader to
			// ensure we have the correct base point keys to enforce
			// the channel's service level agreement.
			chanOutputs, _ := env.exeCtx.ChanOutputsForTrader(src)
			for _, chanOutput := range chanOutputs {
				chanPoint := chanOutput.OutPoint
				chanInfo, ok := signMsg.ChannelInfos[chanPoint]
				if !ok {
					// The trader didn't provide information
					// for one of their channels, so we'll
					// ignore their orders and retry the
					// batch.
					orderNonces := env.traderToOrders[src]
					env.tempErr = &ErrMissingChannelInfo{
						Trader:       src,
						ChannelPoint: chanPoint,
						OrderNonces:  orderNonces,
					}
					log.Warn(env.tempErr)
					return BatchTempError, env, nil
				}

				err := env.validateChanInfo(
					src, chanOutput, chanInfo,
				)
				if err != nil {
					// If the information submitted between
					// both parties of the to be created
					// channel don't match, then one must be
					// lying. The bidder doesn't have any
					// incentive to lie, but we cannot be
					// sure, so we'll punish both anyway.
					matchingTrader := env.
						matchingChanTrader[chanPoint].
						AccountID
					orders := env.traderToOrders[src]
					orders = append(
						orders,
						env.traderToOrders[matchingTrader]...,
					)
					env.tempErr = &ErrNonMatchingChannelInfo{
						Err:          err,
						Trader1:      src,
						Trader2:      matchingTrader,
						ChannelPoint: chanPoint,
						OrderNonces:  orders,
					}
					log.Warn(env.tempErr)
					return BatchTempError, env, nil
				}
			}

			// If we have all the witnesses we need, then we'll
			// proceed to the batch finalize phase.
			if len(env.acctWitnesses) == len(env.traders) {
				log.Infof("Received valid signatures from all " +
					"traders, finalizing batch")

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
		auctioneerWitness, err := env.masterAcct.AccountWitness(
			b.signer, batchTx, auctioneerInputIndex,
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

		// With the batch now finalized, we'll commit the batch to
		// disk, as we're now able to broadcast a valid multi-funding
		// transaction.
		env.result = &ExecutionResult{
			BatchID:           env.batchID,
			Batch:             env.batch,
			MasterAccountDiff: exeCtx.MasterAccountDiff,
			BatchTx:           batchTx,
			LifetimePackages:  env.lifetimePkgs,
		}
		if err := b.batchStorer.Store(ctxb, env.result); err != nil {
			log.Errorf("Failed to store batch: %v", err)
			return 0, env, err
		}

		log.Infof("Batch(%x) finalized and committed!", env.batchID[:])

		diffs := env.batch.FeeReport.AccountDiffs
		matchedAccounts := make([][33]byte, 0, len(diffs))
		for id := range diffs {
			matchedAccounts = append(matchedAccounts, id)
		}
		err = b.accountWatcher.WatchMatchedAccounts(
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

// executor is the primary goroutine of the BatchExecutor. It accepts new
// requests for batches, and then attempts to drive the state machine either to
// batch completion, or a terminal failure of the batch.
func (b *BatchExecutor) executor() {
	defer b.wg.Done()

	var (
		exeState ExecutionState
		env      environment
	)

	for {
		select {

		// A new event has arrived, we'll now attempt to advance the
		// state machine until either we finish the batch, or end up at
		// the same start as before.
		case event := <-b.venueEvents:

			var err error
		out:
			for {
				log.Infof("New incoming event: %v", event.Trigger())

				priorState := exeState

				exeState, env, err = b.stateStep(
					exeState, env, event,
				)
				if err != nil {
					env.batchReq.Result <- &ExecutionResult{
						Err: err,
					}

					env.cancel()
					env = environment{}
					exeState = NoActiveBatch
					break out
				}

				log.Infof("State transition: %v -> %v",
					priorState, exeState)

				// We transitioned successfully, so we'll go
				// ahead and store that new state now.
				err := b.store.UpdateExecutionState(exeState)
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

					env.batchReq.Result <- env.result

					env.cancel()
					env = environment{}
					exeState = NoActiveBatch

					break out
				}
			}

		// A new batch has just arrived, we'll now attempt to construct
		// the batch execution transaction, sign it amongst all the
		// traders and finally commit the finalized bach to disk.
		case newBatch := <-b.newBatches:
			// TODO(guggero): Use timeout and/or cancel?
			batchCtx := context.Background()

			// The batch ID is always the currently stored ID. We
			// can try to match the same batch multiple times with
			// traders bailing out and us trying again. But we
			// always need to use the same ID. Only after clearing
			// the batch, the ID is increased and stored as the ID
			// to use for the next batch.
			batchKey, err := b.store.BatchKey(batchCtx)
			if err != nil {
				log.Errorf("Nope, couldn't get batch key!")

				newBatch.Result <- &ExecutionResult{
					Err: err,
				}

				continue
			}

			log.Infof("New OrderBatch(id=%x)",
				batchKey.SerializeCompressed())

			exeState = NoActiveBatch

			msgTimers := newMsgTimers(b.traderMsgTimeout)
			env = newEnvironment(
				newBatch, batchKey, msgTimers,
			)

			// With a fresh environment constructed, we'll now send
			// ourselves a new trigger to kick off the batch
			// execution.
			b.wg.Add(1)
			go func() {
				defer b.wg.Done()

				// TODO(roasbeef): carry the env along with it?
				select {
				case b.venueEvents <- &newBatchEvent{
					batch: newBatch.OrderBatch,
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
func (b *BatchExecutor) Submit(batch *matching.OrderBatch,
	feeSchedule terms.FeeSchedule,
	batchFeeRate chainfee.SatPerKWeight) (chan *ExecutionResult, error) {

	exeReq := &executionReq{
		OrderBatch:   batch,
		feeSchedule:  feeSchedule,
		batchFeeRate: batchFeeRate,
		Result:       make(chan *ExecutionResult, 1),
	}

	select {
	case b.newBatches <- exeReq:

	case <-b.quit:
		return nil, fmt.Errorf("executor shutting down")
	}

	return exeReq.Result, nil
}

// RegisterTrader registers a new trader as being active. An active traders is
// eligible to join execution of a batch that they're a part of.
func (b *BatchExecutor) RegisterTrader(t *ActiveTrader) error {
	b.Lock()
	defer b.Unlock()

	_, ok := b.activeTraders[t.AccountKey]
	if ok {
		return fmt.Errorf("trader %x already registered",
			t.AccountKey)
	}
	b.activeTraders[t.AccountKey] = t

	log.Infof("Registering new trader: %x", t.AccountKey[:])

	return nil
}

// UnregisterTrader removes a registered trader from the batch.
//
// TODO(roasbeef): job of the caller to unregister the traders to ensure we
// don't loop in the state machine
func (b *BatchExecutor) UnregisterTrader(t *ActiveTrader) error {
	b.Lock()
	defer b.Unlock()

	delete(b.activeTraders, t.AccountKey)

	log.Infof("Disconnecting trader: %x", t.AccountKey[:])

	// TODO(roasbeef): client always removes traders?

	return nil
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
// batch.
//
// NOTE: The write lock MUST be held when calling this method.
func (b *BatchExecutor) syncTraderState() error {
	log.Debugf("Syncing account state for %v traders", len(b.activeTraders))

	// For each active trader, we'll attempt to sync the state of our
	// in-memory representation with the resulting state after the batch
	// has been applied.
	//
	// TODO(roasbeef): optimize by only refreshing traders that were in a
	// recent batch? for account updates send them to the executor
	for acctID := range b.activeTraders {
		acctKey, err := btcec.ParsePubKey(acctID[:], btcec.S256())
		if err != nil {
			return err
		}
		diskTraderAcct, err := b.store.Account(
			context.Background(), acctKey, false,
		)
		if err != nil {
			return err
		}

		// Now that we have the fresh trader state from disk, we'll
		// update our in-memory map with the latest state.
		refreshedTrader := matching.NewTraderFromAccount(
			diskTraderAcct,
		)
		b.activeTraders[acctID].Trader = &refreshedTrader
	}

	return nil
}
