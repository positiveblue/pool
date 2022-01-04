package batchtx

import (
	"fmt"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/btcsuite/btcutil/txsort"
	"github.com/lightninglabs/pool/order"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/feebump"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// ErrPoorTrader is returned if an account cannot pay their chain fees!
type ErrPoorTrader struct {
	Account matching.AccountID
	Err     error
}

// Error returns a human-readable error message.
func (e *ErrPoorTrader) Error() string {
	return e.Err.Error()
}

// ErrMasterBalanceDust will be returned if a batch transaction is attempted
// assembled where the final master account balance would become dust.
var ErrMasterBalanceDust = fmt.Errorf("final master account balance below dust")

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

// BatchInput stores information about a input to the batch transaction, either
// spending a given trader's account, or spending some other output into the
// batch.
type BatchInput struct {
	// InputIndex the input index in the final batch execution transaction.
	InputIndex uint32

	// InputPoint is the full outpoint referenced within the batch
	// execution.
	InputPoint wire.OutPoint

	// PrevOutput is the previous output that we're spending. This includes
	// the output value, and also the prior script.
	PrevOutput wire.TxOut
}

// RequestedInput holds information about an extra input that has been
// requested added to the batch transaction.
type RequestedInput struct {
	// PrevOutPoint is the outpoint the input should spend.
	PrevOutPoint wire.OutPoint

	// Value is the value of the spent outpoint.
	Value btcutil.Amount

	// PkScript is the script of the output spent.
	PkScript []byte

	// AddWeightEstimate adds a size upper bound to the given weight
	// estimator for this input type.
	AddWeightEstimate func(*input.TxWeightEstimator) error
}

// RequestedOutput holds information about an extra output that has been
// requested added to the batch transaction.
type RequestedOutput struct {
	// Value is the value of the output.
	Value btcutil.Amount

	// PkScript is the script to send to.
	PkScript []byte

	// AddWeightEstimate adds the size of the output to the given weight
	// estimator.
	AddWeightEstimate func(*input.TxWeightEstimator) error
}

// BatchIO is a struct holding inputs and outputs that should be attempted
// added to the batch transaction, in addition to the regular account inputs,
// and account and channel outputs.
type BatchIO struct {
	// Inputs is a list of requested inputs.
	Inputs []*RequestedInput

	// Outputs is a list of requested outputs.
	Outputs []*RequestedOutput
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

// AccountScript derives the auctioneer's account script.
//
// TODO(roasbeef): post tapscript, all can appear uniform w/ their spends ;)
func (m *MasterAccountState) AccountScript() ([]byte, error) {
	batchKey, err := btcec.ParsePubKey(
		m.BatchKey[:], btcec.S256(),
	)
	if err != nil {
		return nil, err
	}

	return m.script(batchKey)
}

// PrevAccountScript derives the auctioneer's account script for the previous
// batch.
func (m *MasterAccountState) PrevAccountScript() ([]byte, error) {
	batchKey, err := btcec.ParsePubKey(
		m.BatchKey[:], btcec.S256(),
	)
	if err != nil {
		return nil, err
	}

	prevBatchKey := poolscript.DecrementKey(batchKey)
	return m.script(prevBatchKey)
}

// script derives the auctioneer's account script for the given batch key.
func (m *MasterAccountState) script(batchKey *btcec.PublicKey) ([]byte, error) {
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
	// BatchID is the current batch ID.
	BatchID [33]byte

	// FeeScheduler is a function that returns the fee schedule for a
	// specific account ID.
	FeeScheduler matching.FeeScheduler

	// BatchFeeRate is the target fee rate used when assembling the batch
	// execution transaction.
	BatchFeeRate chainfee.SatPerKWeight

	// BatchHeightHint is the earliest absolute height in the chain in which
	// the batch transaction can be found within. This will be used by
	// traders to base off their absolute channel lease maturity height.
	BatchHeightHint uint32

	// MasterAcct is the auctioneers account at the point at which this
	// batch is executed.
	MasterAcct *account.Auctioneer

	// OrderBatch is a pointer to the batch that this context attempts to
	// execute.
	OrderBatch *matching.OrderBatch

	// ExeTx is the unsigned execution transaction.
	ExeTx *wire.MsgTx

	// FeeInfoEstimate holds a fee info estimate for the unsigned execution
	// transaction.
	FeeInfoEstimate *feebump.TxFeeInfo

	// MasterAccountDiff is a diff that describes the prior and current
	// state of the auctioneer's master account output.
	MasterAccountDiff *MasterAccountState

	// orderIndex maps an order nonce to the output within the batch
	// execution transaction that executes the order.
	orderIndex map[orderT.Nonce][]*OrderOutput

	// traderIndex maps a trader's account ID to set of outputs that create
	// channels that involve the trader.
	traderIndex map[matching.AccountID][]*OrderOutput

	// accountOutputIndex maps a trader's account to the output that
	// re-creates its account in the batch.
	//
	// NOTE: If a trader's account is fully consumed in this batch, then
	// they won't have an entry in this map.
	accountOutputIndex map[matching.AccountID]wire.OutPoint

	// acctInputIndex maps a trader's account ID to its previous account
	// outpoint, that is being spent in the batch.
	acctInputIndex map[matching.AccountID]wire.OutPoint

	// batchInputIndex maps the outpoints spent by the batch transaction to
	// information about the input.
	batchInputIndex map[wire.OutPoint]*BatchInput

	// masterIO are extra inputs and outputs requested added to the batch
	// by the master account. This means that the delta will be taken from
	// or added to the master account.
	masterIO *BatchIO
}

// indexBatchTx is a helper method that indexes a batch transaction given a
// number of auxiliary indexes built up during batch transaction construction.
// This method also returns the input index of the auctioneer's account.
func (e *ExecutionContext) indexBatchTx(
	scriptToOrders map[string][2]order.Order,
	traderAccounts map[matching.AccountID]*wire.TxOut,
	ordersForTrader map[matching.AccountID]map[orderT.Nonce]struct{}) error {

	txHash := e.ExeTx.TxHash()

	// First, we'll map each order to the proper account output using an
	// auxiliary index that maps the script to the nonce.
	for stringScript, orders := range scriptToOrders {
		fundingScript := []byte(stringScript)

		found, outputIndex := input.FindScriptOutputIndex(
			e.ExeTx, fundingScript,
		)
		if !found {
			return fmt.Errorf("unable to find funding script "+
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
				return fmt.Errorf("unable to find order "+
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
			return fmt.Errorf("unable to find script for %x",
				acctID[:])
		}

		e.accountOutputIndex[acctID] = wire.OutPoint{
			Hash:  txHash,
			Index: outputIndex,
		}
	}

	// Finally, we'll populate the final component (the input index) of the
	// batchInputIndex map.
	for i, txIn := range e.ExeTx.TxIn {
		prevOut := txIn.PreviousOutPoint
		op, ok := e.batchInputIndex[prevOut]
		if !ok {
			return fmt.Errorf("prev outpoint %v not found "+
				"in index", prevOut)
		}

		op.InputIndex = uint32(i)
	}

	return nil
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

	// We'll create a deep copy of the order batch that we will work with
	// during batch tx assembly. The reason is that we'll modify the fee
	// report with chain fees paid, which means we'll mutate the balances
	// in the batch. If this method fails for some reason, that would lead
	// to the order batch passed in becoming invalid for the next call.
	c := orderBatch.Copy()
	orderBatch = &c

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
		accountScript, err := poolscript.AccountScript(
			acctPreBatch.AccountExpiry, acctKey, auctioneerKey,
			batchKey, acctPreBatch.VenueSecret,
		)
		if err != nil {
			return err
		}

		e.acctInputIndex[acctID] = prevOutPoint
		e.batchInputIndex[prevOutPoint] = &BatchInput{
			InputPoint: prevOutPoint,
			PrevOutput: wire.TxOut{
				Value:    int64(acctPreBatch.AccountBalance),
				PkScript: accountScript,
			},
		}

		inputToAcct[prevOutPoint] = acctID
	}

	prevScript, err := mAccountDiff.PrevAccountScript()
	if err != nil {
		return err
	}

	e.ExeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: mAccountDiff.PriorPoint,
	})
	e.batchInputIndex[mAccountDiff.PriorPoint] = &BatchInput{
		InputPoint: mAccountDiff.PriorPoint,
		PrevOutput: wire.TxOut{
			Value:    int64(mAccountDiff.AccountBalance),
			PkScript: prevScript,
		},
	}

	// As we go estimate and count the chain fees paid by the traders.
	txFeeEstimator := newChainFeeEstimator(
		orderBatch.Orders, feeRate, e.masterIO,
	)
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
			err := fmt.Errorf("account %x only had balance %v to "+
				"cover chain fee %v", acctID,
				trader.EndingBalance, traderFee)
			return &ErrPoorTrader{
				Account: acctID,
				Err:     err,
			}
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

			// If the client supported account expiry height extension after
			// participating in a batch, set the expiry to the new height.
			if trader.NewExpiry != 0 {
				acctParams.AccountExpiry = trader.NewExpiry
			}
			accountScript, err := poolscript.AccountScript(
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

			// Don't auto-renew accounts that are fully spent.
			// Otherwise, the client is going to calculate the wrong
			// pk script when looking for the spend on chain.
			trader.NewExpiry = 0
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

		fundingAmount := orderDetails.Quote.TotalSatsCleared +
			bid.SelfChanBalance
		_, fundingOutput, err := input.GenFundingPkScript(
			bid.MultiSigKey[:], ask.MultiSigKey[:],
			int64(fundingAmount),
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

	auctioneerFee, err := txFeeEstimator.AuctioneerFee(
		totalTraderFees, traderOuts,
	)
	if err != nil {
		return err
	}
	finalAccountBalance -= int64(auctioneerFee)

	// We'll go through and add extra master account Inputs/Outputs to the
	// batch tx. The balance delta will be added to the master account
	// balance.
	var balanceDelta btcutil.Amount
	for _, in := range e.masterIO.Inputs {
		balanceDelta += in.Value
		e.ExeTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: in.PrevOutPoint,
		})
		e.batchInputIndex[in.PrevOutPoint] = &BatchInput{
			InputPoint: in.PrevOutPoint,
			PrevOutput: wire.TxOut{
				Value:    int64(in.Value),
				PkScript: in.PkScript,
			},
		}
	}

	for _, out := range e.masterIO.Outputs {
		balanceDelta -= out.Value
		op := &wire.TxOut{
			Value:    int64(out.Value),
			PkScript: out.PkScript,
		}
		e.ExeTx.AddTxOut(op)
	}

	finalAccountBalance += int64(balanceDelta)

	log.Infof("Master Auctioneer Output balance delta: prev_bal=%v, "+
		"new_bal=%v, delta=%v (fees_accrued=%v, auctioneer_chain_fee"+
		"=%v, extra_io=%v)", mAccountDiff.AccountBalance,
		btcutil.Amount(finalAccountBalance),
		btcutil.Amount(finalAccountBalance)-mAccountDiff.AccountBalance,
		orderBatch.FeeReport.AuctioneerFeesAccrued, auctioneerFee,
		balanceDelta)

	// If the final master account balance goes below dust, the next batch
	// cannot be executed, so we have no choice other than return a
	// terminal error.
	if finalAccountBalance < int64(orderT.MinNoDustAccountSize) {
		return ErrMasterBalanceDust
	}

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
	err = e.indexBatchTx(scriptToOrders, traderAccounts, ordersForTrader)
	if err != nil {
		return err
	}

	// Get the master account index from the input index, since we'll need
	// that for later.
	masterAcctInputIndex := int(
		e.batchInputIndex[mAccountDiff.PriorPoint].InputIndex,
	)

	err = blockchain.CheckTransactionSanity(btcutil.NewTx(e.ExeTx))
	if err != nil {
		return err
	}

	// We'll take note of the fee paid by the transaction and the final
	// weight estimate. We will use this to determine if we need to
	// recreate the batch transaction with a higher fee before executing
	// it.
	txFee := totalTraderFees + auctioneerFee
	txWeight, err := txFeeEstimator.EstimateBatchWeight(traderOuts)
	if err != nil {
		return err
	}

	e.FeeInfoEstimate = &feebump.TxFeeInfo{
		Fee:    txFee,
		Weight: txWeight,
	}

	// Now that batch tx assembly has finished, update the order batch in
	// the execution context to point to our working copy with chain fees
	// accounted for.
	e.OrderBatch = orderBatch

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

// NewExecutionContext creates a new ExecutionContext which contains all the
// information needed to execute the passed OrderBatch.
func NewExecutionContext(batchKey *btcec.PublicKey, batch *matching.OrderBatch,
	masterAcct *account.Auctioneer, masterIO *BatchIO,
	batchFeeRate chainfee.SatPerKWeight, batchHeightHint uint32,
	feeScheduler matching.FeeScheduler) (*ExecutionContext, error) {

	// When we create this master account state, we'll ensure that
	// we provide the "next" batch key, as this is what will be
	// used to create the outputs in the BET.
	masterAcctState := &MasterAccountState{
		PriorPoint:     masterAcct.OutPoint,
		AccountBalance: masterAcct.Balance,
	}
	copy(
		masterAcctState.AuctioneerKey[:],
		masterAcct.AuctioneerKey.PubKey.SerializeCompressed(),
	)
	nextBatchKey := poolscript.IncrementKey(batchKey)
	copy(
		masterAcctState.BatchKey[:],
		nextBatchKey.SerializeCompressed(),
	)

	var batchID [33]byte
	copy(batchID[:], batchKey.SerializeCompressed())

	exeCtx := ExecutionContext{
		BatchID:            batchID,
		FeeScheduler:       feeScheduler,
		BatchFeeRate:       batchFeeRate,
		BatchHeightHint:    batchHeightHint,
		MasterAcct:         masterAcct,
		OrderBatch:         batch,
		orderIndex:         make(map[orderT.Nonce][]*OrderOutput),
		traderIndex:        make(map[matching.AccountID][]*OrderOutput),
		accountOutputIndex: make(map[matching.AccountID]wire.OutPoint),
		acctInputIndex:     make(map[matching.AccountID]wire.OutPoint),
		batchInputIndex:    make(map[wire.OutPoint]*BatchInput),
		masterIO:           masterIO,
	}

	err := exeCtx.assembleBatchTx(batch, masterAcctState, batchFeeRate)
	if err != nil {
		return nil, err
	}

	return &exeCtx, nil
}

// OutputsForOrder returns the corresponding output within the execution
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

// AllChanOutputs returns all the channel outpoints that are being created by
// the batch in this execution context.
func (e *ExecutionContext) AllChanOutputs() []wire.OutPoint {
	// We de-duplicate the outpoints first by putting them into a map
	// because otherwise we'd get double the outpoints since two traders are
	// always involved in a channel output.
	ops := make(map[wire.OutPoint]struct{})
	for _, outputs := range e.traderIndex {
		for _, output := range outputs {
			ops[output.OutPoint] = struct{}{}
		}
	}

	result := make([]wire.OutPoint, 0, len(ops))
	for op := range ops {
		result = append(result, op)
	}

	return result
}

// AcctOutputForTrader returns the output that re-creates an account for the
// target trader.
//
// NOTE: If the trader's account was fully consumed, then there won't be an
// entry for them.
func (e *ExecutionContext) AcctOutputForTrader(acct matching.AccountID) (wire.OutPoint, bool) {
	op, ok := e.accountOutputIndex[acct]
	return op, ok
}

// AcctInputForTrader returns the account input information for the target
// trader, if it exists.
func (e *ExecutionContext) AcctInputForTrader(acct matching.AccountID) (*BatchInput, bool) {
	op, ok := e.acctInputIndex[acct]
	if !ok {
		return nil, false
	}

	input, ok := e.batchInputIndex[op]
	return input, ok
}

func (e *ExecutionContext) BatchInput(op wire.OutPoint) (*BatchInput, bool) {
	input, ok := e.batchInputIndex[op]
	return input, ok
}

// ExtraInputs returns a list of extra batch inputs added by the auctioneer.
func (e *ExecutionContext) ExtraInputs() []*BatchInput {
	extra := make([]*BatchInput, len(e.masterIO.Inputs))
	for i, in := range e.masterIO.Inputs {
		prev := in.PrevOutPoint
		extra[i] = e.batchInputIndex[prev]
	}
	return extra
}
