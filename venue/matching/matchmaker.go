package matching

import (
	"container/list"
	"encoding/hex"

	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/llm/terms"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
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

	// acctFetcher is the function that'll be used to fetch the latest
	// state of an account from disk so we can do things like compute the
	// fee report using the latest account balance for a trader.
	acctFetcher AccountFetcher
}

// NewUniformPriceCallMarket returns a new instance of the
// UniformPriceCallMarket struct given the price clearer and fee schedule for
// this current batch epoch.
func NewUniformPriceCallMarket(priceClearer PriceClearer,
	feeSchedule terms.FeeSchedule,
	acctFetcher AccountFetcher) *UniformPriceCallMarket {

	u := &UniformPriceCallMarket{
		priceClearer: priceClearer,
		feeSchedule:  feeSchedule,
		acctFetcher:  acctFetcher,
	}
	u.resetOrderState()

	return u

}

// resetOrderState resets the order state to blank.
func (u *UniformPriceCallMarket) resetOrderState() {
	u.bids = list.New()
	u.bidIndex = make(map[orderT.Nonce]*list.Element)

	u.asks = list.New()
	u.askIndex = make(map[orderT.Nonce]*list.Element)
}

// MaybeClear attempts to clear a batch given a BatchID. Note that it's
// possible no match is possible, in which case an error will be
// returned.
//
// NOTE: This method is a part of the BatchAuctioneer interface.
func (u *UniformPriceCallMarket) MaybeClear(_ BatchID,
	feeRate chainfee.SatPerKWeight) (*OrderBatch, error) {

	// At this point we know we have a set of orders, so we'll create the
	// match maker for usage below.
	matchMaker := NewMultiUnitMatchMaker(u.acctFetcher)

	// At this point, there's may be at least a single order that we can
	// execute, so we'll attempt to match the entire pending batch.
	//
	// First we'll obtain slices pointing to the backing list so the match
	// maker can examine all the entries easily.
	var (
		bids = make([]*order.Bid, 0, u.bids.Len())
		asks = make([]*order.Ask, 0, u.asks.Len())

		// We'll skip orders having a max batch fee rate lower than our
		// current estimate.
		skippedBids []*order.Bid
		skippedAsks []*order.Ask
	)

	for bid := u.bids.Front(); bid != nil; bid = bid.Next() {
		b := bid.Value.(order.Bid)

		if b.MaxBatchFeeRate < feeRate {
			skippedBids = append(skippedBids, &b)
			continue
		}

		bids = append(bids, &b)
	}
	for ask := u.asks.Front(); ask != nil; ask = ask.Next() {
		a := ask.Value.(order.Ask)

		if a.MaxBatchFeeRate < feeRate {
			skippedAsks = append(skippedAsks, &a)
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

	// Now that we know which orders will execute in this batch, we'll
	// update our in-memory set of orders so we can properly make a market
	// for the next batch.
	//
	// TODO(roasbeef): remove orders instead?
	u.resetOrderState()
	if err := u.ConsiderBids(matchSet.UnmatchedBids...); err != nil {
		return nil, err
	}
	if err := u.ConsiderAsks(matchSet.UnmatchedAsks...); err != nil {
		return nil, err
	}
	if err := u.ConsiderBids(skippedBids...); err != nil {
		return nil, err
	}
	if err := u.ConsiderAsks(skippedAsks...); err != nil {
		return nil, err
	}

	// Next, we'll also add any orders that weren't fully consumed we'll
	// also re-add those to the current active order set.
	for _, order := range matchSet.MatchedOrders {
		bid := order.Details.Bid
		if bid.UnitsUnfulfilled != 0 {
			if err := u.ConsiderBids(bid); err != nil {
				return nil, err
			}
		}

		ask := order.Details.Ask
		if ask.UnitsUnfulfilled != 0 {
			if err := u.ConsiderAsks(ask); err != nil {
				return nil, err
			}
		}
	}

	// As a final step, we'll compute the diff for each trader's account.
	// With this final piece of information, the caller will be able to
	// easily update all the order/account state in a single atomic
	// transaction.
	feeReport := NewTradingFeeReport(
		matchSet.MatchedOrders, u.feeSchedule, clearingPrice,
	)

	return &OrderBatch{
		Orders:        matchSet.MatchedOrders,
		FeeReport:     feeReport,
		ClearingPrice: clearingPrice,
	}, nil
}

// ConsiderBid adds a set of bids to the staging arena for match making. Only
// once a bid has been considered will it be eligible to be included in an
// OrderBatch.
//
// NOTE: This is a part of the BatchAuctioneer interface.
func (u *UniformPriceCallMarket) ConsiderBids(bids ...*order.Bid) error {
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
