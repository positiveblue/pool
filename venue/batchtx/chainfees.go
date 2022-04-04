package batchtx

import (
	"github.com/btcsuite/btcd/btcutil"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/feebump"
	"github.com/lightninglabs/subasta/venue/matching"
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

	// extraIO are extra inputs and outputs added to the batch.
	extraIO *BatchIO
}

// newChainFeeEstimator creates a new instance a chainFeeEstimator for an order
// batch.
func newChainFeeEstimator(orders []matching.MatchedOrder,
	feeRate chainfee.SatPerKWeight, io *BatchIO) *chainFeeEstimator {

	traderChanCount := make(map[matching.AccountID]uint32)
	for _, order := range orders {
		traderChanCount[order.Asker.AccountKey]++
		traderChanCount[order.Bidder.AccountKey]++
	}

	return &chainFeeEstimator{
		feeRate:         feeRate,
		traderChanCount: traderChanCount,
		orders:          orders,
		extraIO:         io,
	}
}

// EstimateBatchWeight attempts to estimate the total weight of the fully
// signed batch execution transaction, given the number of non-dust trader
// outputs.
func (c *chainFeeEstimator) EstimateBatchWeight(
	numTraderOuts int) (int64, error) {

	var weightEstimator input.TxWeightEstimator

	// For each trader in this set, we'll add an input for their account
	// spend.
	for range c.traderChanCount {
		weightEstimator.AddWitnessInput(
			poolscript.MultiSigWitnessSize,
		)
	}

	// Add an output to the estimate for each trader account that was
	// non-dust.
	for i := 0; i < numTraderOuts; i++ {
		weightEstimator.AddP2WSHOutput()
	}

	// Each new matched order pair will constitute a new channel, so we'll
	// add a P2WSH output for each one.
	for range c.orders {
		weightEstimator.AddP2WSHOutput()
	}

	// Now that we've processed all the orders for each trader, we'll
	// account for the master output for the auctioneer, as well as the
	// size of the witness when we go to spend our master output.
	weightEstimator.AddP2WSHOutput()
	weightEstimator.AddWitnessInput(
		account.AuctioneerWitnessSize,
	)

	// If there are extra inputs requested added to the batch, use the
	// witness type to estimate the added weight.
	for _, in := range c.extraIO.Inputs {
		err := in.AddWeightEstimate(&weightEstimator)
		if err != nil {
			return 0, err
		}
	}

	// For extra outputs we support only P2WKH and P2WSH for now.
	for _, out := range c.extraIO.Outputs {
		err := out.AddWeightEstimate(&weightEstimator)
		if err != nil {
			return 0, err
		}
	}

	return int64(weightEstimator.Weight()), nil
}

// EstimateTraderFee returns the estimate for the fee that a trader will need
// to pay in the BET. The more outputs a trader creates (channels), the higher
// fee it will pay.
func (c *chainFeeEstimator) EstimateTraderFee(acctID matching.AccountID) btcutil.Amount {
	numTraderChans, ok := c.traderChanCount[acctID]
	if !ok {
		return 0
	}

	return orderT.EstimateTraderFee(numTraderChans, c.feeRate)
}

// AuctioneerFee computes the "fee surplus" or the fees that the auctioneer
// will pay, given the total chain fees paid by the traders, and trader outputs
// manifested on the batch tx. The goal of this is to make the auctioneer pay
// only the fees for the "signalling" bytes within the raw transaction
// serialization, but sometimes it will need to pay more if a trader's account
// didn't have enough left to cover its full fee.
//
// NOTE: This value can be negative if there's no true "surplus".
func (c *chainFeeEstimator) AuctioneerFee(traderChainFeesPaid btcutil.Amount,
	numTraderOuts int) (btcutil.Amount, error) {

	// To compute the total surplus fee that we need to pay as the
	// auctioneer, we'll first compute the weight of a completed exeTx.
	totalTxWeight, err := c.EstimateBatchWeight(numTraderOuts)
	if err != nil {
		return 0, err
	}

	// Get the total tx fee to pay for this weight at our fee rate.
	txFee := c.feeRate.FeeForWeight(totalTxWeight)

	// Since the BOLT#3 standard is to round the final fee down, we might
	// actually end up being one sat short to meet our fee rate. If that
	// happens we add one more sat to the fee, which will be paid by the
	// auctioneer.
	if feebump.FeeRate(txFee, totalTxWeight) < c.feeRate {
		txFee++
	}

	// Finally, the fee that we (the auctioneer) need to pay is the
	// difference between the fee needed for the entire transaction, and
	// what the trader pays. We compute the surplus like this, as it means
	// that we pay for all signalling data in the serialized transaction,
	// while the traders pay only for the inputs/outputs they add to the
	// transaction.
	return txFee - traderChainFeesPaid, nil
}
