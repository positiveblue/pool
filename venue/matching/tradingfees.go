package matching

import (
	"github.com/btcsuite/btcutil"

	orderT "github.com/lightninglabs/agora/client/order"
)

// FixedRatePremium is the unit that we'll use to express the "lease" rate of
// the funds within a channel. This value is compounded every N blocks. As a
// result, a trader will pay for the longer they wish to allocate liquidity to
// another agent in the market. In other words, this is our period interest
// rate.
type FixedRatePremium uint32

// LumpSumPremium calculates the total amount that will be paid out to lease an
// asset at the target FixedRatePremium, for the specified amount. This
// computes the total amount that will be paid over the lifetime of the asset.
// We'll use this to compute the total amount that a taker needs to set aside
// once they're matched.
func (f FixedRatePremium) LumpSumPremium(amt btcutil.Amount,
	durationBlocks uint32) btcutil.Amount {

	// First, we'll compute the premium that will be paid each block over
	// the lifetime of the asset.
	//
	// TODO(roasbeef): last param isn't used, ended up calculating the
	// fixed rate fee here
	premiumPerBlock := orderT.CalcFee(amt, uint32(f), 0)

	// Once we have this value, we can then multiply the premium paid per
	// block times the number of compounding periods, or the total lease
	// duration.
	return premiumPerBlock * btcutil.Amount(durationBlocks)
}

// TODO(roasbeef):
//  * computes all execution fees (or do in another package?)
//  * compute fees the taker need to pay to the maker
//     * rn just premium based on channel size
//  * compute fees both sides pay to us
//     * based + %

// FeeSchedule is an interface that represents the configuration source that
// the auctioneer will use to determine how much to charge in fees for each
// trader.
type FeeSchedule interface {
	// BaseFee is the base fee the auctioneer will charge the traders for
	// each executed order.
	BaseFee() btcutil.Amount

	// ExecutionFee computes the execution fee (usually based off of a
	// rate) for the target amount.
	ExecutionFee(amt btcutil.Amount) btcutil.Amount
}

// AccountDiff represents a matching+clearing event for a trader's account.
// This diff shows the total balance delta along with a breakdown for each item
// for a trader's account.
type AccountDiff struct {
	// StartingState is the starting state for a trader's account.
	StartingState *Trader

	// EndingBalance is the ending balance for a trader's account.
	EndingBalance btcutil.Amount

	// TotalExecutionFeesPaid is the total amount of fees a trader paid to
	// the venue.
	TotalExecutionFeesPaid btcutil.Amount

	// TotalTakerFeesPaid is the total amount of fees the trader paid to
	// purchase any channels in this batch.
	TotalTakerFeesPaid btcutil.Amount

	// TotalMakerFeesAccrued is the total amount of fees the trader gained
	// by selling channels in this batch.
	TotalMakerFeesAccrued btcutil.Amount
}

// TradingFeeReport is the breakdown of the balance fluctuations to a trade's
// account during this batch.
type TradingFeeReport struct {
	// AccountDiffs maps a trader's account ID to an account diff.
	AccountDiffs map[AccountID]AccountDiff

	// AuctioneerFees is the total amount of satoshis the auctioneer gained
	// in this batch. This should be the sum of the TotalExecutionFeesPaid
	// for all accounts in the AccountDiffs map above.
	AuctioneerFeesAccrued btcutil.Amount
}

// NewTradingFeeReport creates a new trading fee report given a set of matched
// orders, the clearing price for the batch, and the feeSchedule of the
// auctioneer.
func NewTradingFeeReport(orders []MatchedOrder, feeSchedule FeeSchedule,
	clearingPrice FixedRatePremium) TradingFeeReport {

	accountDiffs := make(map[AccountID]AccountDiff)
	var totalFeesAccrued btcutil.Amount

	// For each order pair, we'll compute the exchange of funds due to
	// channel selling, buying, and trading fee execution.
	for _, order := range orders {
		maker := order.Asker
		taker := order.Bidder

		// If neither the taker or maker have an entry yet in the
		// account diff map, we'll initialize them with their starting
		// balance before clearing of this batch.
		if _, ok := accountDiffs[taker.AccountKey]; !ok {
			accountDiffs[taker.AccountKey] = AccountDiff{
				StartingState: &taker,
				EndingBalance: taker.AccountBalance,
			}
		}
		if _, ok := accountDiffs[maker.AccountKey]; !ok {
			accountDiffs[maker.AccountKey] = AccountDiff{
				StartingState: &maker,
				EndingBalance: maker.AccountBalance,
			}
		}

		takerDiff := accountDiffs[taker.AccountKey]
		makerDiff := accountDiffs[maker.AccountKey]

		// Now that we know we have state initialized for both sides,
		// we'll evaluate the order they belong to clear them against
		// their balances.
		totalSats := order.Details.Quote.TotalSatsCleared

		// First, we'll need to subtract the total amount of sats
		// cleared (the channel size), from the account of the maker.
		makerDiff.EndingBalance -= totalSats

		// Next, we'll need to debit the taker's account to pay the
		// premium derived from the uniform clearing price for this
		//
		// TODO(roasbeef): which duration to use? clear the duration
		// market as well?
		//
		// TODO(roasbeef): need market wide clamp on duration?
		satsPremium := clearingPrice.LumpSumPremium(
			totalSats, order.Details.Bid.MinDuration(),
		)
		takerDiff.EndingBalance -= satsPremium
		takerDiff.TotalTakerFeesPaid += satsPremium
		makerDiff.EndingBalance += satsPremium
		makerDiff.TotalMakerFeesAccrued += satsPremium

		// Finally, we'll subtract our execution fees from both sides
		// as well. The execution fee is the base fee plus the
		// execution fee which scales based on the order size.
		//
		// TODO(roasbeef): need to thread thru proper execution fee
		//  * only collect from taker? won't work for makers if
		//    execution fee is greater than their fee
		executionFee := feeSchedule.BaseFee() + feeSchedule.ExecutionFee(
			totalSats,
		)
		takerDiff.EndingBalance -= executionFee
		makerDiff.EndingBalance -= executionFee

		takerDiff.TotalExecutionFeesPaid += executionFee
		makerDiff.TotalExecutionFeesPaid += executionFee

		// We'll accrue the execution fees from BOTH traders, hence the
		// times two.
		totalFeesAccrued += executionFee * 2

		accountDiffs[taker.AccountKey] = takerDiff
		accountDiffs[maker.AccountKey] = makerDiff
	}

	return TradingFeeReport{
		AccountDiffs:          accountDiffs,
		AuctioneerFeesAccrued: totalFeesAccrued,
	}
}
