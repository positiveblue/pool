package venue

import (
	"bytes"
	"fmt"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/pool/chaninfo"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightninglabs/subasta/venue/matching"
)

// batchVesion is the current version for batch transactions.
const batchVersion = uint32(orderT.CurrentVersion)

// multiplexMessage is a helper struct that holds all data that is multi-plexed
// from multiple venue traders to a single trader daemon/connection identified
// by an LSAT.
type multiplexMessage struct {
	commLine         *DuplexLine
	matchedOrders    map[orderT.Nonce][]*matching.MatchedOrder
	chargedAccounts  []matching.AccountDiff
	accountOutpoints []wire.OutPoint
}

// traderChannelInfo is a helper struct regarding a specific trader's channel
// information used for the creation of channel service lifetime packages.
type traderChannelInfo struct {
	matching.AccountID
	*batchtx.OrderOutput
	*chaninfo.ChannelInfo
}

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

	// exeCtx is the execution context, which contains the batch
	// transaction, and all other state we need to gather signatures for
	// the batch transaction itself.
	exeCtx *batchtx.ExecutionContext

	// resultChan is a channel that will be used to send the final results
	// of the batch.
	//
	// NOTE: This chan MUST be buffered.
	resultChan chan *ExecutionResult

	// result is a pointer to eventual result of this batch execution flow.
	//
	// TODO9roasbeef): wrap in method?
	result *ExecutionResult

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

	// matchingChanTrader tracks the first trader's information submitted
	// for each to be created channel as part of the batch. This is used
	// once the second trader submits their information in order to ensure
	// both parties have provided them accurately.
	//
	// NOTE: This will only be set after the BatchSigning state.
	matchingChanTrader map[wire.OutPoint]traderChannelInfo

	// lifetimePkgs contains the service level enforcement package for each
	// channel created as a result of the batch.
	//
	// NOTE: This will only be set after the BatchSigning state.
	lifetimePkgs []*chanenforcement.LifetimePackage

	// rejectingTraders is a map of all traders that sent a partial reject
	// message during the batch execution and the orders they reject. One or
	// more entries in this list means we have to start at matchmaking
	// again, which can be expensive in resources. That's why we also report
	// this map back to the auctioneer so it can track the number of
	// rejects.
	rejectingTraders map[matching.AccountID]OrderRejectMap

	// msgTimers is the set of messages timers we'll expose to the main
	// state machine to ensure traders send messages in time.
	*msgTimers

	quit chan struct{}
}

// newEnvironment creates a new environment given an executionReq and the
// current batch public key.
func newEnvironment(newBatch *executionReq,
	stallTimers *msgTimers) environment {

	env := environment{
		exeCtx:             newBatch.exeCtx,
		resultChan:         newBatch.Result,
		traders:            make(map[matching.AccountID]*ActiveTrader),
		traderToOrders:     make(map[matching.AccountID][]orderT.Nonce),
		acctWitnesses:      make(map[int]wire.TxWitness),
		matchingChanTrader: make(map[wire.OutPoint]traderChannelInfo),
		rejectingTraders:   make(map[matching.AccountID]OrderRejectMap),
		msgTimers:          stallTimers,
		quit:               make(chan struct{}),
	}
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
	accountDiffs := e.exeCtx.OrderBatch.FeeReport.AccountDiffs

	log.Infof("Sending OrderMatchPrepare to %v traders for batch=%x",
		len(e.traders), e.exeCtx.BatchID[:])

	// Each prepare message includes the fully serialized batch
	// transaction, so we'll encode this first before we proceed.
	var batchTxBuf bytes.Buffer
	if err := e.exeCtx.ExeTx.Serialize(&batchTxBuf); err != nil {
		return err
	}

	// What the venue sees as a trader is only one account of a trader's
	// daemon that might manage multiple accounts. The prepare message must
	// only be sent once per daemon/connection, otherwise the daemon will
	// only sign for the latest message. To identify which accounts (venue
	// "traders") belong together, we can use the LSAT ID.
	var (
		msgs = make(map[lsat.TokenID]*multiplexMessage)
		msg  *multiplexMessage
		ok   bool
	)

	// For each venue trader, we'll collect all the matched orders it
	// belongs to, along with the other required information.
	for _, trader := range e.traders {
		msg, ok = msgs[trader.TokenID]
		if !ok {
			// We haven't seen the daemon with this token yet.
			// Create a new multi-plex message now. All venue
			// traders for this token are hooked up to the same comm
			// line so we only need to pick one, so we pick the
			// first one we come across.
			msg = &multiplexMessage{
				commLine: trader.CommLine,
				matchedOrders: make(
					map[orderT.Nonce][]*matching.MatchedOrder,
				),
			}
			msgs[trader.TokenID] = msg
		}

		traderKey := trader.AccountKey
		msg.chargedAccounts = append(
			msg.chargedAccounts, accountDiffs[traderKey],
		)
		acctOutpoint, _ := e.exeCtx.AcctOutputForTrader(traderKey)
		msg.accountOutpoints = append(
			msg.accountOutpoints, acctOutpoint,
		)

		// For a given trader, we'll filter out all the orders that it
		// belongs to, and also update our map tracking the order for
		// each trader.
		e.traderToOrders = make(map[matching.AccountID][]orderT.Nonce)
		for _, order := range e.exeCtx.OrderBatch.Orders {
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

			msg.matchedOrders[orderNonce] = append(
				msg.matchedOrders[orderNonce], &order,
			)

			e.traderToOrders[traderKey] = append(
				e.traderToOrders[traderKey],
				orderNonce,
			)
		}
	}

	// Send out the batched messages now.
	for _, msg := range msgs {
		select {
		case msg.commLine.Send <- &PrepareMsg{
			MatchedOrders:    msg.matchedOrders,
			ClearingPrice:    e.exeCtx.OrderBatch.ClearingPrice,
			ChargedAccounts:  msg.chargedAccounts,
			AccountOutPoints: msg.accountOutpoints,
			ExecutionFee:     e.exeCtx.FeeSchedule,
			BatchTx:          batchTxBuf.Bytes(),
			FeeRate:          e.exeCtx.BatchFeeRate,
			BatchID:          e.exeCtx.BatchID,
			BatchVersion:     batchVersion,
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
		len(e.traders), e.exeCtx.BatchID[:])

	// What the venue sees as a trader is only one account of a trader's
	// daemon that might manage multiple accounts. The sign message must
	// only be sent once per daemon/connection, otherwise the daemon will
	// only sign for the latest message. To identify which accounts (venue
	// "traders") belong together, we can use the LSAT ID.
	msgs := make(map[lsat.TokenID]*DuplexLine)
	for _, trader := range e.traders {
		msgs[trader.TokenID] = trader.CommLine
	}

	for _, line := range msgs {
		select {
		case line.Send <- &SignBeginMsg{
			BatchID: e.exeCtx.BatchID,
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
		len(e.traders), e.exeCtx.BatchID[:])

	// What the venue sees as a trader is only one account of a trader's
	// daemon that might manage multiple accounts. The finalize message must
	// only be sent once per daemon/connection, otherwise the daemon will
	// only sign for the latest message. To identify which accounts (venue
	// "traders") belong together, we can use the LSAT ID.
	msgs := make(map[lsat.TokenID]*DuplexLine)
	for _, trader := range e.traders {
		msgs[trader.TokenID] = trader.CommLine
	}

	for _, line := range msgs {
		select {
		case line.Send <- &FinalizeMsg{
			BatchID:   e.exeCtx.BatchID,
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

	accountWitness := poolscript.SpendMultiSig(
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

// validateChanInfo ensures that the two traders behind the creation of a
// channel submit its information accurately. If any aspect does not match, an
// error is returned.
func (e *environment) validateChanInfo(trader matching.AccountID,
	chanOutput *batchtx.OrderOutput, chanInfo *chaninfo.ChannelInfo) error {

	// If we haven't seen a trader for this channel yet, cache it, and
	// perform validation once the second trader's information is received.
	matchingChanTrader, ok := e.matchingChanTrader[chanOutput.OutPoint]
	if !ok {
		e.matchingChanTrader[chanOutput.OutPoint] = traderChannelInfo{
			AccountID:   trader,
			OrderOutput: chanOutput,
			ChannelInfo: chanInfo,
		}
		return nil
	}

	// We'll need to determine a few parameters required for the lifetime
	// package which depend on which trader was responsible for the bid/ask.
	var (
		maturityHeight           uint32
		bidTrader, askTrader     matching.AccountID
		bidChanInfo, askChanInfo *chaninfo.ChannelInfo
	)
	if chanOutput.Order.Type() == orderT.TypeAsk {
		bidTrader, askTrader = matchingChanTrader.AccountID, trader
		bidChanInfo, askChanInfo = matchingChanTrader.ChannelInfo, chanInfo
		bid := matchingChanTrader.OrderOutput.Order.(*order.Bid)
		maturityHeight = bid.MinDuration()
	} else {
		bidTrader, askTrader = trader, matchingChanTrader.AccountID
		bidChanInfo, askChanInfo = chanInfo, matchingChanTrader.ChannelInfo
		maturityHeight = chanOutput.Order.(*order.Bid).MinDuration()
	}

	bidAccountKey, err := btcec.ParsePubKey(bidTrader[:], btcec.S256())
	if err != nil {
		return err
	}
	askAccountKey, err := btcec.ParsePubKey(askTrader[:], btcec.S256())
	if err != nil {
		return err
	}

	// Create the channel's lifetime package and cache it.
	//
	// TODO: set proper height hint.
	lifetimePkg, err := chanenforcement.NewLifetimePackage(
		chanOutput.OutPoint, chanOutput.TxOut.PkScript, 1,
		maturityHeight, askAccountKey, bidAccountKey, askChanInfo,
		bidChanInfo,
	)
	if err != nil {
		return err
	}
	e.lifetimePkgs = append(e.lifetimePkgs, lifetimePkg)

	return nil
}
