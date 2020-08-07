package matching

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
)

var emptyAcct [33]byte

type orderGenCfg struct {
	numUnits  orderT.SupplyUnit
	fixedRate uint32
	duration  uint32
	acctKey   [33]byte
	acctState *account.State
}

type orderGenOption func(*orderGenCfg)

func staticUnitGen(numUnits orderT.SupplyUnit) orderGenOption {
	return func(opt *orderGenCfg) {
		opt.numUnits = numUnits
	}
}

func staticDurationGen(duration uint32) orderGenOption {
	return func(opt *orderGenCfg) {
		opt.duration = duration
	}
}

func minDurationGenBound(minDuration uint32, r *rand.Rand) orderGenOption {
	duration := uint32(r.Int31()) + minDuration
	return func(opt *orderGenCfg) {
		opt.duration = duration
	}
}

func maxDurationGenBound(maxDuration uint32, r *rand.Rand) orderGenOption {
	duration := uint32(r.Int31())
	if duration > maxDuration {
		duration = maxDuration - 1
	}

	return func(opt *orderGenCfg) {
		opt.duration = duration
	}
}

func minRateGenBound(minRate uint32, r *rand.Rand) orderGenOption {
	rate := uint32(r.Int31()) + minRate
	return func(opt *orderGenCfg) {
		opt.fixedRate = rate
	}
}

func maxRateGenBound(maxRate uint32, r *rand.Rand) orderGenOption {
	rate := uint32(r.Int31())
	if rate > maxRate {
		rate = maxRate - 1
	}

	return func(opt *orderGenCfg) {
		opt.fixedRate = rate
	}
}

func staticRateGen(rate uint32) orderGenOption {
	return func(opt *orderGenCfg) {
		opt.fixedRate = rate
	}
}

func staticAccountKeyGen(acctKey [33]byte) orderGenOption {
	return func(opt *orderGenCfg) {
		opt.acctKey = acctKey
	}
}

func staticAccountState(state account.State) orderGenOption {
	return func(opt *orderGenCfg) {
		opt.acctState = &state
	}
}

func (o *orderGenCfg) supplyUnits(r *rand.Rand) orderT.SupplyUnit {
	if o.numUnits != 0 {
		return o.numUnits
	}

	return orderT.SupplyUnit(r.Int31())
}

func (o *orderGenCfg) rate(r *rand.Rand) uint32 {
	if o.fixedRate != 0 {
		return o.fixedRate
	}

	return uint32(r.Int31())
}

func (o *orderGenCfg) leaseDuration(r *rand.Rand) uint32 {
	if o.duration != 0 {
		return o.duration
	}

	return uint32(r.Int31())
}

func (o *orderGenCfg) traderKey(r *rand.Rand) [33]byte { // nolint:interfacer
	if o.acctKey != emptyAcct {
		return o.acctKey
	}

	var key [33]byte
	_, _ = r.Read(key[:])

	return key
}

func (o *orderGenCfg) accountState(*rand.Rand) account.State {
	if o.acctState != nil {
		return *o.acctState
	}
	return account.StateOpen
}

func genRandAccount(r *rand.Rand, genOptions ...orderGenOption) *account.Account {
	genCfg := orderGenCfg{}
	for _, optionModifier := range genOptions {
		optionModifier(&genCfg)
	}

	return &account.Account{
		TraderKeyRaw: genCfg.traderKey(r),
		Value:        btcutil.Amount(r.Int63()),
		Expiry:       uint32(r.Int31()),
		State:        genCfg.accountState(r),
	}
}

func genRandBid(r *rand.Rand, accts *acctFetcher, // nolint:dupl
	genOptions ...orderGenOption) *order.Bid {

	genCfg := orderGenCfg{}
	for _, optionModifier := range genOptions {
		optionModifier(&genCfg)
	}

	// TODO(roasbeef): also need to generate fake account for the internal
	// pointer

	var nonce orderT.Nonce
	_, _ = r.Read(nonce[:])

	acct := genRandAccount(r, genOptions...)

	numUnits := genCfg.supplyUnits(r)
	b := &order.Bid{
		Bid: orderT.Bid{
			Kit:         *orderT.NewKit(nonce),
			MinDuration: genCfg.leaseDuration(r),
		},
	}
	b.Amt = numUnits.ToSatoshis()
	b.Units = numUnits
	b.UnitsUnfulfilled = numUnits
	b.FixedRate = genCfg.rate(r)
	b.MaxBatchFeeRate = chainfee.FeePerKwFloor

	copy(b.Bid.Kit.AcctKey[:], acct.TraderKeyRaw[:])
	b.NodeKey = b.Bid.Kit.AcctKey

	// If the trader's account doesn't have at least the total amount of
	// the order, then we'll clamp the value.
	if acct.Value < b.Amt {
		acct.Value = b.Amt
	}

	accts.accts[acct.TraderKeyRaw] = acct

	return b
}

func genRandAsk(r *rand.Rand, accts *acctFetcher, // nolint:dupl
	genOptions ...orderGenOption) *order.Ask {

	genCfg := orderGenCfg{}
	for _, optionModifier := range genOptions {
		optionModifier(&genCfg)
	}

	var nonce orderT.Nonce
	_, _ = r.Read(nonce[:])

	acct := genRandAccount(r, genOptions...)

	numUnits := genCfg.supplyUnits(r)
	a := &order.Ask{
		Ask: orderT.Ask{
			Kit:         *orderT.NewKit(nonce),
			MaxDuration: genCfg.leaseDuration(r),
		},
	}
	a.Amt = numUnits.ToSatoshis()
	a.Units = numUnits
	a.UnitsUnfulfilled = numUnits
	a.FixedRate = genCfg.rate(r)
	a.MaxBatchFeeRate = chainfee.FeePerKwFloor

	copy(a.Ask.Kit.AcctKey[:], acct.TraderKeyRaw[:])
	a.NodeKey = a.Ask.Kit.AcctKey

	// If the trader's account doesn't have at least the total amount of
	// the order, then we'll clamp the value.
	if acct.Value < a.Amt {
		acct.Value = a.Amt
	}

	accts.accts[acct.TraderKeyRaw] = acct

	return a
}

type orderPair struct {
	Bid *order.Bid
	Ask *order.Ask
}

type acctFetcher struct {
	accts map[[33]byte]*account.Account
}

func newAcctFetcher() *acctFetcher {
	return &acctFetcher{
		accts: make(map[[33]byte]*account.Account),
	}
}

func (a *acctFetcher) fetchAcct(k AccountID) (*account.Account, error) {
	acct, ok := a.accts[k]
	if !ok {
		return nil, fmt.Errorf("unable to find acct")
	}

	return acct, nil
}

// TestMatchPossibleSpreadNeverMatches tests that given a bid and an ask, if
// the bid has a price lower than the ask, then a match never occurs.
func TestMatchPossibleSpreadNeverMatches(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	n, y := 0, 0
	scenario := func(testPair orderPair) bool {
		// If the bid rate is greater than the ask rate, then this
		// passes our scenario (should be a match).
		if testPair.Bid.FixedRate >= testPair.Ask.FixedRate {
			n++
			return true
		}

		// Instead we care about the case where the bid rate is _less_
		// than the ask, this shouldn't trigger any matches.
		quote, match := matchMaker.MatchPossible(
			testPair.Bid, testPair.Ask,
		)
		if match {
			t.Fatalf("expected no match due to rate spread")
			return false
		}

		// As no match was possible, we should receive a NullQuote
		// which signals that no match occurred.
		if quote != NullQuote {
			t.Fatalf("expected null quote due to rate spread")
			return false
		}

		y++

		// If we get to this point, then our scenario has been upheld:
		// a spread should never trigger a match.
		return true
	}

	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			randOrderPair := orderPair{
				Bid: genRandBid(r, acctDB),
				Ask: genRandAsk(r, acctDB),
			}

			v[0] = reflect.ValueOf(randOrderPair)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("spread no match property violated: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestMatchPossibleDurationIncompatability tests that if the min duration of a
// bid is greater than the mask duration of an ask, then no match will happen.
func TestMatchPossibleDurationIncompatability(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	n, y := 0, 0
	scenario := func(testPair orderPair) bool {
		// If the min duration of the bid is less than the min duration
		// of an ask, then we can skip this case.
		if testPair.Bid.MinDuration() < testPair.Ask.MaxDuration() {
			n++
			return true
		}

		// Instead we care about the case where the bid min duration is
		// greater than the ask. This shouldn't trigger any matches.
		quote, match := matchMaker.MatchPossible(
			testPair.Bid, testPair.Ask,
		)
		if match {
			t.Fatalf("expected no match due to duration spread")
			return false
		}

		// As no match was possible, we should receive a NullQuote
		// which signals that no match occurred.
		if quote != NullQuote {
			t.Fatalf("expected null quote due to duration spread")
			return false
		}

		// If we get to this point, then our scenario has been upheld:
		// a spread should never trigger a match.
		y++
		return true
	}

	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			randOrderPair := orderPair{
				Bid: genRandBid(r, acctDB),
				Ask: genRandAsk(r, acctDB),
			}

			v[0] = reflect.ValueOf(randOrderPair)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("spread no match property violated: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestMatchPossibleMatchPartialBid tests that if an match is possible, and the
// number of supply units of the bid is greater than the number of units for
// the ask, then we output a partial bid match.
func TestMatchPossibleMatchPartialBid(t *testing.T) { // nolint:dupl
	t.Parallel()

	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	n, y := 0, 0
	scenario := func(testPair orderPair) bool {
		// As we'll only enter this section if a line is possible, we
		// should receive a positive match.
		quote, match := matchMaker.MatchPossible(
			testPair.Bid, testPair.Ask,
		)

		// If we failed to match, then we'll continue as we already
		// know that due to the above tests, we'll never match on a
		// rate or duration spread.
		if quote == NullQuote {
			n++
			return true
		}

		// As the quote isn't null, we should have a match at this
		// point.
		if !match {
			t.Logf("got match with null quote")
			return false
		}

		// At this point, we know we have a match, but the properties
		// below will only hold if the number of units of the bid was
		// greater than the ask, so we'll assert that here.
		if testPair.Bid.Units <= testPair.Ask.Units {
			n++
			return true
		}

		// As the number of supply units for the bid was greater than
		// the ask, we should receive a partial bid fulfill.
		if quote.Type != PartialBidFulfill {
			t.Fatalf("expected partial bid fulfill, instead "+
				"got %v", quote.Type)
			return false
		}

		// The number of units matched should be the difference between
		// the number of bid units and the number of ask units.
		unitsDiff := testPair.Bid.Units - testPair.Ask.Units
		if quote.UnitsUnmatched != unitsDiff {
			t.Fatalf("expected %v unit unmatched, instead got %v",
				unitsDiff, quote.UnitsUnmatched)
			return false
		}

		// Conversely, the number of units matched should be equal to
		// the available number of units for the ask.
		if quote.UnitsMatched != testPair.Ask.Units {
			t.Fatalf("expected %v unit matched, instead got %v",
				testPair.Ask.Units, quote.UnitsMatched)
			return false
		}

		// If we get to this point, then our scenario has been upheld:
		// a spread should never trigger a match.
		y++
		return true
	}

	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			randOrderPair := orderPair{
				Bid: genRandBid(r, acctDB),
				Ask: genRandAsk(r, acctDB),
			}

			v[0] = reflect.ValueOf(randOrderPair)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("partial bid match scenario failed: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestMatchPossibleMatchPartialAsk tests that if we have a match, and the
// supply units of the ask is greater than the bid, then we should get a
// partial ask match.
func TestMatchPossibleMatchPartialAsk(t *testing.T) { // nolint:dupl
	t.Parallel()

	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	n, y := 0, 0
	scenario := func(testPair orderPair) bool {
		// As we'll only enter this section if a line is possible, we
		// should receive a positive match.
		quote, match := matchMaker.MatchPossible(
			testPair.Bid, testPair.Ask,
		)

		// If we failed to match, then we'll continue as we already
		// know that due to the above tests, we'll never match on a
		// rate or duration spread.
		if quote == NullQuote {
			n++
			return true
		}

		// As the quote isn't null, we should have a match at this
		// point.
		if !match {
			t.Logf("got match with null quote")
			return false
		}

		// At this point, we know we have a match, but the properties
		// below will only hold if the number of units of the ask was
		// greater than the bid, so we'll assert that here.
		if testPair.Ask.Units <= testPair.Bid.Units {
			n++
			return true
		}

		// As the number of supply units for the bid was greater than
		// the ask, we should receive a partial ask fulfill.
		if quote.Type != PartialAskFulfill {
			t.Fatalf("expected partial ask fulfill, instead "+
				"got %v", quote.Type)
			return false
		}

		// The number of units matched should be the difference between
		// the number of bid units and the number of ask units.
		unitsDiff := testPair.Ask.Units - testPair.Bid.Units
		if quote.UnitsUnmatched != unitsDiff {
			t.Fatalf("expected %v unit unmatched, instead got %v",
				unitsDiff, quote.UnitsUnmatched)
			return false
		}

		// Conversely, the number of units matched should be equal to
		// the available number of units for the bid.
		if quote.UnitsMatched != testPair.Bid.Units {
			t.Fatalf("expected %v unit matched, instead got %v",
				testPair.Bid.Units, quote.UnitsMatched)
			return false
		}

		// If we get to this point, then our scenario has been upheld:
		// a spread should never trigger a match.
		y++
		return true
	}

	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			randOrderPair := orderPair{
				Bid: genRandBid(r, acctDB),
				Ask: genRandAsk(r, acctDB),
			}

			v[0] = reflect.ValueOf(randOrderPair)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("partial bid match scenario failed: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestMatchPossibleMatchFullFill tests that if we have a match, and the number
// of units for the bid and ask are equal to each other, then we have a full
// match.
func TestMatchPossibleMatchFullFill(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	n, y := 0, 0
	scenario := func(testPair orderPair) bool {
		// As we'll only enter this section if a line is possible, we
		// should receive a positive match.
		quote, match := matchMaker.MatchPossible(
			testPair.Bid, testPair.Ask,
		)

		// If we failed to match, then we'll continue as we already
		// know that due to the above tests, we'll never match on a
		// rate or duration spread.
		if quote == NullQuote {
			n++
			return true
		}

		// As the quote isn't null, we should have a match at this
		// point.
		if !match {
			t.Logf("got match with null quote")
			return false
		}

		// At this point, we know we have a match, but the properties
		// below will only hold if the number of units of the ask was
		// equal to the bid, so we'll assert that here.
		if testPair.Ask.Units != testPair.Bid.Units {
			n++
			return true
		}

		// As the number of supply units for the bid was greater than
		// the ask, we should receive a partial ask fulfill.
		if quote.Type != TotalFulfill {

			t.Fatalf("expected partial ask fulfill, instead "+
				"got %v", quote.Type)
			return false
		}

		// As the supply is equal, amount of unmatched units should be
		// zero.
		if quote.UnitsUnmatched != 0 {

			t.Fatalf("expected %v unit unmatched, instead got %v",
				0, quote.UnitsUnmatched)
			return false
		}

		// As we have a full match, the number of matched unit should
		// be equal to the bid and the ask.
		if quote.UnitsMatched != testPair.Bid.Units &&
			quote.UnitsMatched != testPair.Ask.Units {

			t.Fatalf("expected %v unit matched, instead got %v",
				testPair.Bid.Units, quote.UnitsMatched)
			return false
		}

		// If we get to this point, then our scenario has been upheld:
		// a spread should never trigger a match.
		y++
		return true
	}

	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			// For this scenario, we'll force _all_ the generated
			// orders to have the same number of units to ensure
			// each iteration of the test hits our "positive"
			// scenario.
			numUnits := orderT.SupplyUnit(r.Int31())

			randOrderPair := orderPair{
				Bid: genRandBid(r, acctDB, staticUnitGen(numUnits)),
				Ask: genRandAsk(r, acctDB, staticUnitGen(numUnits)),
			}

			v[0] = reflect.ValueOf(randOrderPair)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("partial bid match scenario failed: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestMatchBatchInputOrderIgnored...
//
// TODO(roasbeef): don't need this?
func TestMatchBatchInputOrderIgnored(t *testing.T) {
	t.Parallel()
}

// orderSet is the set of orders that we'll pass in to the MatchBatch method
// under test. We'll programmatically generate a set of test data for each test
// based on the scenario under test.
type orderSet struct {
	Bids []*order.Bid
	Asks []*order.Ask
}

// TestMatchBatchBidFillsManyAsk tests that if we have a single bid that can
// satisfy all the asks, then we end up with a match set that pairs that single
// bid with all the asks, possibly leaving a remainder.
func TestMatchBatchBidFillsManyAsk(t *testing.T) { // nolint:dupl
	t.Parallel()

	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	n, y := 0, 0
	scenario := func(orders orderSet) bool {
		// First, we'll attempt to match the batch. As we generate a
		// set of input data to ensure a match, if if have an error,
		// then we know something went wrong.
		//
		// TODO(roasbeef): modify below to sometimes allow non full
		// matches?
		matchSet, err := matchMaker.MatchBatch(
			orders.Bids, orders.Asks,
		)
		if err != nil {
			t.Logf("unable to match batch: %v", err)
			return false
		}

		// The set of matched orders should be the same size as the
		// number of asks.
		if len(matchSet.MatchedOrders) != len(orders.Asks) {
			t.Logf("wrong number of matched orders: "+
				"expected %v, got %v",
				len(orders.Asks), len(matchSet.MatchedOrders))
			return false
		}

		// As the entire set of orders should be matched, there should
		// be no unmatched bids and asks.
		switch {
		case len(matchSet.UnmatchedAsks) != 0:
			t.Logf("expected zero unmatched asks, instead "+
				"got: %v", len(matchSet.UnmatchedAsks))
			return false

		case len(matchSet.UnmatchedBids) != 0:
			t.Logf("expected zero unmatched bids, instead "+
				"got: %v", len(matchSet.UnmatchedBids))
			return false
		}

		// The bid order we created should have an entry in the
		// orderRemainders map. The entry should be zero as the bid has
		// been fully matched, but only if we have more than one ask.
		bidNonce := orders.Bids[0].Nonce()
		bidRemainder, ok := matchMaker.orderRemainders[bidNonce]
		if !ok && len(orders.Asks) > 1 {
			t.Logf("remainder not found for bid order")
			return false
		}

		if bidRemainder != 0 {
			t.Logf("expected no bid remainder, instead have: %v, %v",
				bidRemainder,
				spew.Sdump(matchMaker.orderRemainders))
			return false
		}

		// Finally the bid should have zero units UnitsUnfulfilled.
		leftOverUnits := orders.Bids[0].UnitsUnfulfilled
		if leftOverUnits != 0 {
			t.Logf("expected zero left over units, instead "+
				"have: %v", leftOverUnits)
			return false
		}

		// The match type for each matched order should be
		// PartialBidFulfill.
		numMatchedOrders := len(matchSet.MatchedOrders)
		for i, match := range matchSet.MatchedOrders {
			matchType := match.Details.Quote.Type
			switch {
			// If this is the final matched order, then we expect a
			// full fulfill as the Bid should be able to consume
			// _all_ the asks.
			case i == numMatchedOrders-1 && matchType != TotalFulfill:
				t.Logf("wrong match type: expected %v, got %v",
					TotalFulfill, matchType)
				return false

			// Otherwise, we expect a partial bid fill as each ask
			// will be fully consumed by the same bid.
			case i != numMatchedOrders-1 && matchType != PartialBidFulfill:
				t.Logf("wrong match type: expected %v, got %v",
					PartialBidFulfill, matchType)
				return false
			}
		}

		y++
		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			var randOrderSet orderSet

			// totalSupply is the total supply that we aim to fill
			// in this test. The bid inserted will have this exact
			// supply, while the asks will have a random supply
			// which all sum up to this value.
			totalSupply := uint16(r.Int31())
			bidDuration := uint32(r.Int31())
			bidRate := uint32(r.Int31())
			randOrderSet.Bids = make([]*order.Bid, 1)
			randOrderSet.Bids[0] = genRandBid(
				r,
				acctDB,
				staticUnitGen(orderT.SupplyUnit(totalSupply)),
				staticDurationGen(bidDuration),
				staticRateGen(bidRate),
			)

			// We'll generate a random number of orders for the
			// number of asks that we'll attempt to fill against a
			// single bid.
			numAsks := uint16(r.Int31())
			randOrderSet.Asks = make([]*order.Ask, 0, numAsks)
			remainingSupply := totalSupply
			for i := 0; i < int(numAsks)-1; i++ {
				if remainingSupply <= 0 {
					break
				}

				// Next we'll generate the amount of supply this
				// ask will offer. We add one as the range is
				// exclusive, and we also want to test a case where
				// one ask may satisfy the entire bid.
				askSupply := r.Int31n(int32(remainingSupply)) + 1
				if askSupply > int32(remainingSupply) {
					askSupply = int32(remainingSupply)
				}

				// Reduce the remaining supply to ensure that the
				// supply for each ask sums up to totalSupply.
				remainingSupply -= uint16(askSupply)

				// When we generate the ask order, we make sure
				// to bound the bid duration and rate to ensure
				// everything matches with the bid above.
				randOrderSet.Asks = append(randOrderSet.Asks, genRandAsk(
					r,
					acctDB,
					staticUnitGen(orderT.SupplyUnit(askSupply)),
					minDurationGenBound(bidDuration, r),
					maxRateGenBound(bidRate, r),
				))
			}

			v[0] = reflect.ValueOf(randOrderSet)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("partial bid match scenario failed: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestMatchBatchAskFillsManyBid tests that if we have a single ask that can
// satisfy all the bids, then we end up with a match set that pairs the single
// ask with all the bids, possibly leaving a remainder.
func TestMatchBatchAskFillsManyBid(t *testing.T) { // nolint:dupl
	t.Parallel()

	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	n, y := 0, 0
	scenario := func(orders orderSet) bool {
		// First, we'll attempt to match the batch. As we generate a
		// set of input data to ensure a match, if if have an error,
		// then we know something went wrong.
		matchSet, err := matchMaker.MatchBatch(
			orders.Bids, orders.Asks,
		)
		if err != nil {
			t.Logf("unable to match batch: %v", err)
			return false
		}

		// The set of matched orders should be the same size as the
		// number of bids.
		if len(matchSet.MatchedOrders) != len(orders.Bids) {
			t.Logf("wrong number of matched orders: "+
				"expected %v, got %v",
				len(orders.Bids), len(matchSet.MatchedOrders))
			return false
		}

		// As the entire set of orders should be matched, there should
		// be no unmatched bids and asks.
		switch {
		case len(matchSet.UnmatchedAsks) != 0:
			t.Logf("expected zero unmatched asks, instead "+
				"got: %v", len(matchSet.UnmatchedAsks))
			return false

		case len(matchSet.UnmatchedBids) != 0:
			t.Logf("expected zero unmatched bids, instead "+
				"got: %v", len(matchSet.UnmatchedBids))
			return false
		}

		// The ask order we created should have an entry in the
		// orderRemainders map. The entry should be zero as the ask has
		// been fully matched, but only if we have more than one bid.
		askNonce := orders.Asks[0].Nonce()
		askRemainder, ok := matchMaker.orderRemainders[askNonce]
		if !ok && len(orders.Bids) > 1 {
			t.Logf("remainder not found for bid order")
			return false
		}

		if askRemainder != 0 {
			t.Logf("expected no ask remainder, instead have: %v, %v",
				askRemainder,
				spew.Sdump(matchMaker.orderRemainders))
			return false
		}

		// Finally the ask should have zero units UnitsUnfulfilled.
		leftOverUnits := orders.Asks[0].UnitsUnfulfilled
		if leftOverUnits != 0 {
			t.Logf("expected zero left over units, instead "+
				"have: %v", leftOverUnits)
			return false
		}

		// The match type for each matched order should be
		// PartialAskFulfill, other than the final order which should
		// _fully_ match.
		numMatchedOrders := len(matchSet.MatchedOrders)
		for i, match := range matchSet.MatchedOrders {
			matchType := match.Details.Quote.Type
			switch {
			// If this is the final matched order, then we expect a
			// full fulfill as the Bid should be able to consume
			// _all_ the asks.
			case i == numMatchedOrders-1 && matchType != TotalFulfill:
				t.Logf("wrong match type: expected %v, got %v",
					TotalFulfill, matchType)
				return false

			// Otherwise, we expect a partial bid fill as each ask
			// will be fully consumed by the same bid.
			case i != numMatchedOrders-1 && matchType != PartialAskFulfill:
				t.Logf("wrong match type: expected %v, got %v",
					PartialAskFulfill, matchType)
				return false
			}
		}

		y++
		return true
	}

	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			var randOrderSet orderSet

			// totalSupply is the total supply that we aim to fill
			// in this test. The ask inserted will have this exact
			// supply, while the bids will have a random supply
			// which all sum up to this value.
			totalSupply := uint16(r.Int31())
			askDuration := uint32(r.Int31())
			askRate := uint32(r.Int31())
			randOrderSet.Asks = make([]*order.Ask, 1)
			randOrderSet.Asks[0] = genRandAsk(
				r,
				acctDB,
				staticUnitGen(orderT.SupplyUnit(totalSupply)),
				staticDurationGen(askDuration),
				staticRateGen(askRate),
			)

			// We'll generate a random number of orders for the
			// number of asks that we'll attempt to fill against a
			// single ask.
			numBids := uint16(r.Int31())
			randOrderSet.Bids = make([]*order.Bid, 0, numBids)
			remainingSupply := totalSupply
			for i := 0; i < int(numBids)-1; i++ {
				if remainingSupply <= 0 {
					break
				}

				// Next we'll generate the amount of supply
				// this bid will request. We add one as the
				// range is exclusive, and we also want to test
				// a case where one ask may satisfy the entire
				// bid.
				bidSupply := r.Int31n(int32(remainingSupply)) + 1
				if bidSupply > int32(remainingSupply) {
					bidSupply = int32(remainingSupply)
				}

				// Reduce the remaining supply to ensure that
				// the supply for each bid sums up to
				// totalSupply.
				remainingSupply -= uint16(bidSupply)

				// When we generate the ask order, we make sure
				// to bound the bid duration and rate to ensure
				// everything matches with the ask above.
				randOrderSet.Bids = append(
					randOrderSet.Bids,
					genRandBid(
						r,
						acctDB,
						staticUnitGen(orderT.SupplyUnit(bidSupply)),
						maxDurationGenBound(askDuration, r),
						minRateGenBound(askRate, r),
					))
			}

			v[0] = reflect.ValueOf(randOrderSet)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("partial ask match scenario failed: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestMatchBatchNoMatch tests that when a set of orders is passed in with no
// resulting match, then there is no match set, and the set of unmatched orders
// are populated.
func TestMatchBatchNoMatch(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	n, y := 0, 0
	scenario := func(orders orderSet) bool {
		// First, we'll attempt to match our randomly generated batch,
		// no error should be triggered otherwise, we'll fail.
		matchSet, err := matchMaker.MatchBatch(
			orders.Bids, orders.Asks,
		)
		switch {
		case err == ErrNoMarketPossible:
			n++
			return true

		case err != nil:
			t.Logf("unable to match batch: %v", err)
			return false
		}

		// If we have a set of matched orders, then we'll exit as we
		// don't care about this case.
		if len(matchSet.MatchedOrders) > 0 {
			n++
			return true
		}

		// Otherwise, we at this point there was no match. This
		// shouldn't happen as the auctioneer should be able to detect
		// this before the batch, but we'll assert proper handling
		// anyway.
		//
		// First, the number of unmatched bids and asks should be
		// identical.
		numBids := len(orders.Bids)
		numAsks := len(orders.Asks)
		switch {
		case len(matchSet.UnmatchedBids) != numBids:
			t.Logf("expected %v unmatched bids, got %v", numBids,
				len(matchSet.UnmatchedBids))
			return false

		case len(matchSet.UnmatchedAsks) != numAsks:
			t.Logf("expected %v unmatched asks, got %v", numAsks,
				len(matchSet.UnmatchedAsks))
			return false
		}

		y++
		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			// To ensure that we'll create a set with no matches,
			// we'll give each bid and ask values that will create
			// a spread.
			bidRate := uint32(100)
			askRate := uint32(1000)

			// When generating the random set below, we'll cap the
			// number of orders on both sides to ensure the test
			// completes in a timely manner.
			numMaxOrders := 1000
			var randOrderSet orderSet

			numBids := uint16(r.Int31n(int32(numMaxOrders)))
			randOrderSet.Bids = make([]*order.Bid, numBids)
			for i := 0; i < int(numBids); i++ {
				randOrderSet.Bids[i] = genRandBid(
					r, acctDB, staticRateGen(bidRate),
				)
			}

			numAsks := uint16(r.Int31n(int32(numMaxOrders)))
			randOrderSet.Asks = make([]*order.Ask, numAsks)
			for i := 0; i < int(numAsks); i++ {
				randOrderSet.Asks[i] = genRandAsk(
					r, acctDB, staticRateGen(askRate),
				)
			}

			v[0] = reflect.ValueOf(randOrderSet)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("partial ask match scenario failed: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

func genRandOrderSet(r *rand.Rand, acctDB *acctFetcher,
	numMaxOrders uint32, genOptions ...orderGenOption) orderSet {

	var randOrderSet orderSet

	numBids := uint16(r.Int31n(int32(numMaxOrders))) + 1
	randOrderSet.Bids = make([]*order.Bid, numBids)
	for i := 0; i < int(numBids); i++ {
		randOrderSet.Bids[i] = genRandBid(r, acctDB, genOptions...)
	}

	numAsks := uint16(r.Int31n(int32(numMaxOrders))) + 1
	randOrderSet.Asks = make([]*order.Ask, numAsks)
	for i := 0; i < int(numAsks); i++ {
		randOrderSet.Asks[i] = genRandAsk(r, acctDB, genOptions...)
	}

	return randOrderSet
}

// TestMatchMakingBatch tests that given a random set of orders, if a match is
// possible, then the output of the match set is well formed.
func TestMatchMakingBatch(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	n, y := 0, 0
	scenario := func(orders orderSet) bool {
		// First, we'll attempt to match our randomly generated batch,
		// no error should be triggered otherwise, we'll fail.
		matchSet, err := matchMaker.MatchBatch(
			orders.Bids, orders.Asks,
		)
		switch {
		case err == ErrNoMarketPossible:
			n++
			return true

		case err != nil:
			t.Logf("unable to match batch: %v", err)
			return false
		}

		// If there's no match, then we exit here, as the
		// TestMatchBatchNoMatch scenario covers this case.
		if len(matchSet.MatchedOrders) == 0 {
			n++
			return true
		}

		// If there's a match then we'll now execute a series of
		// assertions to ensure the matches are well formed.
		numTotalOrders := len(orders.Bids) + len(orders.Asks)
		matchSetSize := len(matchSet.UnmatchedBids) + len(matchSet.UnmatchedAsks)
		seenOrders := make(map[orderT.Nonce]struct{})
		for _, orderPair := range matchSet.MatchedOrders {
			ask := orderPair.Details.Ask
			bid := orderPair.Details.Bid

			if _, ok := seenOrders[ask.Nonce()]; !ok {
				seenOrders[ask.Nonce()] = struct{}{}
				matchSetSize++
			}
			if _, ok := seenOrders[bid.Nonce()]; !ok {
				seenOrders[bid.Nonce()] = struct{}{}
				matchSetSize++
			}
		}

		// The sum of the matched and unmatched orders should equal the
		// number of total orders.
		if numTotalOrders != matchSetSize {
			t.Logf("expected %v total orders instead got %v",
				numTotalOrders, matchSetSize)
			return false
		}

		// For each matched order, a match an independent match should
		// be possible. Additionally, we'll tally all the partial
		// matches to ensure that the UnitsUnfulfilled field for each
		// is tracked properly.
		fullyClearedOrders := make(map[orderT.Nonce]struct{})
		orderMap := make(map[orderT.Nonce]order.ServerOrder)
		for _, matchedOrder := range matchSet.MatchedOrders {
			ask := matchedOrder.Details.Ask
			bid := matchedOrder.Details.Bid

			if _, ok := matchMaker.MatchPossible(bid, ask); !ok {
				t.Logf("incompatible orders returned in match "+
					"set: %v vs %v", spew.Sdump(bid), spew.Sdump(ask))
				return false
			}

			if matchedOrder.Details.Quote.Type == TotalFulfill {
				fullyClearedOrders[ask.Nonce()] = struct{}{}
				fullyClearedOrders[bid.Nonce()] = struct{}{}
			}

			orderMap[ask.Nonce()] = ask
			orderMap[bid.Nonce()] = bid
		}

		// Now for all the orders that aren't fully cleared, the
		// orderRemainders maps should have the same number of unfilled
		// units as the order pointer itself.
		for nonce, matchedOrder := range orderMap {
			if _, ok := fullyClearedOrders[nonce]; ok {
				continue
			}

			mapRemainder := matchMaker.orderRemainders[nonce]

			var orderRemainder orderT.SupplyUnit
			switch o := matchedOrder.(type) {
			case *order.Ask:
				orderRemainder = o.UnitsUnfulfilled

			case *order.Bid:
				orderRemainder = o.UnitsUnfulfilled
			}

			if orderRemainder != mapRemainder {
				t.Logf("state out of sync (order=%x): map_remainder=%v, "+
					"order_remainder=%v", nonce[:], mapRemainder,
					orderRemainder)
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
			randOrderSet := genRandOrderSet(r, acctDB, 1000)

			v[0] = reflect.ValueOf(randOrderSet)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("arbitrary match batch scenario failed: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestMatchingNoSelfOrderMatch tests that if a trader submits an Ask and a
// Bid, then we don't attempt to match those orders as they're with the same
// trader.
func TestMatchingNoSelfOrderMatch(t *testing.T) {
	t.Parallel()

	r := rand.New(rand.NewSource(time.Now().Unix()))
	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	traderAcct := genRandAccount(r)

	bid := genRandBid(
		r, acctDB, staticAccountKeyGen(traderAcct.TraderKeyRaw),
	)
	ask := genRandAsk(
		r, acctDB, staticAccountKeyGen(traderAcct.TraderKeyRaw),
	)

	_, match := matchMaker.MatchPossible(bid, ask)
	if match {
		t.Fatalf("orders by the same trader shouldn't match!")
	}
}

// TestMatchingAccountNotReady ensures that accounts within a batch matching
// attempt are ready to participate in the batch.
func TestMatchingAccountNotReady(t *testing.T) {
	t.Parallel()

	r := rand.New(rand.NewSource(time.Now().Unix()))
	acctDB := newAcctFetcher()
	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)

	ask := genRandAsk(r, acctDB)
	asks := []*order.Ask{ask}

	bid := genRandBid(
		r,
		acctDB,
		staticUnitGen(ask.Units),
		staticDurationGen(ask.MaxDuration()),
		staticRateGen(ask.FixedRate),
		staticAccountState(account.StatePendingOpen),
	)
	bids := []*order.Bid{bid}

	matchSet, err := matchMaker.MatchBatch(bids, asks)
	require.NoError(t, err)

	require.Empty(t, matchSet.MatchedOrders)
	require.Equal(t, matchSet.UnmatchedAsks, asks)
	require.Equal(t, matchSet.UnmatchedBids, bids)
}

// TestMatchingLowestAskNoMatchMultiPass tests that if the lowest ask doesn't
// mach with the first bid, but does with the second bid, then it's single
// included in the batch. This test was added as a regression tests, hence the
// hand picked test data.
func TestMatchingLowestAskNoMatchMultiPass(t *testing.T) {
	t.Parallel()

	acctDB := newAcctFetcher()

	// We'll create a single ask, fixing the parameters that are critical
	// for matching.
	askDuration := uint32(1000)
	targetRate := uint32(1000)
	askSupply := uint16(100)
	r := rand.New(rand.NewSource(time.Now().Unix()))
	asks := []*order.Ask{
		genRandAsk(
			r,
			acctDB,
			staticUnitGen(orderT.SupplyUnit(askSupply)),
			staticDurationGen(askDuration),
			staticRateGen(targetRate),
		),
	}

	// We'll now create two bids, one with a high target rate but an
	// incompatible duration, and another with a lower rate, but params to
	// ensure a match with the ask above.
	bids := []*order.Bid{
		// This bid will be close to matching, but it wants a longer
		// duration, so it can be matched with the ask above.
		genRandBid(
			r,
			acctDB,
			staticUnitGen(orderT.SupplyUnit(askSupply)),
			staticDurationGen(askDuration*2),
			staticRateGen(targetRate*2),
		),

		// This bid will match exactly with the ask above.
		genRandBid(
			r,
			acctDB,
			staticUnitGen(orderT.SupplyUnit(askSupply)),
			staticDurationGen(askDuration),
			staticRateGen(targetRate),
		),
	}

	matchMaker := NewMultiUnitMatchMaker(acctDB.fetchAcct)
	matchSet, err := matchMaker.MatchBatch(bids, asks)
	if err != nil {
		t.Fatalf("unable to match orders: %v", err)
	}

	// We should have exactly one match, zero unmatched asks, and a single
	// unmatched bid.
	switch {
	case len(matchSet.MatchedOrders) != 1:
		t.Fatalf("expected a single matched order, instead "+
			"got %v", len(matchSet.MatchedOrders))

	case len(matchSet.UnmatchedBids) != 1:
		t.Fatalf("expected a single unmatched bid, instead "+
			"got %v", len(matchSet.UnmatchedBids))

	case len(matchSet.UnmatchedAsks) != 0:
		t.Fatalf("expected a single unmatched ask, instead "+
			"got %v", len(matchSet.UnmatchedAsks))
	}

	// The sole match should be a full match, and should report our target
	// rate above.
	orderPair := matchSet.MatchedOrders[0]
	quoteDetails := orderPair.Details.Quote
	if quoteDetails.Type != TotalFulfill {
		t.Fatalf("expected total fill, instead got: %v",
			quoteDetails.Type)
	}
	if uint32(quoteDetails.MatchingRate) != targetRate {
		t.Fatalf("wrong rate: expected %v got %v",
			targetRate, quoteDetails.MatchingRate)
	}
}

// TODO(roasbeef): totally random one, test that the output is well formed (num
// orders conserved, fields properly updated, etc)

// TODO(roasbeef): test that you can submit a bid and an ask and have that work
// properly, may be elsewhere
