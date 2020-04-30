package venue

import (
	"bytes"
	"fmt"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/client/clmscript"
	orderT "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/venue/batchtx"
	"github.com/lightninglabs/agora/venue/matching"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// environment is the running state of the state machine. The state machine
// defined a state step method which takes the current state, a trigger and the
// environment to produce a next state and updated environment:
// stateStep(state, env, trigger) -> (state, env). The environment itself is
// the context of a new execution for the state machine. It is essentially a
// scratch pad for the main state machine.
type environment struct {
	// stopped indicates (1) if the env has already been reset.
	//
	// NOTE: This variable MUST be used atomically.
	stopped uint32

	// batchReq is the batch request that initiated the creation of this
	// environment.
	batchReq *executionReq

	// result is a pointer to eventual result of this batch execution flow.
	//
	// TODO9roasbeef): wrap in method?
	result *ExecutionResult

	// exeFee is the execution fee that was used to match this batch.
	exeFee orderT.FeeSchedule

	// feeRate is the target fee rate of the batch execution transaction.
	feeRate chainfee.SatPerKWeight

	// batchVersion is the current batch version.
	batchVersion uint32

	// batchID is the current batch ID.
	batchID [33]byte

	// batch is the batch itself that we aim to execute using the
	// environment.
	batch *matching.OrderBatch

	// masterAcct points to the master account state.
	//
	// NOTE: This will only be set after the NoActiveBatch state.
	masterAcct *account.Auctioneer

	// exeCtx is the execution context, which contains the batch
	// transaction, and all other state we need to gather signatures for
	// the batch transaction itself.
	//
	// NOTE: This will only be set after the NoActiveBatch state.
	exeCtx *batchtx.ExecutionContext

	// tempErr is an error that is set each time we need to transition to
	// the BatchTempError state.
	tempErr error

	// traders maps the account ID of a trader to its active trader struct.
	//
	// NOTE: This will only be set after the NoActiveBatch state.
	traders map[matching.AccountID]*ActiveTrader

	// traderToOrders maps the account ID of a trader to all the orders
	// that involve this trader. We'll use this to instruct a caller to
	// remove all orders by a trader due to an error.
	//
	// NOTE: This will only be set after the NoActiveBatch state.
	traderToOrders map[matching.AccountID][]orderT.Nonce

	// acctWitnesses maps the tx index in the batch execution transaction
	// to the witness for that trader's account.
	//
	// NOTE: This will only be set after the BatchSigning state.
	acctWitnesses map[int]wire.TxWitness

	// msgTimers is the set of messages timers we'll expose to the main
	// state machine to ensure traders send messages in time.
	*msgTimers

	quit chan struct{}
}

// newEnvironment creates a new environment given an executionReq and the
// current batch public key.
func newEnvironment(newBatch *executionReq, batchKey *btcec.PublicKey,
	stallTimers *msgTimers) environment {

	env := environment{
		batchReq:       newBatch,
		exeFee:         newBatch.feeSchedule,
		feeRate:        newBatch.batchFeeRate,
		batch:          newBatch.OrderBatch,
		traders:        make(map[matching.AccountID]*ActiveTrader),
		traderToOrders: make(map[matching.AccountID][]orderT.Nonce),
		acctWitnesses:  make(map[int]wire.TxWitness),
		msgTimers:      stallTimers,
		quit:           make(chan struct{}),
	}

	copy(env.batchID[:], batchKey.SerializeCompressed())

	return env
}

// cancel cancels all active goroutines in this environment.
func (e *environment) cancel() {
	if e.quit == nil {
		return
	}

	if !atomic.CompareAndSwapUint32(&e.stopped, 0, 1) {
		return
	}

	close(e.quit)

	e.msgTimers.resetAll()
}

// sendPrepareMsg send a prepare message to all active traders in the batch.
// Along the way, we'll also populate our traderToOrders map as well.
func (e *environment) sendPrepareMsg() error {
	accountDiffs := e.batch.FeeReport.AccountDiffs

	log.Infof("Sending OrderMatchPrepare to %v traders for batch=%x",
		len(e.traders), e.batchID[:])

	// Each prepare message includes the fully serialized batch
	// transaction, so we'll encode this first before we proceed.
	var batchTxBuf bytes.Buffer
	if err := e.exeCtx.ExeTx.Serialize(&batchTxBuf); err != nil {
		return err
	}

	// For each trader, we'll send out a prepare message that includes all
	// the matched orders it belongs to, along with the other required
	// information.
	for _, trader := range e.traders {
		traderKey := trader.AccountKey

		// For a given trader, we'll filter out all the orders that it
		// belongs to, and also update our map tracking the order for
		// each trader.
		matchedOrders := make(map[orderT.Nonce]*matching.MatchedOrder)
		e.traderToOrders = make(map[matching.AccountID][]orderT.Nonce)
		for _, order := range e.batch.Orders {
			order := order

			var orderNonce orderT.Nonce

			switch {
			case order.Asker.AccountKey == traderKey:
				orderNonce = order.Details.Ask.Nonce()

			case order.Bidder.AccountKey == traderKey:
				orderNonce = order.Details.Bid.Nonce()

			default:
				// The trader isn't a part of this order, so
				// we'll go onto the next one.
				continue
			}

			matchedOrders[orderNonce] = &order

			e.traderToOrders[traderKey] = append(
				e.traderToOrders[traderKey],
				orderNonce,
			)
		}

		select {
		case trader.CommLine.Send <- &PrepareMsg{
			AcctKey:       trader.AccountKey,
			MatchedOrders: matchedOrders,
			ClearingPrice: e.batch.ClearingPrice,
			ChargedAccounts: []matching.AccountDiff{
				accountDiffs[trader.AccountKey],
			},
			ExecutionFee: e.exeFee,
			BatchTx:      batchTxBuf.Bytes(),
			FeeRate:      e.feeRate,
			BatchID:      e.batchID,
			BatchVersion: e.batchVersion,
		}:
		case <-e.quit:
			return fmt.Errorf("environment exiting")
		}
	}

	return nil
}

// sendSignBeginMsg sends the SignBeginMsg to all active traders.
func (e *environment) sendSignBeginMsg() error {
	log.Infof("Sending OrderSignBegin to %v traders for batch=%x",
		len(e.traders), e.batchID[:])

	for _, trader := range e.traders {
		select {
		case trader.CommLine.Send <- &SignBeginMsg{
			BatchID: e.batchID,
			AcctKey: trader.AccountKey,
		}:
		case <-e.quit:
			return fmt.Errorf("environment exiting")
		}
	}

	return nil
}

// sendFinalizeMsg sends the finalize message to all active traders.
func (e *environment) sendFinalizeMsg(batchTxID chainhash.Hash) error {
	log.Infof("Sending Finalize to %v traders for batch=%x",
		len(e.traders), e.batchID[:])

	for _, trader := range e.traders {
		select {
		case trader.CommLine.Send <- &FinalizeMsg{
			AcctKey:   trader.AccountKey,
			BatchID:   e.batchID,
			BatchTxID: batchTxID,
		}:
		case <-e.quit:
			return fmt.Errorf("environment exiting")
		}
	}

	return nil
}

// validateAccountWitness attempts to validate a signature (by verifying a
// complete witness) for the account input of a given trader. If the trader
// isn't a part of the batch, or the signature is invalid, then we'll return an
// error.
func (e *environment) validateAccountWitness(witnessScript []byte,
	traderAcctInput *batchtx.AcctInput,
	traderSig, auctioneerSig *btcec.Signature) error {

	batchTx := e.exeCtx.ExeTx.Copy()

	inputIndex := int(traderAcctInput.InputIndex)

	accountWitness := clmscript.SpendMultiSig(
		witnessScript,
		append(traderSig.Serialize(), byte(txscript.SigHashAll)),
		append(auctioneerSig.Serialize(), byte(txscript.SigHashAll)),
	)
	batchTx.TxIn[int(traderAcctInput.InputIndex)].Witness = accountWitness

	vm, err := txscript.NewEngine(
		traderAcctInput.PrevOutput.PkScript, batchTx,
		inputIndex, txscript.StandardVerifyFlags,
		nil, nil, traderAcctInput.PrevOutput.Value,
	)
	if err != nil {
		return err
	}

	if err := vm.Execute(); err != nil {
		return err
	}

	e.acctWitnesses[inputIndex] = accountWitness

	return nil
}
