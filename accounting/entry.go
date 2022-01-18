package accounting

import (
	"time"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/faraday/fiat"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/shopspring/decimal"
)

type BatchEntry struct {
	// BatchID is the id of the batch this entry refers to.
	BatchID orderT.BatchID

	// Timestamp is the time at which the event occurred.
	Timestamp time.Time

	// TxID is the transaction ID of this entry.
	TxID string

	// BatchTxFee is the chain fee paid by the above batch tx.
	BatchTxFees btcutil.Amount

	// AccruedFees is the total number of satoshis we earned as auctioneer
	// fees. This should be the sum of the TotalExecutionFeesPaid for all
	// accounts in the AccountDiffs of the batch.
	AccruedFees btcutil.Amount

	// TraderChainFees is the total on chain fees paid by all the market
	// participants in this batch.
	TraderChainFees btcutil.Amount

	// ProfitInSats is the net value earned (or lost if negative) during the
	// execution of this batch.  It should match Accrued + Trader - Batch fees.
	ProfitInSats btcutil.Amount

	// ProfitInUSD is the net value earned (or lost if negative) during the
	// execution of this batch in USD.
	ProfitInUSD decimal.Decimal

	// BTCPrice is the timestamped bitcoin price we used to get our fiat
	// value.
	BTCPrice *fiat.Price
}

// extractBatchEntry returns a new reporting entry line for the given batch.
func extractBatchEntry(cfg *Config, batchID orderT.BatchID,
	batch *subastadb.BatchSnapshot) (*BatchEntry, error) {

	timestamp := batch.OrderBatch.CreationTimestamp
	txID := batch.BatchTx.TxHash().String()
	batchTxFees := batch.BatchTxFee
	accruedFees := batch.OrderBatch.FeeReport.AuctioneerFeesAccrued
	traderChainFees := getTraderChainFees(batch.OrderBatch)
	auctioneerChainFees := batchTxFees - traderChainFees
	profitInSats := accruedFees - auctioneerChainFees
	profitnInMsat := lnwire.NewMSatFromSatoshis(profitInSats)
	btcPrice, err := cfg.GetPrice(timestamp)
	if err != nil {
		return nil, err
	}
	profitInUSD := fiat.MsatToFiat(btcPrice.Price, profitnInMsat)

	return &BatchEntry{
		BatchID:         batchID,
		Timestamp:       timestamp,
		TxID:            txID,
		BatchTxFees:     batchTxFees,
		AccruedFees:     accruedFees,
		TraderChainFees: traderChainFees,
		ProfitInSats:    profitInSats,
		ProfitInUSD:     profitInUSD,
		BTCPrice:        btcPrice,
	}, nil
}

// getTraderChainFees returns all the chain fees paid by the traders in a batch.
func getTraderChainFees(orderBatch *matching.OrderBatch) btcutil.Amount {
	diffs := orderBatch.FeeReport.AccountDiffs
	for _, o := range orderBatch.Orders {
		diffs[o.Asker.AccountKey].StartingState.AccountBalance -=
			o.Details.Quote.TotalSatsCleared
		diffs[o.Asker.AccountKey].StartingState.AccountBalance -=
			o.Details.Bid.SelfChanBalance
	}

	feesPaid := btcutil.Amount(0)
	for _, diff := range diffs {
		diff.StartingState.AccountBalance -= diff.EndingBalance
		diff.StartingState.AccountBalance += diff.TotalMakerFeesAccrued
		diff.StartingState.AccountBalance -= diff.TotalExecutionFeesPaid
		diff.StartingState.AccountBalance -= diff.TotalTakerFeesPaid
		feesPaid += diff.StartingState.AccountBalance
	}

	return feesPaid
}

type LSATEntry struct {
	// Timestamp is the time at which the event occurred.
	Timestamp time.Time

	// ProfitInSats is the net value charged for this LSAT token.
	ProfitInSats btcutil.Amount

	// ProfitInUSD is the net value charged in USD.
	ProfitInUSD decimal.Decimal

	// BTCPrice is the timestamped bitcoin price we used to get our fiat
	// value.
	BTCPrice *fiat.Price
}
