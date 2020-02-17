package batchtx

import (
	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/client/clmscript"
	"github.com/lightninglabs/agora/venue/matching"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// TODO(roasbeef): needs to live in the client package? for proper verification

// chainFeeEstimator is a helper struct that we'll use to construct the batch
// execution transaction. It helps us easily compute things like the fees that
// each trader should pay.
type chainFeeEstimator struct {
	// feeRate is target fee rate of the batch execution transaction.
	feeRate chainfee.SatPerKWeight

	// traderChanCount is a running tally of the number of channels each
	// trader will have created in this batch.
	traderChanCount map[matching.AccountID]uint32

	// orders is the test of orders within the given batch.
	orders []matching.MatchedOrder
}

// newChainFeeEstimator creates a new instance a chainFeeEstimator for an order
// batch.
func newChainFeeEstimator(orders []matching.MatchedOrder,
	feeRate chainfee.SatPerKWeight) *chainFeeEstimator {

	traderChanCount := make(map[matching.AccountID]uint32)
	for _, order := range orders {
		traderChanCount[order.Asker.AccountKey]++
		traderChanCount[order.Bidder.AccountKey]++
	}

	return &chainFeeEstimator{
		feeRate:         feeRate,
		traderChanCount: traderChanCount,
		orders:          orders,
	}
}

const (
	// AuctAcctSpendWitnessSize is the size of the witness when the
	// auctioneer spends their account output.
	AuctAcctSpendWitnessSize = 218
)

// EstimateBatchWeight attempts to estimate the total weight of the fully
// signed batch execution transaction.
func (c *chainFeeEstimator) EstimateBatchWeight() int64 {
	var weightEstimator input.TxWeightEstimator

	// For each trader in this set, we'll add an input for their account
	// spend, as well as an output for the new account to be created for
	// them.
	for range c.traderChanCount {
		weightEstimator.AddWitnessInput(
			clmscript.MultiSigWitnessSize,
		)
		weightEstimator.AddP2WSHOutput()

		// TODO(roasbeef): need to handle the case of full account consumption
	}

	// Each new matched order pair will constitute a new channel, so we'll
	// add a P2WSH output for each one.
	for range c.orders {
		weightEstimator.AddP2WSHOutput()
	}

	// Now that we've processed all the orders for each trader, we'll
	// account for the master output for the auctioneer, as well as the
	// size of the witness when we go to spend our master output.
	weightEstimator.AddP2WKHOutput()
	weightEstimator.AddWitnessInput(
		AuctAcctSpendWitnessSize,
	)

	return int64(weightEstimator.Weight())
}

// EstimateTraderFee returns the estimate for the fee that a trader will need
// to pay in the BET. The more outputs a trader creates (channels), the higher
// fee it will pay.
func (c *chainFeeEstimator) EstimateTraderFee(acctID matching.AccountID) btcutil.Amount {
	numTraderChans, ok := c.traderChanCount[acctID]
	if !ok {
		return 0
	}

	var weightEstimate int64

	// First we'll tack on the size of their account output that will be
	// threaded through in this batch.
	weightEstimate += input.P2WSHOutputSize

	// Next we'll add the size of a typical input to account for the input
	// spending their account outpoint.
	weightEstimate += input.InputSize

	// Next, for each channel that will be created involving this trader,
	// we'll add the size of a regular P2WSH output. We divide value by two
	// as the maker and taker will split this fees.
	chanOutputSize := uint32(input.P2WSHOutputSize)
	weightEstimate += int64(chanOutputSize*numTraderChans+1) / 2

	// At this point we've tallied all the non-witness data, so we multiply
	// by 4 to scale it up
	weightEstimate *= blockchain.WitnessScaleFactor

	// Finally, we tack on the size of the witness spending the account
	// outpoint.
	weightEstimate += clmscript.MultiSigWitnessSize

	return c.feeRate.FeeForWeight(weightEstimate)
}

// AuctioneerFee computes the "fee surplus" or the fees that the auctioneer
// will pay. The goal of this is to make the auctioneer pay only the fees for
// the "signalling" bytes within the raw transaction serialization.
//
// NOTE: This value can be negative if there's no true "surplus".
func (c *chainFeeEstimator) AuctioneerFee() btcutil.Amount {
	// To compute the total surplus fee that we need to pay as the
	// auctioneer, we'll first compute the weight of a completed exeTx.
	totalTxWeight := c.EstimateBatchWeight()

	// Next, we'll tally up the total amount that each trader needs to pay
	// for their added inputs and outputs to the batch transaction.
	var traderChainFeesPaid btcutil.Amount
	for trader := range c.traderChanCount {
		traderChainFeesPaid += c.EstimateTraderFee(trader)
	}

	// Finally, the fee that we (the auctioneer) need to pay is the
	// difference between the fee needed for the entire transaction, and
	// what the trader pays. We compute the surplus like this, as it means
	// that we pay for all signalling data in the serialized transaction,
	// while the traders pay only for the inputs/outputs they add to the
	// transaction.
	return c.feeRate.FeeForWeight(totalTxWeight) - traderChainFeesPaid
}
