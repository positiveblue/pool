package matching

import (
	"container/list"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
)

var (
	dummyFilterChain = []OrderFilter{
		NewBatchFeeRateFilter(chainfee.FeePerKwFloor),
	}
	testDuration = orderT.LegacyLeaseDurationBucket
)

func defaultBuckets() *order.DurationBuckets {
	buckets := order.NewDurationBuckets()
	buckets.PutMarket(testDuration, order.BucketStateClearingMarket)
	return buckets
}

// TestCallMarketConsiderForgetOrders tests that we're able to properly add and
// remove orders from the uniform price call market.
func TestCallMarketConsiderForgetOrders(t *testing.T) {
	t.Parallel()

	acctDB, _, _ := newAcctCacher()

	// In this scenario, we test that given a set of bids (clamping to
	// ensure we don't have too long a runtime. We're able to add all the
	// bids, then remove all of them resulting in an empty set of bids. The
	// bids index should also be blank at this point.
	scenario := func(orders orderSet) bool {
		callMarket := NewUniformPriceCallMarket(
			&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
			defaultBuckets(),
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

	_, acctCacher, predicates := newAcctCacher()
	callMarket := NewUniformPriceCallMarket(
		&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
		defaultBuckets(),
	)

	_, err := callMarket.MaybeClear(
		acctCacher, dummyFilterChain, predicates,
	)
	if err != ErrNoMarketPossible {
		t.Fatalf("expected ErrNoMarketPossible, instead got: %v", err)
	}
}

// TestMaybeClearNoClearPossible tests that if there isn't a clear possible,
// then we should receive an error and no matches are produced.
func TestMaybeClearNoClearPossible(t *testing.T) {
	t.Parallel()

	acctDB, acctCacher, predicates := newAcctCacher()

	// First, we'll generate a bid and ask that can't match as the rate
	// they request is incompatible.
	r := rand.New(rand.NewSource(time.Now().Unix()))
	bid := genRandBid(r, acctDB, staticRateGen(100))
	ask := genRandAsk(r, acctDB, staticRateGen(1000))

	// Next, we'll create our call market, and add these two incompatible
	// orders.
	callMarket := NewUniformPriceCallMarket(
		&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
		defaultBuckets(),
	)
	if err := callMarket.ConsiderBids(bid); err != nil {
		t.Fatalf("unable to add bids")
	}
	if err := callMarket.ConsiderAsks(ask); err != nil {
		t.Fatalf("unable to add asks")
	}

	// If we attempt to make a market, we should get the
	// ErrNoMarketPossible error.
	_, err := callMarket.MaybeClear(
		acctCacher, dummyFilterChain, predicates,
	)
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
func TestMaybeClearClearingPriceConsistency(t *testing.T) { // nolint:gocyclo
	t.Parallel()

	acctDB, acctCacher, predicates := newAcctCacher()

	y := 0
	scenario := func(orders orderSet) bool {
		// We'll start with making a new call market,
		callMarket := NewUniformPriceCallMarket(
			&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
			defaultBuckets(),
		)
		if err := callMarket.ConsiderBids(orders.Bids...); err != nil {
			t.Logf("unable to add bids")
			return false
		}
		if err := callMarket.ConsiderAsks(orders.Asks...); err != nil {
			t.Logf("unable to add asks")
			return false
		}

		// Check all bids and asks are found in the bid index.
		bidNonces := make(map[orderT.Nonce]struct{})
		for _, bid := range orders.Bids {
			if _, ok := callMarket.bidIndex[bid.Nonce()]; !ok {
				t.Logf("bid not found in index")
				return false
			}
			bidNonces[bid.Nonce()] = struct{}{}
		}

		askNonces := make(map[orderT.Nonce]struct{})
		for _, ask := range orders.Asks {
			if _, ok := callMarket.askIndex[ask.Nonce()]; !ok {
				t.Logf("ask not found in index")
				return false
			}
			askNonces[ask.Nonce()] = struct{}{}
		}

		// We'll now attempt to make a market, if no market can be
		// made, then we'll go to the next scenario.
		orderBatch, err := callMarket.MaybeClear(
			acctCacher, dummyFilterChain, predicates,
		)
		if err != nil {
			t.Logf("clear error: %v", err)
			return false
		}

		// Indexes should not be mutated.
		checkEqual := func(n map[orderT.Nonce]struct{},
			index map[orderT.Nonce]*list.Element) error {

			for k := range n {
				if _, ok := index[k]; ok {
					continue
				}
				return fmt.Errorf("nonce %v not found in index", k)
			}

			for k := range index {
				if _, ok := n[k]; ok {
					continue
				}
				return fmt.Errorf("nonce %v not found among nonces", k)
			}

			return nil
		}

		// We check that the OrderBatch's copy method properly copies
		// all data.
		batchCopy := orderBatch.Copy()
		require.Equal(t, orderBatch, &batchCopy)

		if err := checkEqual(bidNonces, callMarket.bidIndex); err != nil {
			t.Logf("not equal: %v", err)
			return false
		}

		if err := checkEqual(askNonces, callMarket.askIndex); err != nil {
			t.Logf("not equal: %v", err)
			return false
		}

		// At this point we know that we have a match. We remove the
		// matched orders from the order book, and check that it gets
		// updated accordingly.
		matchedOrders := orderBatch.Orders
		if err := callMarket.RemoveMatches(matchedOrders...); err != nil {
			t.Logf("unable to remove matches: %v", err)
			return false
		}

		// First, we'll ensure that the set of internal orders are
		// consistent.
		fullyConsumedOrders := make(map[orderT.Nonce]struct{})
		for _, matchedOrder := range matchedOrders {
			bid := matchedOrder.Details.Bid
			ask := matchedOrder.Details.Ask

			// Check that the bid and ask was among our original
			// orders.
			_, ok := bidNonces[bid.Nonce()]
			if !ok {
				t.Logf("bid not found among nonces")
				return false
			}
			_, ok = askNonces[ask.Nonce()]
			if !ok {
				t.Logf("ask not found among nonces")
				return false
			}

			// If the order set was totally filled or the
			// remaining units were less than its minimum match
			// size, then it shouldn't be found in current set of
			// active orders.
			if bid.UnitsUnfulfilled == 0 ||
				bid.UnitsUnfulfilled < bid.MinUnitsMatch {

				if _, ok := callMarket.bidIndex[bid.Nonce()]; ok {
					t.Logf("bid %v found in active orders "+
						"with unfulfilled units=%v "+
						"and min units=%v", bid.Nonce(),
						bid.UnitsUnfulfilled,
						bid.MinUnitsMatch)
					return false
				}

				bidNonce := bid.Nonce()
				fullyConsumedOrders[bidNonce] = struct{}{}
			}
			if ask.UnitsUnfulfilled == 0 ||
				ask.UnitsUnfulfilled < ask.MinUnitsMatch {

				if _, ok := callMarket.askIndex[ask.Nonce()]; ok {
					t.Logf("ask %v found in active orders "+
						"with unfulfilled units=%v "+
						"and min units=%v", ask.Nonce(),
						ask.UnitsUnfulfilled,
						ask.MinUnitsMatch)

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
		resultClearingPrice := orderBatch.ClearingPrices[testDuration]
		var clearingPriceFound bool
		for _, orderMatch := range matchedOrders {
			quote := orderMatch.Details.Quote
			if quote.MatchingRate == resultClearingPrice {
				clearingPriceFound = true
				break
			}
		}

		if !clearingPriceFound {
			t.Logf("clearing price of %v not found in set of "+
				"matched orders", resultClearingPrice)
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
			randOrderSet := genRandOrderSet(
				r, acctDB,
				1000,
				staticDurationGen(testDuration),
				oneOfUnitGen(50, 100, 200, 400, 777, 1000),
			)

			// We'll also supplements this set of orders with an
			// pair of orders that we know will be totally filled.
			sameUnitSet := genRandOrderSet(
				r, acctDB,
				1,
				staticRateGen(1000),
				staticUnitGen(1000),
				staticDurationGen(testDuration),
				staticMinUnitsMatchGen(1),
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

	t.Logf("Total number of scenarios run: %v (%v positive)", y, y)
}

// TestMaybeClearFilterFeeRates tests that orders with a max batch feerate
// below the current fee estimate won't be considered during match making.
func TestMaybeClearFilterFeeRates(t *testing.T) {
	t.Parallel()

	acctDB, acctCacher, predicates := newAcctCacher()

	r := rand.New(rand.NewSource(time.Now().Unix()))

	// Create 6 bids and asks, with increasing max batch fee rates.
	var bids []*order.Bid
	var asks []*order.Ask
	for i := 0; i < 6; i++ {
		bid := genRandBid(
			r, acctDB, staticRateGen(1000), staticUnitGen(10),
			staticMinUnitsMatchGen(1), staticDurationGen(2016),
		)
		ask := genRandAsk(
			r, acctDB, staticRateGen(1000), staticUnitGen(10),
			staticMinUnitsMatchGen(1), staticDurationGen(2016),
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
		defaultBuckets(),
	)
	if err := callMarket.ConsiderBids(bids...); err != nil {
		t.Fatalf("unable to add bids")
	}
	if err := callMarket.ConsiderAsks(asks...); err != nil {
		t.Fatalf("unable to add asks")
	}

	// If we attempt to make a market with a fee rate above all the orders'
	// max fee rate, we should get the ErrNoMarketPossible error.
	_, err := callMarket.MaybeClear(
		acctCacher, []OrderFilter{NewBatchFeeRateFilter(
			7 * chainfee.FeePerKwFloor,
		)}, predicates,
	)
	if err != ErrNoMarketPossible {
		t.Fatalf("expected ErrNoMarketPossible, got: %v", err)
	}

	// Now make a market with a fee rate that should make exactly 3 matches
	// possible.
	orderBatch, err := callMarket.MaybeClear(
		acctCacher, []OrderFilter{NewBatchFeeRateFilter(
			4 * chainfee.FeePerKwFloor,
		)}, predicates,
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	matches := orderBatch.Orders
	if len(matches) != 3 {
		t.Fatalf("expected 3 matches, got %v", len(matches))
	}
}

// TestMaybeClearClearingPriceInvariant checks that the set of matched orders
// returned from MaybeClear all satisfy the clearing price extracted.
func TestMaybeClearClearingPriceInvariant(t *testing.T) {
	t.Parallel()

	acctDB, acctCacher, predicates := newAcctCacher()

	n, y := 0, 0
	scenario := func(orders orderSet) bool {
		// LastAcceptedBid is the clearing price model used.
		callMarket := NewUniformPriceCallMarket(
			&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
			defaultBuckets(),
		)

		// Add all orders from the scenario to the call market, and
		// clear the batch.
		if err := callMarket.ConsiderBids(orders.Bids...); err != nil {
			t.Fatalf("unable to add bids: %v", err)
		}
		if err := callMarket.ConsiderAsks(orders.Asks...); err != nil {
			t.Fatalf("unable to add asks: %v", err)
		}

		orderBatch, err := callMarket.MaybeClear(
			acctCacher, dummyFilterChain, predicates,
		)
		switch {
		case err == ErrNoMarketPossible:
			n++
			return true

		case err != nil:
			t.Logf("unable to match batch: %v", err)
			return false
		}

		matchedOrders := orderBatch.Orders
		clearingPrice := orderBatch.ClearingPrices[testDuration]

		// If there's no match, ErrNoMarketPossible should have been
		// returned, so we consider this a failure.
		if len(matchedOrders) == 0 {
			t.Logf("matched order set empty")
			return false
		}

		// Check that all orders had their fixed rate satisfied by the
		// clearing price.
		for _, orderPair := range matchedOrders {
			ask := orderPair.Details.Ask
			bid := orderPair.Details.Bid

			if ask.FixedRate > uint32(clearingPrice) {
				t.Logf("clearing price of %v not "+
					"satisfying ask rate of %v",
					clearingPrice, ask.FixedRate)
				return false
			}

			if bid.FixedRate < uint32(clearingPrice) {
				t.Logf("clearing price of %v not "+
					"satisfying bid rate of %v",
					clearingPrice, bid.FixedRate)
				return false
			}
		}
		y++
		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {
			// When generating the random set below, we'll cap the
			// number of orders on both sides to ensure the test
			// completes in a timely manner.
			randOrderSet := genRandOrderSet(
				r, acctDB,
				1000,
				staticDurationGen(testDuration),
			)

			v[0] = reflect.ValueOf(randOrderSet)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("MaybeClear scenario failed: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestMatchingAccountNotReady ensures that accounts within a batch matching
// attempt are ready to participate in the batch.
func TestMatchingAccountNotReady(t *testing.T) {
	t.Parallel()

	r := rand.New(rand.NewSource(time.Now().Unix()))
	acctDB, acctCacher, predicates := newAcctCacher()

	ask := genRandAsk(r, acctDB)
	asks := []*order.Ask{ask}

	bid := genRandBid(
		r,
		acctDB,
		staticUnitGen(ask.Units),
		staticDurationGen(ask.LeaseDuration()),
		staticRateGen(ask.FixedRate),
		staticAccountState(account.StatePendingOpen),
	)
	bids := []*order.Bid{bid}

	assertNotInBatch(t, acctCacher, predicates, asks, bids)
}

// TestMatchingAccountNotReadyCloseToExpiry tests that if an account is close
// to being expired, then it's excluded from being cleared in a batch.
func TestMatchingAccountNotReadyCloseToExpiry(t *testing.T) {
	t.Parallel()

	r := rand.New(rand.NewSource(time.Now().Unix()))
	acctDB, acctCacher, predicates := newAcctCacher()
	matchMaker := NewMultiUnitMatchMaker(acctCacher, predicates)

	const accountExpiryOffset = 144
	currentHeight := uint32(1000)
	acctCacher.accountExpiryCutoff = currentHeight + accountExpiryOffset

	// We'll create another set of bids that should match, however the
	// account of the Bidder will be close to final expiry. This should
	// result in no match being found.
	askDuration := uint32(1000)
	targetRate := uint32(1000)
	askSupply := uint16(100)
	ask := genRandAsk(
		r, acctDB,
		staticUnitGen(orderT.SupplyUnit(askSupply)),
		staticMinUnitsMatchGen(1),
		staticDurationGen(askDuration), staticRateGen(targetRate),
	)
	bid := genRandBid(
		r,
		acctDB,
		staticUnitGen(orderT.SupplyUnit(askSupply)),
		staticMinUnitsMatchGen(1),
		staticDurationGen(askDuration), staticRateGen(targetRate),
	)

	asks := []*order.Ask{ask}
	bids := []*order.Bid{bid}

	// As the expiry is one block before the current height, it shouldn't
	// be able to join the batch.
	acctDB.accts[bid.NodeKey].Expiry = currentHeight - 1

	assertNotInBatch(t, acctCacher, predicates, asks, bids)

	// We'll invalidate the cache so we fetch the latest state for the next
	// attempt.
	matchMaker.accountCacher.(*AccountFilter).accountCache = make(
		map[[33]byte]*account.Account,
	)

	// If we modify the expiry to be _right before_ our cut off, it still
	// shouldn't be allowed in the batch due to the strictness of the
	// check.
	acctDB.accts[bid.NodeKey].Expiry = currentHeight + accountExpiryOffset

	assertNotInBatch(t, acctCacher, predicates, asks, bids)

	// Finally, if we set a height well ahead of our cut off, then we
	// should have a match.
	matchMaker.accountCacher.(*AccountFilter).accountCache = make(
		map[[33]byte]*account.Account,
	)
	acctDB.accts[bid.NodeKey].Expiry = currentHeight + (accountExpiryOffset * 2)

	matchSet, err := matchMaker.MatchBatch(bids, asks)
	require.NoError(t, err)
	require.NotEmpty(t, matchSet.MatchedOrders)
}

func assertNotInBatch(t *testing.T, acctCacher *AccountFilter,
	predicates []MatchPredicate, asks []*order.Ask, bids []*order.Bid) {

	// LastAcceptedBid is the clearing price model used.
	callMarket := NewUniformPriceCallMarket(
		&LastAcceptedBid{}, &mockFeeSchedule{1, 100000},
		defaultBuckets(),
	)
	filterChain := []OrderFilter{acctCacher}

	// Add all orders from the scenario to the call market, and
	// clear the batch.
	if err := callMarket.ConsiderBids(bids...); err != nil {
		t.Fatalf("unable to add bids: %v", err)
	}
	if err := callMarket.ConsiderAsks(asks...); err != nil {
		t.Fatalf("unable to add asks: %v", err)
	}

	orderBatch, err := callMarket.MaybeClear(
		acctCacher, filterChain, predicates,
	)

	require.Error(t, err)
	require.Equal(t, ErrNoMarketPossible, err)
	require.Nil(t, orderBatch)
}
