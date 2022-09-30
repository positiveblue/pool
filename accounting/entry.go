package accounting

import (
	"context"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightninglabs/faraday/fiat"
	"github.com/lightninglabs/lndclient"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/channeldb"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/shopspring/decimal"
)

const (
	// MaxInvoices is the maximum number of invoices to be returned
	// by the LightningClient.
	maxInvocies = 999999
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
	batch *matching.BatchSnapshot) (*BatchEntry, error) {

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

// isLSATInvoice is a helper function used to determine if an invoice is linked
// to a settled LSAT toker or not.
func isLSATInvoice(invoice lndclient.Invoice) bool {
	// We only care about settled invoices.
	if invoice.State != channeldb.ContractSettled {
		return false
	}
	// LSAT invoices have this memo by default.
	if invoice.Memo != "LSAT" {
		return false
	}

	return true
}

// getLSATInvoices returns all the settled invoices linked to LSAT payments.
func getLSATInvoices(ctx context.Context, cfg *Config) (
	[]lndclient.Invoice, error) {

	invoices := []lndclient.Invoice{}

	invoiceReq := lndclient.ListInvoicesRequest{
		MaxInvoices: maxInvocies,
	}
	resp, err := cfg.LightningClient.ListInvoices(ctx, invoiceReq)
	if err != nil {
		return invoices, err
	}

	for _, invoice := range resp.Invoices {
		timestamp := invoice.CreationDate

		// Filter out invoices that are not in the wanted time span.
		if timestamp.Before(cfg.Start) || timestamp.After(cfg.End) {
			continue
		}

		// Filter out non-LSAT invocies.
		if !isLSATInvoice(invoice) {
			continue
		}

		invoices = append(invoices, invoice)
	}

	return invoices, nil
}

// extractLSATEntry returns a new reporting entry line for the given LSAT
// invoice.
func extractLSATEntry(cfg *Config, invoice lndclient.Invoice) (
	*LSATEntry, error) {

	timestamp := invoice.CreationDate
	profitInMsat := invoice.AmountPaid
	profitInSats := profitInMsat.ToSatoshis()
	btcPrice, err := cfg.GetPrice(timestamp)
	if err != nil {
		return nil, err
	}
	profitInUSD := fiat.MsatToFiat(btcPrice.Price, profitInMsat)

	return &LSATEntry{
		Timestamp:    timestamp,
		ProfitInSats: profitInSats,
		ProfitInUSD:  profitInUSD,
		BTCPrice:     btcPrice,
	}, nil
}
