package matching

import (
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/btcsuite/btcutil"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/terms"
)

type mockFeeSchedule struct {
	baseFee    btcutil.Amount
	exeFeeRate orderT.FixedRatePremium
}

func (m *mockFeeSchedule) BaseFee() btcutil.Amount {
	return m.baseFee
}

func (m *mockFeeSchedule) ExecutionFee(amt btcutil.Amount) btcutil.Amount {
	return btcutil.Amount(orderT.PerBlockPremium(amt, uint32(m.exeFeeRate)))
}

var _ terms.FeeSchedule = (*mockFeeSchedule)(nil)

func genRandMatchedOrders(r *rand.Rand, acctDB *acctFetcher,
	opts ...orderGenOption) []MatchedOrder {

	numMatchedOrders := uint16(r.Int31()) % 1000
	matchedOrders := make([]MatchedOrder, numMatchedOrders)
	for i := 0; i < int(numMatchedOrders); i++ {
		ask := genRandAsk(r, acctDB, opts...)
		bid := genRandBid(r, acctDB, opts...)

		bidAcct, _ := acctDB.fetchAcct(bid.Bid.AcctKey)
		askAcct, _ := acctDB.fetchAcct(ask.Ask.AcctKey)

		order := MatchedOrder{
			Asker: Trader{
				AccountKey:     askAcct.TraderKeyRaw,
				AccountBalance: askAcct.Value,
			},
			Bidder: Trader{
				AccountKey:     bidAcct.TraderKeyRaw,
				AccountBalance: bidAcct.Value,
			},
			Details: OrderPair{
				Ask: ask,
				Bid: bid,
				Quote: PriceQuote{
					TotalSatsCleared: ask.Units.ToSatoshis(),
				},
			},
		}

		matchedOrders[i] = order
	}

	return matchedOrders
}

// For simplicity, we'll use the same clearing price of 1% for the
// entire batch.
const clearingPrice = orderT.FixedRatePremium(10000)

// TestTradingReportCompletion tests that each time we generate a trading
// report, it includes all the traders that were included in the set of matched
// orders.
func TestTradingReportCompletion(t *testing.T) {
	t.Parallel()

	acctDB, _, _ := newAcctCacher()

	feeSchedule := mockFeeSchedule{
		baseFee:    1,
		exeFeeRate: orderT.FixedRatePremium(10000),
	}
	n, y := 0, 0
	scenario := func(matchedOrders []MatchedOrder) bool {
		clearingPrices := map[uint32]orderT.FixedRatePremium{
			orderT.LegacyLeaseDurationBucket: clearingPrice,
		}
		subBatches := map[uint32][]MatchedOrder{
			orderT.LegacyLeaseDurationBucket: matchedOrders,
		}
		report := NewTradingFeeReport(
			subBatches, &feeSchedule, clearingPrices,
		)

		// First, we'll gather up all the traders that were in this
		// batch.
		matchedTraders := make(map[AccountID]struct{})
		for _, order := range matchedOrders {
			matchedTraders[order.Asker.AccountKey] = struct{}{}
			matchedTraders[order.Bidder.AccountKey] = struct{}{}
		}

		// The completion property means that ever trader should have
		// an account diff enclosed.
		for trader := range matchedTraders {
			if _, ok := report.AccountDiffs[trader]; !ok {
				t.Logf("trader %x not found in account diff", trader)
				return false
			}
		}

		y++
		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {
			v[0] = reflect.ValueOf(genRandMatchedOrders(
				r, acctDB,
			))
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("trader report completion violated: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestTradingReportGeneration verifies that the produced trading reports are
// well formed. All traders should have their diffs account for the orders they
// submitted, and the various fee stats properly computed.
func TestTradingReportGeneration(t *testing.T) {
	t.Parallel()

	acctDB, _, _ := newAcctCacher()

	feeSchedule := mockFeeSchedule{
		baseFee:    1,
		exeFeeRate: orderT.FixedRatePremium(10000),
	}
	n, y := 0, 0
	scenario := func(matchedOrders []MatchedOrder) bool {
		clearingPrices := map[uint32]orderT.FixedRatePremium{
			orderT.LegacyLeaseDurationBucket: clearingPrice,
		}
		subBatches := map[uint32][]MatchedOrder{
			orderT.LegacyLeaseDurationBucket: matchedOrders,
		}
		report := NewTradingFeeReport(
			subBatches, &feeSchedule, clearingPrices,
		)

		// The total amount of fees that each trader paid should
		// properly sum up to the total amount of fees the auctioneer
		// takes
		var totalFeesPaid btcutil.Amount
		for _, traderDiff := range report.AccountDiffs {
			totalFeesPaid += traderDiff.TotalExecutionFeesPaid
		}

		if totalFeesPaid != report.AuctioneerFeesAccrued {
			t.Logf("expected %v total fees, instead sum is %v",
				report.AuctioneerFeesAccrued, totalFeesPaid)
			return false
		}

		// For each trader (which should all be unique), their ending
		// balance should account for the orders the submitted as a
		// maker, and also the execution fees they paid.
		for _, orderMatch := range matchedOrders {
			matchDetails := orderMatch.Details
			maker := orderMatch.Asker
			taker := orderMatch.Bidder

			makerDiff := report.AccountDiffs[maker.AccountKey]
			makerStartingBalance := makerDiff.StartingState.AccountBalance

			// Makers need to debit their account by the amount
			// cleared, collect their premium, and also
			makerBalDiff := makerStartingBalance
			makerBalDiff -= matchDetails.Quote.TotalSatsCleared
			makerBalDiff += makerDiff.TotalMakerFeesAccrued
			makerBalDiff -= makerDiff.TotalExecutionFeesPaid

			takerDiff := report.AccountDiffs[taker.AccountKey]
			takerStartingBalance := takerDiff.StartingState.AccountBalance

			// Takers only need to pay execution fees, and gain
			// nothing.
			takerBalDiff := takerStartingBalance
			takerBalDiff -= takerDiff.TotalExecutionFeesPaid
			takerBalDiff -= takerDiff.TotalTakerFeesPaid

			// The balance should reflect the processed order as
			// well as all the execution fees paid.
			switch {
			case makerDiff.EndingBalance != makerBalDiff:
				t.Logf("wrong ending balance for maker: "+
					"expected %v, got %v",
					makerBalDiff,
					makerDiff.EndingBalance)
				return false

			case takerDiff.EndingBalance != takerBalDiff:
				t.Logf("wrong ending balance taker: "+
					"expected %v, got %v",
					takerBalDiff,
					takerDiff.EndingBalance)
				return false
			}
		}

		y++
		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {
			// We'll use a static duration for the orders generated
			// so we don't end up with massive premiums spanning
			// multiple years.
			//
			// We'll also clamp down on the order size as well as
			// the account size so the number are manageable.
			v[0] = reflect.ValueOf(
				genRandMatchedOrders(
					r, acctDB,
					staticDurationGen(2),
				),
			)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("trader report completion violated: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestLumpSumPremiumCalc tests certain properties of the LumpSumPremium method
// namely that it is monotonically increasing with respect to the total lease
// duration of the asset.
func TestLumpSumPremiumCalc(t *testing.T) {
	t.Parallel()

	n, y := 0, 0
	r := rand.New(rand.NewSource(time.Now().Unix()))
	scenario := func(amt uint32, duration uint16) bool {
		// We ensure that the premium is never over 100%, so we clam it
		// up to the max fixed point value.
		premiumA := orderT.FixedRatePremium(
			r.Int31n(int32(orderT.FeeRateTotalParts)),
		)
		premiumB := orderT.FixedRatePremium(
			r.Int31n(int32(orderT.FeeRateTotalParts)),
		)

		// We ask for the amount as a uint32 to ensure we don't get a
		// trillion satoshis or anything like that.
		satAmt := btcutil.Amount(amt)

		// If the first premium is less than the second, then we don't
		// care about this case.
		if premiumA < premiumB {
			n++
			return true
		}

		// Now we'll compute the lump sum payment for each premium
		// given a random duration.
		sumA := premiumA.LumpSumPremium(satAmt, uint32(duration))
		sumB := premiumB.LumpSumPremium(satAmt, uint32(duration))

		switch {
		// If the premiums are equal to each other then, the final
		// sums should be as well.
		case premiumA == premiumB && sumA != sumB:
			t.Logf("expected equal sums: %v vs %v", sumA, sumB)
			return false

		// Alternatively, if A > B, then we expect their sums to adhere
		// to the same relation.
		case premiumA > premiumB && sumA <= sumB:
			t.Logf("expected A > B, instead: %v vs %v", sumA, sumB)
			return false
		}

		y++
		return true
	}
	if err := quick.Check(scenario, nil); err != nil {
		t.Fatalf("lump sum premium scenario violated: %v", err)
	}
	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}
