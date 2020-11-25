package matching

import (
	"container/list"
	"encoding/hex"
	"fmt"
	"sync"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/order"
)

// BatchID is a 33-byte identifier that uniquely identifies this batch. This ID
// will be used later for account key derivation when constructing the batch
// execution transaction.
type BatchID [33]byte

// String returns the hex encoded batch ID.
func (b BatchID) String() string {
	return hex.EncodeToString(b[:])
}

// UniformPriceCallMarket is a discrete-batch auction that clears all orders in
// a batch according to a selected uniform clearing price. This struct will be
// used by the main auctioneer system to clear batches ever period T, or as
// frequently as is needed.
//
// NOTE: This is an implementation of the BatchAuctioneer interface.
type UniformPriceCallMarket struct {
	// bids is a linked list of all bids.
	bids *list.List

	// bidIndex is an index into the above linked list so we can easily
	// remove bids that are cancelled or matched.
	bidIndex map[orderT.Nonce]*list.Element

	// asks is a linked list of all asks.
	asks *list.List

	// askIndex is an index into the above linked list so we can easily
	// remove asks that are cancelled or matched.
	askIndex map[orderT.Nonce]*list.Element

	// priceClearer is the main instance that the call market will used to
	// arrive at the uniform clearing price.
	priceClearer PriceClearer

	// feeSchedule is the current fee schedule of the auctioneer. This will
	// be used to determine how much to charge traders in venue and
	// execution fees.
	feeSchedule terms.FeeSchedule

	sync.Mutex
}

// NewUniformPriceCallMarket returns a new instance of the
// UniformPriceCallMarket struct given the price clearer and fee schedule for
// this current batch epoch.
func NewUniformPriceCallMarket(priceClearer PriceClearer,
	feeSchedule terms.FeeSchedule) *UniformPriceCallMarket {

	u := &UniformPriceCallMarket{
		priceClearer: priceClearer,
		feeSchedule:  feeSchedule,
	}

	u.Lock()
	defer u.Unlock()

	u.resetOrderState()

	return u

}

// resetOrderState resets the order state to blank.
//
// NOTE: The mutex MUST be held when calling this method.
func (u *UniformPriceCallMarket) resetOrderState() {
	u.bids = list.New()
	u.bidIndex = make(map[orderT.Nonce]*list.Element)

	u.asks = list.New()
	u.askIndex = make(map[orderT.Nonce]*list.Element)
}

// MaybeClear attempts to clear a batch given the fee rate and a list of default
// match predicates to check. Note that it can happen that no match is possible,
// in which case an error will be returned.
//
// The fee rate provided will be used to exclude orders which had their
// max batch fee rate set lower.
//
// NOTE: This method is a part of the BatchAuctioneer interface.
func (u *UniformPriceCallMarket) MaybeClear(acctCacher AccountCacher,
	filterChain []OrderFilter,
	predicateChain []MatchPredicate) (*OrderBatch, error) {

	u.Lock()
	defer u.Unlock()

	// At this point we know we have a set of orders, so we'll create the
	// match maker for usage below.
	matchMaker := NewMultiUnitMatchMaker(acctCacher, predicateChain)

	// At this point, there's may be at least a single order that we can
	// execute, so we'll attempt to match the entire pending batch.
	//
	// First we'll obtain slices pointing to the backing list so the match
	// maker can examine all the entries easily.
	//
	// NOTE: we make value copies of the orders found in the backing lists,
	// such that MatchBatch won't actually mutate the order book contents.
	var (
		bids = make([]*order.Bid, 0, u.bids.Len())
		asks = make([]*order.Ask, 0, u.asks.Len())
	)

	for bid := u.bids.Front(); bid != nil; bid = bid.Next() {
		b := bid.Value.(order.Bid)

		if !SuitsFilterChain(&b, filterChain...) {
			continue
		}

		bids = append(bids, &b)
	}
	for ask := u.asks.Front(); ask != nil; ask = ask.Next() {
		a := ask.Value.(order.Ask)

		if !SuitsFilterChain(&a, filterChain...) {
			continue
		}

		asks = append(asks, &a)
	}

	// With our bids/asks converted to the proper container type, we'll
	// attempt to perform match making for the entire batch to make the
	// market for this next batch.
	matchSet, err := matchMaker.MatchBatch(bids, asks)
	if err != nil {
		// TODO(roasbeef): wrapped errors thru the entire codebase?
		return nil, err
	}

	// If we have no matched orders at all, then we can exit early here, as
	// there's no market to make.
	if len(matchSet.MatchedOrders) == 0 {
		return nil, ErrNoMarketPossible
	}

	// Now that we know all the orders to be executed for this batch (and
	// the stragglers), we'll compute the uniform clearing price with the
	// current price clearer instance.
	clearingPrice, err := u.priceClearer.ExtractClearingPrice(matchSet)
	if err != nil {
		return nil, err
	}

	// To ensure clearing price consistency within the batch, we filter out
	// any anomalies. There should always be at least a single match left,
	// otherwise something is off with how we extract the clearing price.
	matches := filterAnomalies(matchSet.MatchedOrders, clearingPrice)
	if len(matches) == 0 {
		return nil, fmt.Errorf("filtering match set of %d resulted "+
			"in empty set", len(matchSet.MatchedOrders))
	}

	logMatches := func(matches []MatchedOrder) string {
		s := ""
		for _, m := range matches {
			d := m.Details
			s += fmt.Sprintf("ask=%v, bid=%v, units=%v\n",
				d.Ask.Nonce(), d.Bid.Nonce(), d.Quote.UnitsMatched)
		}

		return s
	}

	log.Debugf("Final matches at clearing price %v: \n%v",
		clearingPrice, logMatches(matches))

	// As a final step, we'll compute the diff for each trader's account.
	// With this final piece of information, the caller will be able to
	// easily update all the order/account state in a single atomic
	// transaction.
	feeReport := NewTradingFeeReport(matches, u.feeSchedule, clearingPrice)
	return &OrderBatch{
		Orders:        matches,
		FeeReport:     feeReport,
		ClearingPrice: clearingPrice,
	}, nil
}

// filterAnomalies filters our any order among the matched orders for which the
// clearing price doesn't satisfy its fixed rate. This can happen in cases
// where the orders are "skewed" because of incompatibilities, and we filter
// the batch to ensure all traders are happy. The orders that get filtered out
// will most likely be included in the next batch.
//
// TODO(halseth): we risk skewed matches never to be included if they always
// get kicked to the next batch. Could immediately follow up with a new batch.
func filterAnomalies(matches []MatchedOrder,
	clearingPrice orderT.FixedRatePremium) []MatchedOrder {

	filtered := make([]MatchedOrder, 0, len(matches))
	for _, m := range matches {
		ask := m.Details.Ask
		bid := m.Details.Bid
		if ask.FixedRate > uint32(clearingPrice) {
			log.Debugf("Filtered out ask %v (and matched bid %v) "+
				"with fixed rate %v for clearing price %v",
				ask.Nonce(), bid.Nonce(), ask.FixedRate,
				clearingPrice)
			continue
		}

		if bid.FixedRate < uint32(clearingPrice) {
			log.Debugf("Filtered out bid %v (and matched ask %v) "+
				"with fixed rate %v for clearing price %v",
				bid.Nonce(), ask.Nonce(), bid.FixedRate,
				clearingPrice)
			continue
		}

		filtered = append(filtered, m)
	}

	return filtered
}

// RemoveMatches updates the order book by subtracting the given matches filled
// volume.
//
// NOTE: This method is a part of the BatchAuctioneer interface.
func (u *UniformPriceCallMarket) RemoveMatches(matches ...MatchedOrder) error {
	u.Lock()
	defer u.Unlock()

	// Index the filled volume by nonce.
	filledVolume := make(map[orderT.Nonce]orderT.SupplyUnit)
	for _, match := range matches {
		filled := match.Details.Quote.UnitsMatched

		filledVolume[match.Details.Bid.Nonce()] += filled
		filledVolume[match.Details.Ask.Nonce()] += filled
	}

	// subtractFilled subtracts from the order kit's UnitsFulfilled  the
	// filled volume for the nonce. If true is returned there is still
	// volume left after subtraction, and the order should be added back to
	// the order book.
	subtractFilled := func(o *orderT.Kit) (bool, error) {
		// If this order was part of a match, subtract the matched
		// volume.
		if filled, ok := filledVolume[o.Nonce()]; ok {
			if filled > o.UnitsUnfulfilled {
				return false, fmt.Errorf("match size larger " +
					"than unfulfilled units")
			}

			o.UnitsUnfulfilled -= filled

			// If no more units remain, it should not be added back
			// to the order book.
			if o.UnitsUnfulfilled == 0 {
				return false, nil
			}
		}

		return true, nil
	}

	var (
		bids []*order.Bid
		asks []*order.Ask
	)

	// We now go through the order book and subtract the filled volume for
	// each bid and ask. Note that we make a copy of the orders found in
	// the order book before mutating them, so we'll reset the whole order
	// book with the modified orders below.
	for bid := u.bids.Front(); bid != nil; bid = bid.Next() {
		b := bid.Value.(order.Bid)

		ok, err := subtractFilled(&b.Bid.Kit)
		if err != nil {
			return err
		}

		// Nothing remains, it will be removed from the order book.
		if !ok {
			continue
		}

		bids = append(bids, &b)
	}

	for ask := u.asks.Front(); ask != nil; ask = ask.Next() {
		a := ask.Value.(order.Ask)

		ok, err := subtractFilled(&a.Ask.Kit)
		if err != nil {
			return err
		}

		// Nothing remains, it will be removed from the order book.
		if !ok {
			continue
		}

		asks = append(asks, &a)
	}

	// Finally reset the order book state, and add back the updated bids
	// and asks.
	u.resetOrderState()
	if err := u.considerBids(bids...); err != nil {
		return err
	}
	if err := u.considerAsks(asks...); err != nil {
		return err
	}

	return nil
}

// ConsiderBid adds a set of bids to the staging arena for match making. Only
// once a bid has been considered will it be eligible to be included in an
// OrderBatch.
//
// NOTE: This is a part of the BatchAuctioneer interface.
func (u *UniformPriceCallMarket) ConsiderBids(bids ...*order.Bid) error {
	u.Lock()
	defer u.Unlock()

	return u.considerBids(bids...)
}

// ConsiderBid adds a set of bids to the staging arena for match making. Only
// once a bid has been considered will it be eligible to be included in an
// OrderBatch.
//
// NOTE: The mutex MUST be held when calling this method.
func (u *UniformPriceCallMarket) considerBids(bids ...*order.Bid) error {
	// We'll add all the bids in a single batch, while keeping our pointer
	// to the "best" (highest) bid in the batch up to date.
	for _, bid := range bids {
		bid := bid

		// If the order is already in the set, then we'll skip it to
		// avoid adding it twice.
		orderNonce := bid.Nonce()
		if _, ok := u.bidIndex[orderNonce]; ok {
			continue
		}

		bidElement := u.bids.PushBack(*bid)
		u.bidIndex[orderNonce] = bidElement
	}

	return nil
}

// ForgetBid removes a set of bids from the staging area.
//
// NOTE: This is a part of the BatchAuctioneer interface.
func (u *UniformPriceCallMarket) ForgetBids(bids ...orderT.Nonce) error {
	u.Lock()
	defer u.Unlock()

	for _, bidNonce := range bids {
		bidElement, ok := u.bidIndex[bidNonce]
		if !ok {
			continue
		}

		u.bids.Remove(bidElement)
		delete(u.bidIndex, bidNonce)
	}

	return nil
}

// ConsiderAsk adds a set of asks to the staging arena for match making. Only
// once an ask has been considered will it be eligible to be included in an
// OrderBatch.
//
// NOTE: This is a part of the BatchAuctioneer interface.
func (u *UniformPriceCallMarket) ConsiderAsks(asks ...*order.Ask) error {
	u.Lock()
	defer u.Unlock()

	return u.considerAsks(asks...)
}

// ConsiderAsk adds a set of asks to the staging arena for match making. Only
// once an ask has been considered will it be eligible to be included in an
// OrderBatch.
//
// NOTE: The mutex MUST be held when calling this method.
func (u *UniformPriceCallMarket) considerAsks(asks ...*order.Ask) error {
	// We'll add all the asks in a single batch, while keeping our pointer
	// to the "best" (lowest) ask in the batch up to date.
	for _, ask := range asks {
		ask := ask

		// If the order is already in the set, then we'll skip it to
		// avoid adding it twice.
		orderNonce := ask.Nonce()
		if _, ok := u.askIndex[orderNonce]; ok {
			continue
		}

		askElement := u.asks.PushBack(*ask)
		u.askIndex[orderNonce] = askElement
	}

	return nil
}

// ForgetAsk removes a set of asks from the staging area.
//
// NOTE: This is a part of the BatchAuctioneer interface.
func (u *UniformPriceCallMarket) ForgetAsks(asks ...orderT.Nonce) error {
	u.Lock()
	defer u.Unlock()

	for _, askNonce := range asks {
		askElement, ok := u.askIndex[askNonce]
		if !ok {
			continue
		}

		u.asks.Remove(askElement)
		delete(u.askIndex, askNonce)
	}

	return nil
}

// A compile-time assertion to ensure that the UniformPriceCallMarket meets the
// BatchAuctioneer interface.
var _ BatchAuctioneer = (*UniformPriceCallMarket)(nil)
