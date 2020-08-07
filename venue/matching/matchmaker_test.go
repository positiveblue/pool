package matching

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// TestCallMarketConsiderForgetOrders tests that we're able to properly add and
// remove orders from the uniform price call market.
func TestCallMarketConsiderForgetOrders(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()

	// In this scenario, we test that given a set of bids (clamping to
	// ensure we don't have too long a runtime. We're able to add all the
	// bids, then remove all of them resulting in an empty set of bids. The
	// bids index should also be blank at this point.
	scenario := func(orders orderSet) bool {
		callMarket := NewUniformPriceCallMarket(
			&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
			acctDB.fetchAcct,
		)

		if err := callMarket.ConsiderBids(orders.Bids...); err != nil {
			t.Fatalf("unable to add bids")
			return false
		}

		// Trying to remove a bid that doesn't exist should have no
		// effect on the bids inserted.
		var blankNonce orderT.Nonce
		if err := callMarket.ForgetBids(blankNonce); err != nil {
			t.Fatalf("unable to forget asks")
			return false
		}

		// We'll add the set of bids again to ensure no bids are double
		// added.
		if err := callMarket.ConsiderBids(orders.Bids...); err != nil {
			t.Fatalf("unable to forget bids")
			return false
		}

		// At this point, ever bid that we added should be found in the
		// bidIndex.
		for _, bid := range orders.Bids {
			if _, ok := callMarket.bidIndex[bid.Nonce()]; !ok {
				t.Fatalf("bid not found!")
			}
		}

		// Next we'll add our set of adds and repeat the same assertion
		// (adding twice to ensure they aren't double counted).
		if err := callMarket.ConsiderAsks(orders.Asks...); err != nil {
			t.Logf("unable to add bids")
			return false
		}
		if err := callMarket.ConsiderAsks(orders.Asks...); err != nil {
			t.Logf("unable to add asks")
			return false
		}

		// Trying to remove a bid that doesn't exist should have no
		// effect on the bids inserted.
		if err := callMarket.ForgetAsks(blankNonce); err != nil {
			t.Logf("unable to forget asks")
			return false
		}

		if err := callMarket.ForgetBids(blankNonce); err != nil {
			t.Logf("unable to forget asks")
			return false
		}
		for _, ask := range orders.Asks {
			if _, ok := callMarket.askIndex[ask.Nonce()]; !ok {
				t.Fatalf("ask not found!")
			}
		}

		// The total number of bids and asks should match the amount we
		// inserted above.
		switch {
		case len(callMarket.bidIndex) != len(orders.Bids):

			t.Fatalf("wrong number of bids: got %v, expected %v",
				callMarket.bids.Len(), len(orders.Bids))
			return false

		case len(callMarket.askIndex) != len(orders.Asks):

			t.Fatalf("wrong number of asks: got %v, expected %v",
				callMarket.asks.Len(), len(orders.Asks))
			return false
		}

		// Now if we forget all the bids and asks, both the internal
		// list as well as the index should be empty.
		for _, bid := range orders.Bids {
			if err := callMarket.ForgetBids(bid.Nonce()); err != nil {
				t.Logf("unable to forget bids")
				return false
			}
		}
		for _, ask := range orders.Asks {
			if err := callMarket.ForgetAsks(ask.Nonce()); err != nil {
				t.Logf("unable to forget asks")
				return false
			}
		}

		switch {
		case len(callMarket.bidIndex) != 0:
			t.Logf("bid index not size zero, instead: %v",
				len(callMarket.bidIndex))
			return false

		case len(callMarket.askIndex) != 0:
			t.Logf("ask index not size zero, instead: %v",
				len(callMarket.askIndex))
			return false

		case callMarket.bids.Len() != 0:
			t.Logf("bid list not size zero, instead: %v",
				callMarket.bids.Len())
			return false

		case callMarket.asks.Len() != 0:
			t.Logf("ask list not size zero, instead: %v",
				callMarket.asks.Len())
			return false
		}

		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {
			// When generating the random set below, we'll cap the
			// number of orders on both sides to ensure the test
			// completes in a timely manner.
			randOrderSet := genRandOrderSet(r, acctDB, 1000)

			v[0] = reflect.ValueOf(randOrderSet)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("call market order scenario: %v", err)
	}
}

// TestMaybeClearNoOrders tests that if no orders exist, and a match is
// attempted, we get back ErrNoMarketPossible.
func TestMaybeClearNoOrders(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()
	callMarket := NewUniformPriceCallMarket(
		&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
		acctDB.fetchAcct,
	)

	_, err := callMarket.MaybeClear(BatchID{}, chainfee.FeePerKwFloor)
	if err != ErrNoMarketPossible {
		t.Fatalf("expected ErrNoMarketPossible, instead got: %v", err)
	}
}

// TestMaybeClearNoClearPossible tests that if there isn't a clear possible,
// then we should receive an error and no matches are produced.
func TestMaybeClearNoClearPossible(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()

	// First, we'll generate a bid and ask that can't match as the rate
	// they request is incompatible.
	r := rand.New(rand.NewSource(time.Now().Unix()))
	bid := genRandBid(r, acctDB, staticRateGen(100))
	ask := genRandAsk(r, acctDB, staticRateGen(1000))

	// Next, we'll create our call market, and add these two incompatible
	// orders.
	callMarket := NewUniformPriceCallMarket(
		&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
		acctDB.fetchAcct,
	)
	if err := callMarket.ConsiderBids(bid); err != nil {
		t.Fatalf("unable to add bids")
	}
	if err := callMarket.ConsiderAsks(ask); err != nil {
		t.Fatalf("unable to add asks")
	}

	// If we attempt to make a market, we should get the
	// ErrNoMarketPossible error.
	_, err := callMarket.MaybeClear(BatchID{}, chainfee.FeePerKwFloor)
	if err != ErrNoMarketPossible {
		t.Fatalf("expected ErrNoMarketPossible, got: %v", err)
	}

	// The bid and ask that we added above, should also still remain in
	// place within the call market.
	if _, ok := callMarket.bidIndex[bid.Nonce()]; !ok {
		t.Fatalf("bid not found in market")
	}
	if _, ok := callMarket.askIndex[ask.Nonce()]; !ok {
		t.Fatalf("ask not found in market")
	}
}

// TestMaybeClearClearingPriceConsistency tests that once a market has been
// cleared, all internal state is consistent with the outcome of the matching
// event.
func TestMaybeClearClearingPriceConsistency(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()

	n, y := 0, 0
	scenario := func(orders orderSet) bool {
		// We'll start with making a new call market,
		callMarket := NewUniformPriceCallMarket(
			&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
			acctDB.fetchAcct,
		)
		if err := callMarket.ConsiderBids(orders.Bids...); err != nil {
			t.Logf("unable to add bids")
			return false
		}
		if err := callMarket.ConsiderAsks(orders.Asks...); err != nil {
			t.Logf("unable to add asks")
			return false
		}

		// We'll now attempt to make a market, if no market can be
		// made, then we'll go to the next scenario.
		orderBatch, err := callMarket.MaybeClear(
			BatchID{}, chainfee.FeePerKwFloor,
		)
		if err != nil {
			fmt.Println("clear error: ", err)
			n++
			return true
		}

		// At this point we know that we have a match.
		//
		// First, we'll ensure that the set of internal orders are
		// consistent.
		matchedOrders := orderBatch.Orders
		fullyConsumedOrders := make(map[orderT.Nonce]struct{})
		for _, matchedOrder := range matchedOrders {
			bid := matchedOrder.Details.Ask
			ask := matchedOrder.Details.Bid

			// If the order set was totally filled, then it
			// shouldn't be found in current set of active orders.
			if bid.UnitsUnfulfilled == 0 {
				if _, ok := callMarket.bidIndex[bid.Nonce()]; ok {
					t.Logf("bid found in active orders " +
						"after total fulfill")
					return false
				}

				bidNonce := bid.Nonce()
				fullyConsumedOrders[bidNonce] = struct{}{}
			}
			if ask.UnitsUnfulfilled == 0 {
				if _, ok := callMarket.askIndex[ask.Nonce()]; ok {
					t.Logf("ask found in active orders " +
						"after total fulfill")
					return false
				}

				askNonce := ask.Nonce()
				fullyConsumedOrders[askNonce] = struct{}{}
			}
		}

		// Next, any order that wasn't fully consumed, should be found
		// in the set of active orders.
		for _, order := range matchedOrders {
			bid := order.Details.Bid
			if _, ok := fullyConsumedOrders[bid.Nonce()]; !ok {
				if _, ok := callMarket.bidIndex[bid.Nonce()]; !ok {
					t.Logf("partial bid fill not found in " +
						"active order set")
					return false
				}
			}

			ask := order.Details.Ask
			if _, ok := fullyConsumedOrders[ask.Nonce()]; !ok {
				if _, ok := callMarket.askIndex[ask.Nonce()]; !ok {
					t.Logf("partial ask fill not found in " +
						"active order set")
					return false
				}
			}
		}

		// Finally, the final clearing price should be the same as at
		// least one of the matched pairs. We use this type of
		// assertion as it makes it generic w.r.t the clearing price
		// algo used.
		var clearingPriceFound bool
		for _, orderMatch := range matchedOrders {
			quote := orderMatch.Details.Quote
			if quote.MatchingRate == orderBatch.ClearingPrice {
				clearingPriceFound = true
				break
			}
		}

		if !clearingPriceFound {
			t.Logf("clearing price of %v not found in set of "+
				"matched orders", orderBatch.ClearingPrice)
			return false
		}

		y++
		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {
			// For this case, we'll generate a set of random
			// orders, as we'll get to test both the case where no
			// orders match, or only a sub-set of the orders match
			// and the intermediate state needs to be updated.
			randOrderSet := genRandOrderSet(r, acctDB, 1000)

			// We'll also supplements this set of orders with an
			// pair of orders that we know will be totally filled.
			sameUnitSet := genRandOrderSet(
				r, acctDB,
				1,
				staticRateGen(1000),
				staticUnitGen(1000),
				staticDurationGen(2),
			)

			randOrderSet.Bids = append(
				randOrderSet.Bids, sameUnitSet.Bids...,
			)
			randOrderSet.Asks = append(
				randOrderSet.Asks, sameUnitSet.Asks...,
			)

			v[0] = reflect.ValueOf(randOrderSet)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("clearing price consistency scenario failed: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestMaybeClearFilterFeeRates tests that orders with a max batch feerate
// below the current fee estimate won't be considered during match making.
func TestMaybeClearFilterFeeRates(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()

	r := rand.New(rand.NewSource(time.Now().Unix()))

	// Create 6 bids and asks, with increasing max batch fee rates.
	var bids []*order.Bid
	var asks []*order.Ask
	for i := 0; i < 6; i++ {
		bid := genRandBid(
			r, acctDB, staticRateGen(1000), staticUnitGen(10),
			staticDurationGen(144),
		)
		ask := genRandAsk(
			r, acctDB, staticRateGen(1000), staticUnitGen(10),
			staticDurationGen(144),
		)

		feeRate := chainfee.FeePerKwFloor * chainfee.SatPerKWeight(i+1)
		bid.MaxBatchFeeRate = feeRate
		ask.MaxBatchFeeRate = feeRate

		bids = append(bids, bid)
		asks = append(asks, ask)
	}

	// Next, we'll create our call market, and add the orders.
	callMarket := NewUniformPriceCallMarket(
		&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
		acctDB.fetchAcct,
	)
	if err := callMarket.ConsiderBids(bids...); err != nil {
		t.Fatalf("unable to add bids")
	}
	if err := callMarket.ConsiderAsks(asks...); err != nil {
		t.Fatalf("unable to add asks")
	}

	// If we attempt to make a market with a fee rate above all the orders'
	// max fee rate, we should get the ErrNoMarketPossible error.
	_, err := callMarket.MaybeClear(BatchID{}, 7*chainfee.FeePerKwFloor)
	if err != ErrNoMarketPossible {
		t.Fatalf("expected ErrNoMarketPossible, got: %v", err)
	}

	// Now make a market with a fee rate that should make exactly 3 matches
	// possible.
	orderBatch, err := callMarket.MaybeClear(BatchID{}, 4*chainfee.FeePerKwFloor)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	matches := orderBatch.Orders
	if len(matches) != 3 {
		t.Fatalf("expected 3 matches, got %v", len(matches))
	}
}
