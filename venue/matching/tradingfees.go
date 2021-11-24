package matching

import (
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/terms"
)

// AccountDiff represents a matching+clearing event for a trader's account.
// This diff shows the total balance delta along with a breakdown for each item
// for a trader's account.
type AccountDiff struct {
	*orderT.AccountTally

	// StartingState is the starting state for a trader's account.
	StartingState *Trader

	// RecreatedOutput is the recreated account output in the batch
	// transaction. This is only set if the account had sufficient balance
	// left for a new on-chain output and wasn't considered to be dust.
	RecreatedOutput *wire.TxOut
}

// TradingFeeReport is the breakdown of the balance fluctuations to a trade's
// account during this batch.
type TradingFeeReport struct {
	// AccountDiffs maps a trader's account ID to an account diff.
	AccountDiffs map[AccountID]*AccountDiff

	// AuctioneerFees is the total amount of satoshis the auctioneer gained
	// in this batch. This should be the sum of the TotalExecutionFeesPaid
	// for all accounts in the AccountDiffs map above.
	AuctioneerFeesAccrued btcutil.Amount
}

// NewTradingFeeReport creates a new trading fee report given a set of matched
// orders, the clearing price for the batch, and the feeSchedule of the
// auctioneer.
func NewTradingFeeReport(subBatches map[uint32][]MatchedOrder,
	feeSchedule terms.FeeSchedule,
	clearingPrices map[uint32]orderT.FixedRatePremium) TradingFeeReport {

	accountDiffs := make(map[AccountID]*AccountDiff)
	var totalFeesAccrued btcutil.Amount

	// For each order pair, we'll compute the exchange of funds due to
	// channel selling, buying, and trading fee execution.
	for _, orders := range subBatches {
		for _, order := range orders {
			maker := order.Asker
			taker := order.Bidder

			// If neither the taker or maker have an entry yet in
			// the account diff map, we'll initialize them with
			// their starting balance before clearing of this batch.
			if _, ok := accountDiffs[taker.AccountKey]; !ok {
				accountDiffs[taker.AccountKey] = &AccountDiff{
					StartingState: &taker,
					AccountTally: &orderT.AccountTally{
						EndingBalance: taker.AccountBalance,
					},
				}
			}
			if _, ok := accountDiffs[maker.AccountKey]; !ok {
				accountDiffs[maker.AccountKey] = &AccountDiff{
					StartingState: &maker,
					AccountTally: &orderT.AccountTally{
						EndingBalance: maker.AccountBalance,
					},
				}
			}

			takerDiff := accountDiffs[taker.AccountKey]
			makerDiff := accountDiffs[maker.AccountKey]

			// Now that we know we have state initialized for both
			// sides, we'll evaluate the order they belong to clear
			// them against their balances.
			totalSats := order.Details.Quote.TotalSatsCleared

			// The durations are symmetric now, it doesn't matter
			// which one we take. They must be in the same bucket
			// anyway.
			duration := order.Details.Ask.LeaseDuration()
			clearingPrice := clearingPrices[duration]

			// Next, we'll need to debit the taker's account to pay
			// the premium derived from the uniform clearing price
			// for this.
			totalFeesAccrued += makerDiff.CalcMakerDelta(
				feeSchedule, clearingPrice, totalSats,
				order.Details.Bid.LeaseDuration(),
			)
			totalFeesAccrued += takerDiff.CalcTakerDelta(
				feeSchedule, clearingPrice, totalSats,
				order.Details.Bid.SelfChanBalance,
				order.Details.Bid.LeaseDuration(),
			)

			accountDiffs[taker.AccountKey] = takerDiff
			accountDiffs[maker.AccountKey] = makerDiff
		}
	}

	return TradingFeeReport{
		AccountDiffs:          accountDiffs,
		AuctioneerFeesAccrued: totalFeesAccrued,
	}
}
