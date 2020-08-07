package batchtx

import (
	"fmt"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/btcsuite/btcutil/txsort"
	"github.com/lightninglabs/llm/clmscript"
	"github.com/lightninglabs/llm/order"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// OrderOutput represents an executed order within the batch execution
// transaction. In order words, this output is the created channel from a
// bid+ask order.
type OrderOutput struct {
	// OutPoint is the outpoint of the order in the batch execution
	// transaction.
	OutPoint wire.OutPoint

	// TxOut is the raw output from the batch execution transaction.
	TxOut *wire.TxOut

	// Order is one of the orders (either the bid or ask) that produced this
	// output.
	Order order.Order
}

// AcctInput stores information about the input spending a given trader's
// account.
type AcctInput struct {
	// InputIndex the input index in the batch execution transaction.
	InputIndex uint32

	// InputPoint is the full outpoint (of the trader's old account)
	// referenced within the batch execution.
	InputPoint wire.OutPoint

	// PrevOutput is the previous output that we're spending. This includes
	// the prior account balance value, and also the prior script.
	PrevOutput wire.TxOut
}

// MasterAccountState is a struct that describes how the master account changes
// from one batch to another. We'll use this to ensure we construct the new
// master account output on the next batch.
//
// TODO(roasbeef): in the accounts folder instead?
type MasterAccountState struct {
	// PriorPoint is the prior outpoint of the master account output.
	PriorPoint wire.OutPoint

	// OutPoint is the new outpoint of the master account output. This
	// should be used to track the master account after this batch is
	// successfully executed.
	OutPoint *wire.OutPoint

	// InputIndex is the input on the batch execution transaction that
	// spends the PriorPoint.
	InputIndex int

	// AccountBalance is the balance of the master account after
	AccountBalance btcutil.Amount

	// BatchKey is the batch key used to derive the tweaked auctioneer key
	// for this batch. This should be "one more" than the BatchKey in the
	// prior diff within this struct.
	BatchKey [33]byte

	// AuctioneerKey is the main key for the auctioneer, this never
	// changes, yet is threaded along in this diff for convenience.
	AuctioneerKey [33]byte
}

// AccountScript derives the auctioneer's account script
//
// TODO(roasbeef): post tapscript, all can appear uniform w/ their spends ;)
func (m *MasterAccountState) AccountScript() ([]byte, error) {
	batchKey, err := btcec.ParsePubKey(
		m.BatchKey[:], btcec.S256(),
	)
	if err != nil {
		return nil, err
	}
	auctioneerKey, err := btcec.ParsePubKey(
		m.AuctioneerKey[:], btcec.S256(),
	)
	if err != nil {
		return nil, err
	}

	return account.AuctioneerAccountScript(
		batchKey, auctioneerKey,
	)
}

// ExecutionContext contains all the information needed to actually execute an
// order batch. Execution requires a number of related pieces of data such as
// the outputs for each order, and the execution transaction itself. Once this
// execution transaction has been fully signed, a batch is complete, and the
// next batch period can start.
type ExecutionContext struct {
	// ExeTx is the unsigned execution transaction.
	ExeTx *wire.MsgTx

	// MasterAccountDiff is a diff that describes the prior and current
	// state of the auctioneer's master account output.
	MasterAccountDiff *MasterAccountState

	// OrderBatch is a pointer to the batch that this context attempts to
	// execute.
	OrderBatch *matching.OrderBatch

	// orderIndex maps an order nonce to the output within the batch
	// execution transaction that executes the order.
	orderIndex map[orderT.Nonce][]*OrderOutput

	// traderIndex maps a trader's account ID to set of outputs that create
	// channels that involve the trader.
	traderIndex map[matching.AccountID][]*OrderOutput

	// accountIndex maps a trader's account to the output that re-creates
	// its account in the batch.
	//
	// NOTE: If a trader's account is fully consumed in this batch, then
	// they won't have an entry in this map.
	accountIndex map[matching.AccountID]wire.OutPoint

	// acctInputIndex maps a trader's account ID to information about their
	// input within the batch execution transaction.
	acctInputIndex map[matching.AccountID]*AcctInput
}

// indexBatchTx is a helper method that indexes a batch transaction given a
// number of auxiliary indexes built up during batch transaction construction.
// This method also returns the input index of the auctioneer's account.
func (e *ExecutionContext) indexBatchTx(
	scriptToOrders map[string][2]order.Order,
	traderAccounts map[matching.AccountID]*wire.TxOut,
	ordersForTrader map[matching.AccountID]map[orderT.Nonce]struct{},
	inputToAcct map[wire.OutPoint]matching.AccountID) (int, error) {

	txHash := e.ExeTx.TxHash()

	// First, we'll map each order to the proper account output using an
	// auxiliary index that maps the script to the nonce.
	for stringScript, orders := range scriptToOrders {
		fundingScript := []byte(stringScript)

		found, outputIndex := input.FindScriptOutputIndex(
			e.ExeTx, fundingScript,
		)
		if !found {
			return 0, fmt.Errorf("unable to find funding script "+
				"for order pair %v/%v", orders[0].Nonce(),
				orders[1].Nonce())
		}

		// TODO(roasbeef): de-dup? pointers
		nonce1, nonce2 := orders[0].Nonce(), orders[1].Nonce()
		e.orderIndex[nonce1] = append(e.orderIndex[nonce1], &OrderOutput{
			OutPoint: wire.OutPoint{
				Hash:  txHash,
				Index: outputIndex,
			},
			TxOut: e.ExeTx.TxOut[outputIndex],
			Order: orders[0],
		})
		e.orderIndex[nonce2] = append(e.orderIndex[nonce2], &OrderOutput{
			OutPoint: wire.OutPoint{
				Hash:  txHash,
				Index: outputIndex,
			},
			TxOut: e.ExeTx.TxOut[outputIndex],
			Order: orders[1],
		})

	}

	// Finally, we'll populate the trader index (trader to funding
	// outputs), and the account index (trader to new account output).
	for acctID, orderNonces := range ordersForTrader {
		for orderNonce := range orderNonces {
			orderOutput, ok := e.orderIndex[orderNonce]
			if !ok {
				return 0, fmt.Errorf("unable to find order "+
					"for: %x", orderNonce[:])
			}

			e.traderIndex[acctID] = append(
				e.traderIndex[acctID], orderOutput...,
			)
		}
	}
	for acctID, txOut := range traderAccounts {
		found, outputIndex := input.FindScriptOutputIndex(
			e.ExeTx, txOut.PkScript,
		)
		if !found {
			return 0, fmt.Errorf("unable to find script for %x",
				acctID[:])
		}

		e.accountIndex[acctID] = wire.OutPoint{
			Hash:  txHash,
			Index: outputIndex,
		}
	}

	// Finally, we'll populate the final component (the input index) of the
	// acctInputIndex map.
	var auctioneerIndex int
	for i, txIn := range e.ExeTx.TxIn {
		acctID, ok := inputToAcct[txIn.PreviousOutPoint]
		if !ok {
			// We'll also have the auctioneer's input and any other
			// inputs that we're piggy backing on, so we won't
			// always find an entry in the map.
			auctioneerIndex = i
			continue
		}

		e.acctInputIndex[acctID].InputIndex = uint32(i)
	}

	return auctioneerIndex, nil
}

// assembleBatchTx attempts to assemble a batch transaction that is able to
// execute the passed orderBatch. We also accept the starting fee rate of the
// batch transaction, along with a description of the prior and next state of
// the auctioneer's account.
func (e *ExecutionContext) assembleBatchTx(orderBatch *matching.OrderBatch,
	mAccountDiff *MasterAccountState, feeRate chainfee.SatPerKWeight) error {

	e.ExeTx = wire.NewMsgTx(2)

	auctioneerKey, err := btcec.ParsePubKey(
		mAccountDiff.AuctioneerKey[:], btcec.S256(),
	)
	if err != nil {
		return err
	}

	// First, we'll add all the necessary inputs: for each trader involved
	// in this batch, we reference an account input on chain, and then also
	// add our master account input as well.
	inputToAcct := make(map[wire.OutPoint]matching.AccountID)
	for acctID, trader := range orderBatch.FeeReport.AccountDiffs {
		acctPreBatch := trader.StartingState
		prevOutPoint := acctPreBatch.AccountOutPoint
		e.ExeTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: prevOutPoint,
		})

		acctKey, err := btcec.ParsePubKey(
			acctPreBatch.AccountKey[:], btcec.S256(),
		)
		if err != nil {
			return err
		}
		batchKey, err := btcec.ParsePubKey(
			acctPreBatch.BatchKey[:], btcec.S256(),
		)
		if err != nil {
			return err
		}
		accountScript, err := clmscript.AccountScript(
			acctPreBatch.AccountExpiry, acctKey, auctioneerKey,
			batchKey, acctPreBatch.VenueSecret,
		)
		if err != nil {
			return err
		}

		e.acctInputIndex[acctID] = &AcctInput{
			InputPoint: prevOutPoint,
			PrevOutput: wire.TxOut{
				Value:    int64(acctPreBatch.AccountBalance),
				PkScript: accountScript,
			},
		}

		inputToAcct[prevOutPoint] = acctID
	}
	e.ExeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: mAccountDiff.PriorPoint,
	})

	// As we go estimate and count the chain fees paid by the traders.
	txFeeEstimator := newChainFeeEstimator(orderBatch.Orders, feeRate)
	var totalTraderFees btcutil.Amount

	// Next, we'll do our first pass amongst the outputs to add the new
	// outputs for each account involved. The value of these outputs are
	// the ending balance for each account after applying this batch. Along
	// the way, we'll also generate an index from the multi-sig script to
	// the order nonce for the account.
	traderAccounts := make(map[matching.AccountID]*wire.TxOut)
	var traderOuts int
	for acctID, trader := range orderBatch.FeeReport.AccountDiffs {
		// We start by finding the chain fee that should be paid by
		// this trader, and subtracting that value from their ending
		// balance.
		traderFee := txFeeEstimator.EstimateTraderFee(acctID)

		// The trader should always have enough balance to cover the on
		// chain fees, otherwise we shouldn't have accepted the order
		// into this batch.
		if traderFee > trader.EndingBalance {
			return fmt.Errorf("account %x only had balance %v to "+
				"cover chain fee %v", acctID,
				trader.EndingBalance, traderFee)
		}

		// Subtract the chain fee from their balance.
		trader.EndingBalance -= traderFee
		totalTraderFees += traderFee

		// If the ending balance is above the dust limit, it will be
		// recreated. If not the account will be closed.
		if trader.EndingBalance >= orderT.MinNoDustAccountSize {
			// Using the set params of the account, and the
			// information within the account key, we'll create a
			// new output to place within our transaction.
			acctParams := trader.StartingState
			acctKey, err := btcec.ParsePubKey(
				acctParams.AccountKey[:], btcec.S256(),
			)
			if err != nil {
				return err
			}
			batchKey, err := btcec.ParsePubKey(
				acctParams.NextBatchKey[:], btcec.S256(),
			)
			if err != nil {
				return err
			}
			accountScript, err := clmscript.AccountScript(
				acctParams.AccountExpiry, acctKey,
				auctioneerKey, batchKey, acctParams.VenueSecret,
			)
			if err != nil {
				return err
			}
			traderAccountTxOut := &wire.TxOut{
				Value:    int64(trader.EndingBalance),
				PkScript: accountScript,
			}

			// With the output created, we'll update our account
			// index for our later passes, and also attach the
			// output directly to the BET (batch execution
			// transaction)
			traderAccounts[acctID] = traderAccountTxOut
			trader.RecreatedOutput = traderAccountTxOut
			e.ExeTx.AddTxOut(traderAccountTxOut)
			traderOuts++
		} else {
			// If the trader's balance was below dust, it will all
			// go to chain fees. Note that we keep the ending
			// balance value intact, as that is used to check the
			// calculation by the traders.
			totalTraderFees += trader.EndingBalance
		}

		orderBatch.FeeReport.AccountDiffs[acctID] = trader
	}

	// Now that the account outputs have been created, we'll proceed to
	// adding the necessary outputs to create all channels purchased in
	// this batch.
	scriptToOrders := make(map[string][2]order.Order)
	ordersForTrader := make(map[matching.AccountID]map[orderT.Nonce]struct{})
	for _, matchedOrder := range orderBatch.Orders {
		// First using the relevant channel details of the order, we'll
		// construct the funding output that will create the channel
		// for both sides.
		orderDetails := matchedOrder.Details
		bid := orderDetails.Bid
		ask := orderDetails.Ask
		_, fundingOutput, err := input.GenFundingPkScript(
			bid.MultiSigKey[:], ask.MultiSigKey[:],
			int64(orderDetails.Quote.TotalSatsCleared),
		)
		if err != nil {
			return err
		}

		// With the funding output assembled, we'll tack it onto the
		// ExeTx.
		e.ExeTx.AddTxOut(fundingOutput)

		// We'll also populate our other index so we can properly index
		// the entire batch execution transaction below.
		chanScript := fundingOutput.PkScript
		bidNonce := bid.Nonce()
		askNonce := ask.Nonce()

		scriptToOrders[string(chanScript)] = [2]order.Order{
			bid, ask,
		}

		askerOrders, ok := ordersForTrader[matchedOrder.Asker.AccountKey]
		if !ok {
			askerOrders = make(map[orderT.Nonce]struct{})
			ordersForTrader[matchedOrder.Asker.AccountKey] = askerOrders
		}
		askerOrders[askNonce] = struct{}{}

		bidderOrders, ok := ordersForTrader[matchedOrder.Bidder.AccountKey]
		if !ok {
			bidderOrders = make(map[orderT.Nonce]struct{})
			ordersForTrader[matchedOrder.Bidder.AccountKey] = bidderOrders
		}
		bidderOrders[bidNonce] = struct{}{}
	}

	// Finally, we'll tack on our master account output, and pay any
	// remaining surplus fees needed (left over unpaid by trader, and also
	// accounting for our auctioneer output).
	finalAccountBalance := int64(mAccountDiff.AccountBalance)
	finalAccountBalance += int64(
		orderBatch.FeeReport.AuctioneerFeesAccrued,
	)
	finalAccountBalance -= int64(
		txFeeEstimator.AuctioneerFee(
			totalTraderFees, traderOuts,
		),
	)

	log.Infof("Master Auctioneer Output balance delta: prev_bal=%v, "+
		"new_bal=%v, delta=%v", mAccountDiff.AccountBalance,
		btcutil.Amount(finalAccountBalance),
		btcutil.Amount(finalAccountBalance)-mAccountDiff.AccountBalance)

	// Next, we'll derive the account script for the auctioneer itself,
	// which is the final thing we need in order to generate the batch
	// execution transaction.
	auctioneerAccountScript, err := mAccountDiff.AccountScript()
	if err != nil {
		return err
	}
	e.ExeTx.AddTxOut(&wire.TxOut{
		Value:    finalAccountBalance,
		PkScript: auctioneerAccountScript,
	})

	txsort.InPlaceSort(e.ExeTx)

	// As the transaction has just been sorted, we can now index the final
	// version of the transaction, so we can easily perform the signing
	// execution in the next phase.
	masterAcctInputIndex, err := e.indexBatchTx(
		scriptToOrders, traderAccounts, ordersForTrader, inputToAcct,
	)
	if err != nil {
		return err
	}

	err = blockchain.CheckTransactionSanity(btcutil.NewTx(e.ExeTx))
	if err != nil {
		return err
	}

	// Finally, we'll construct a new account diff to be used for the
	// _next_ execution transaction which describes the ending state of the
	// master account.
	//
	// TODO(roasbeef): do above in indexBatchTx?
	_, masterAccountOutputIndex := input.FindScriptOutputIndex(
		e.ExeTx, auctioneerAccountScript,
	)
	e.MasterAccountDiff = &MasterAccountState{
		PriorPoint: mAccountDiff.PriorPoint,
		OutPoint: &wire.OutPoint{
			Hash:  e.ExeTx.TxHash(),
			Index: masterAccountOutputIndex,
		},
		AccountBalance: btcutil.Amount(finalAccountBalance),
		AuctioneerKey:  mAccountDiff.AuctioneerKey,
		BatchKey:       mAccountDiff.BatchKey,
		InputIndex:     masterAcctInputIndex,
	}

	return nil
}

// New creates a new ExecutionContext which contains all the information needed
// to execute the passed OrderBatch.
func New(batch *matching.OrderBatch, mad *MasterAccountState,
	feeRate chainfee.SatPerKWeight) (*ExecutionContext, error) {

	exeCtx := ExecutionContext{
		orderIndex:     make(map[orderT.Nonce][]*OrderOutput),
		traderIndex:    make(map[matching.AccountID][]*OrderOutput),
		accountIndex:   make(map[matching.AccountID]wire.OutPoint),
		acctInputIndex: make(map[matching.AccountID]*AcctInput),
		OrderBatch:     batch,
	}

	err := exeCtx.assembleBatchTx(batch, mad, feeRate)
	if err != nil {
		return nil, err
	}

	return &exeCtx, nil
}

// OutputForOrder returns the corresponding output within the execution
// transaction for the passed order nonce.
func (e *ExecutionContext) OutputsForOrder(nonce orderT.Nonce) ([]*OrderOutput, bool) {
	output, ok := e.orderIndex[nonce]
	return output, ok
}

// ChanOutputsForTrader returns the set of outputs that create channels for the
// set of matched orders.
func (e *ExecutionContext) ChanOutputsForTrader(acct matching.AccountID) ([]*OrderOutput, bool) {
	outputs, ok := e.traderIndex[acct]
	return outputs, ok
}

// AcctOutputForTrader returns the output that re-creates an account for the
// target trader.
//
// NOTE: If the trader's account was fully consumed, then there won't be an
// entry for them.
func (e *ExecutionContext) AcctOutputForTrader(acct matching.AccountID) (wire.OutPoint, bool) {
	op, ok := e.accountIndex[acct]
	return op, ok
}

// AcctInputForTrader returns the account input information for the target
// trader, if it exists.
func (e *ExecutionContext) AcctInputForTrader(acct matching.AccountID) (*AcctInput, bool) {
	input, ok := e.acctInputIndex[acct]
	return input, ok
}
