package batchtx

import (
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

var (
	emptyAcct [33]byte
)

const (
	testFeeRate = chainfee.SatPerKWeight(300)
)

type matchedOrderGenCfg struct {
	asker  [33]byte
	bidder [33]byte
}

type matchedOrderGenOption func(*matchedOrderGenCfg)

func (m *matchedOrderGenCfg) bidderKey(r *rand.Rand) [33]byte { // nolint:interfacer
	if m.bidder != emptyAcct {
		return m.bidder
	}

	var b [33]byte
	_, _ = r.Read(b[:])

	return b
}

func (m *matchedOrderGenCfg) askerKey(r *rand.Rand) [33]byte { // nolint:interfacer
	if m.asker != emptyAcct {
		return m.asker
	}

	var a [33]byte
	_, _ = r.Read(a[:])

	return a
}

func staticAskerGen(asker [33]byte) matchedOrderGenOption {
	return func(opt *matchedOrderGenCfg) {
		opt.asker = asker
	}
}

func staticBidderGen(bidder [33]byte) matchedOrderGenOption {
	return func(opt *matchedOrderGenCfg) {
		opt.bidder = bidder
	}
}

func genRandTrader(r *rand.Rand, bidder bool, genOptions ...matchedOrderGenOption) matching.Trader {
	genCfg := matchedOrderGenCfg{}
	for _, optionModifier := range genOptions {
		optionModifier(&genCfg)
	}

	t := matching.Trader{
		AccountExpiry:  uint32(r.Int31()),
		AccountBalance: btcutil.Amount(r.Int31()),
	}
	if bidder {
		t.AccountKey = genCfg.bidderKey(r)
	} else {
		t.AccountKey = genCfg.askerKey(r)
	}

	_, _ = rand.Read(t.NextBatchKey[:])         // nolint:gosec
	_, _ = rand.Read(t.VenueSecret[:])          // nolint:gosec
	_, _ = rand.Read(t.AccountOutPoint.Hash[:]) // nolint:gosec

	return t
}

func genRandMatchedOrder(r *rand.Rand, genOptions ...matchedOrderGenOption) matching.MatchedOrder {

	return matching.MatchedOrder{
		Asker:  genRandTrader(r, true, genOptions...),
		Bidder: genRandTrader(r, false, genOptions...),
		Details: matching.OrderPair{
			Quote: matching.PriceQuote{
				TotalSatsCleared: btcutil.Amount(r.Int31()),
			},
		},
	}
}

func genRandMatchedOrders(r *rand.Rand, numTraders, numOrders int32) []matching.MatchedOrder {
	var randOrders []matching.MatchedOrder

	for i := 0; i < int(numTraders); i++ {
		bidder := genRandTrader(r, true)
		asker := genRandTrader(r, false)

		for i := 0; i < int(numOrders); i++ {
			randOrder := genRandMatchedOrder(
				r,
				staticAskerGen(asker.AccountKey),
				staticBidderGen(bidder.AccountKey),
			)

			randOrders = append(randOrders, randOrder)
		}
	}

	return randOrders
}

// TestChainFeeEstimatorFeeOrderScaling asserts the property that if one trader
// has more matched orders than another in a batch, then they always have a
// greater fee contribution on their end.
func TestChainFeeEstimatorFeeOrderScaling(t *testing.T) {
	t.Parallel()

	scenario := func(orders []matching.MatchedOrder) bool {
		feeEstimator := newChainFeeEstimator(orders, testFeeRate, &BatchIO{})

		// For each pair of traders in this randomly generated batch,
		// assert that if one trader has a greater number of channels
		// in this batch than the other, than the one with more orders
		// has a higher fee contribution.
		for traderA, traderAChanCount := range feeEstimator.traderChanCount {
			for traderB, traderBChanCount := range feeEstimator.traderChanCount {
				if traderA == traderB {
					continue
				}

				traderAFees := feeEstimator.EstimateTraderFee(
					traderA,
				)
				traderBfees := feeEstimator.EstimateTraderFee(
					traderB,
				)
				switch {
				case traderAChanCount > traderBChanCount &&
					traderAFees <= traderBfees:
					t.Logf("traderA(num_chans=%v) has "+
						"less fees than "+
						"traderB(num_chans=%v): %v vs %v",
						traderAChanCount,
						traderBChanCount, traderAFees,
						traderBfees)

					return false

				case traderAChanCount < traderBChanCount &&
					traderAFees >= traderBfees:

					t.Logf("traderA(num_chans=%v) has "+
						"greater fees than "+
						"traderB(num_chans=%v): %v vs %v",
						traderAChanCount,
						traderBChanCount, traderAFees,
						traderBfees)

					return false

				case traderAChanCount == traderBChanCount &&
					traderAFees != traderBfees:

					t.Logf("traderA(num_chans=%v) has "+
						"diff fees than traderB(num_chans=%v): %v vs %v",
						traderAChanCount,
						traderBChanCount, traderAFees,
						traderBfees)

					return false
				}
			}

		}

		// Finally, it should be the case that a trader that isn't in
		// the batch doesn't report any fee contribution.
		if feeEstimator.EstimateTraderFee(emptyAcct) != 0 {
			t.Logf("empty account should have no fees due")
			return false
		}

		return true
	}

	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			randOrders := genRandMatchedOrders(r, 100, 50)

			v[0] = reflect.ValueOf(randOrders)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("fee scaling property violated: %v", err)
	}
}

type matchedOrderSet struct {
	orderSetA []matching.MatchedOrder
	orderSetB []matching.MatchedOrder

	feeRateA chainfee.SatPerKWeight
	feeRateB chainfee.SatPerKWeight
}

// TestChainFeeEstimatorEstimateBatchWeight asserts the property that given two
// sets of orders, and their corresponding chainFeeEstimators, the larger order
// set will have a higher estimated batch weight.
func TestChainFeeEstimatorEstimateBatchWeight(t *testing.T) {
	t.Parallel()

	n, y := 0, 0
	scenario := func(set matchedOrderSet) bool {
		setA, setB := set.orderSetA, set.orderSetB

		estA := newChainFeeEstimator(setA, testFeeRate, &BatchIO{})
		feeSetA, err := estA.EstimateBatchWeight(len(estA.traderChanCount))
		if err != nil {
			t.Logf("unable to estimate batch weight: %v", err)
			return false
		}

		estB := newChainFeeEstimator(setB, testFeeRate, &BatchIO{})
		feeSetB, err := estB.EstimateBatchWeight(len(estB.traderChanCount))
		if err != nil {
			t.Logf("unable to estimate batch weight: %v", err)
			return false
		}

		aLarger := (len(estA.traderChanCount) > len(estB.traderChanCount) &&
			len(estA.orders) > len(estB.orders))

		if !aLarger {
			n++
			return true
		}

		// The weight of a batch should be a monotonically increasing
		// function of the total size of a given batch.
		if feeSetA < feeSetB {
			t.Logf("set A fees should be greater: %v vs %v",
				feeSetA, feeSetB)
			return false
		}

		y++
		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			setA := genRandMatchedOrders(
				r,
				rand.Int31n(100),
				rand.Int31n(50),
			)
			setB := genRandMatchedOrders(
				r,
				rand.Int31n(100),
				rand.Int31n(50),
			)

			v[0] = reflect.ValueOf(matchedOrderSet{
				orderSetA: setA,
				orderSetB: setB,
			})
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("fee scaling property violated: %v", err)
	}

	t.Logf("Total number of scenarios run: %v (%v positive, %v negative)", n+y, y, n)
}

// TestChainFeeEstimatorFeeRateScaling asserts the property that given two
// equally sized order batches, if one has a higher fee rate than the other,
// then both the trader fee and the higher fee surplus will also be higher.
func TestChainFeeEstimatorFeeRateScaling(t *testing.T) {
	t.Parallel()

	scenario := func(set matchedOrderSet) bool {
		setA, setB := set.orderSetA, set.orderSetB

		estimateFee := func(
			orderSet []matching.MatchedOrder) (btcutil.Amount, error) {

			estimator := newChainFeeEstimator(orderSet, testFeeRate, &BatchIO{})

			// Get estimated fees paid by the traders.
			var traders btcutil.Amount
			for trader := range estimator.traderChanCount {
				traders += estimator.EstimateTraderFee(trader)
			}

			// Given what the traders paid, return auctioneer fee.
			return estimator.AuctioneerFee(
				traders, len(estimator.traderChanCount),
			)
		}

		feeSetA, err := estimateFee(setA)
		if err != nil {
			t.Logf("unable to estimate fee: %v", err)
			return false
		}

		feeSetB, err := estimateFee(setB)
		if err != nil {
			t.Logf("unable to estimate fee: %v", err)
			return false
		}

		switch {
		case set.feeRateA > set.feeRateB && feeSetA < feeSetB:
			t.Logf("set A fees should be greater: %v vs %v",
				feeSetA, feeSetB)
			return false

		case set.feeRateA < set.feeRateB && feeSetA > feeSetB:
			t.Logf("set A fees should be less: %v vs %v",
				feeSetA, feeSetB)
			return false

		case set.feeRateA == set.feeRateB && feeSetA != feeSetB:
			t.Logf("set fees should be equal: %v vs %v",
				feeSetA, feeSetB)
			return false
		}

		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			totalSize := int32(uint8(r.Int31()))
			setA := genRandMatchedOrders(r, totalSize, totalSize)
			setB := genRandMatchedOrders(r, totalSize, totalSize)

			v[0] = reflect.ValueOf(matchedOrderSet{
				orderSetA: setA,
				orderSetB: setB,
				feeRateA:  chainfee.SatPerKWeight(uint16(r.Int31())),
				feeRateB:  chainfee.SatPerKWeight(uint16(r.Int31())),
			})
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("fee scaling property violated: %v", err)
	}
}

type dustAccScenario struct {
	orderSet    []matching.MatchedOrder
	endingAccsA int32
	endingAccsB int32
}

// TestChainFeeEstimatorDustAccounts asserts the property that the lower number
// of trader accounts that are recreated on the batch tx, the smaller will the
// total batch tx weight be.
func TestChainFeeEstimatorDustAccounts(t *testing.T) {
	t.Parallel()

	n := 0
	scenario := func(d dustAccScenario) bool {
		set := d.orderSet

		estimator := newChainFeeEstimator(set, testFeeRate, &BatchIO{})

		weightA, err := estimator.EstimateBatchWeight(int(d.endingAccsA))
		if err != nil {
			t.Logf("unable to estimate batch weight: %v", err)
			return false
		}

		weightB, err := estimator.EstimateBatchWeight(int(d.endingAccsB))
		if err != nil {
			t.Logf("unable to estimate batch weight: %v", err)
			return false
		}

		switch {
		case d.endingAccsA < d.endingAccsB && weightA >= weightB:
			t.Logf("tx A should be smaller than B")
			return false

		case d.endingAccsA > d.endingAccsB && weightA <= weightB:
			t.Logf("tx A should be larger than B")
			return false

		case d.endingAccsA == d.endingAccsB && weightA != weightB:
			t.Logf("tx A should be the equal to B")
			return false
		}

		n++
		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {
			set := genRandMatchedOrders(
				r,
				rand.Int31n(100),
				rand.Int31n(50),
			)

			testCase := dustAccScenario{
				orderSet:    set,
				endingAccsA: rand.Int31n(50),
				endingAccsB: rand.Int31n(50),
			}

			v[0] = reflect.ValueOf(testCase)
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("batch weight property violated: %v", err)
	}

	t.Logf("Total number of scenarios run: %v", n)
}

type singleSet struct {
	orderSet []matching.MatchedOrder
	feeRate  chainfee.SatPerKWeight
}

// TestChainFeeEstimatorMeetFeeRate checks that the fees calculated for the
// batch transaction (trader + auctioneer fees) makes the tx meet our target
// fee rate.
func TestChainFeeEstimatorMeetFeeRate(t *testing.T) {
	t.Parallel()

	scenario := func(singleSet singleSet) bool {
		feeRate := singleSet.feeRate
		orderSet := singleSet.orderSet

		estimator := newChainFeeEstimator(orderSet, feeRate, &BatchIO{})

		// Get estimated fees paid by the traders.
		var traders btcutil.Amount
		var numOuts int
		for trader, cnt := range estimator.traderChanCount {
			traders += estimator.EstimateTraderFee(trader)
			numOuts += int(cnt)
		}

		// Given what the traders paid, return total tx fee.
		auctFee, err := estimator.AuctioneerFee(traders, numOuts)
		if err != nil {
			t.Logf("unable to get auctioneer fee: %v", err)
			return false
		}

		totalFee := traders + auctFee

		// Total fee must always be enough to pay for the tx at the
		// wanted fee rate.
		w, err := estimator.EstimateBatchWeight(numOuts)
		if err != nil {
			t.Logf("unable to estimate batch weight: %v", err)
			return false
		}

		finalFeeRate := chainfee.SatPerKWeight(int64(totalFee) * 1000 / w)
		if finalFeeRate < feeRate {
			t.Logf("final fee rate %v not able to satisfy "+
				"targeted test fee rate %v", finalFeeRate,
				feeRate)
			return false
		}

		// For transactions with a weight above 1000 wu, we can check
		// that the final fee rate matches exactly.  The reason this is
		// not always true for smaller transactions is that if we have
		// to add an extra sat to satisfy the target fee rate, it might
		// bump the fee rate with more than 1 sat/kw
		if w >= 1000 {
			if finalFeeRate != feeRate {
				t.Logf("final fee rate %v not equal to test "+
					"fee rate %v", finalFeeRate, feeRate)
				return false
			}
		}

		return true
	}
	quickCfg := quick.Config{
		Values: func(v []reflect.Value, r *rand.Rand) {

			totalSize := int32(uint8(r.Int31()))
			set := genRandMatchedOrders(r, totalSize, totalSize)

			v[0] = reflect.ValueOf(singleSet{
				orderSet: set,
				feeRate:  chainfee.SatPerKWeight(uint16(r.Int31())),
			})
		},
	}
	if err := quick.Check(scenario, &quickCfg); err != nil {
		t.Fatalf("batch tx fee rate property violated: %v", err)
	}
}
