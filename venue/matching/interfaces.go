package matching

import (
	"github.com/btcsuite/btcutil"
	orderT "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
)

// FixedRatePremium..
type FixedRatePremium uint32

// FulfillType...
type FulfillType uint8

const (
	// TotalFulfill...
	TotalFulfill FulfillType = iota

	// PartialAskFulfill..
	PartialAskFulfill

	// PartialBidFulfill..
	PartialBidFulfill
)

// PriceQuote...
//
// TODO(roasbef): rename to MatchDetails?
type PriceQuote struct {
	// ClearingRate...
	ClearingRate FixedRatePremium

	// TotalSatsCleared...
	TotalSatsCleared btcutil.Amount

	// UnitsMatched...
	//
	// TODO(roasbeef): methods to convert back and forth from units to
	// satoshis?
	//
	// amount of bid filled
	UnitsMatched orderT.SupplyUnit

	// UnitsUnmatched...
	//
	// supply remaining
	//
	// TODO(roasbeef): don't need? can recompute with added methods?
	UnitsUnmatched orderT.SupplyUnit

	// Type...
	Type FulfillType

	// TODO(roasbeef): make the constituent unit 100k satoshis?
	//  * all orders are then some multiple of that
}

// NullQuote is a special price quote which signals that given the set of
// current asks/bids, a market cannot be made.
var NullQuote PriceQuote

// OrderPair...
type OrderPair struct {
	// Ask...
	Ask *order.Ask

	// Bid...
	Bid *order.Bid

	// Quote...
	Quote PriceQuote
}

// MatchedOrder...
type MatchedOrder struct {
	// Asker...
	Asker Trader

	// Bidder...
	Bidder Trader

	// Details...
	Details OrderPair

	// TODO(roasbeef): methods or bool for partial match?
}

// MatchSet...
type MatchSet struct {
	// MatchedOrders...
	MatchedOrders []MatchedOrder

	// UnmatchedBids...
	UnmatchedBids []*order.Bid

	// UnmatchedAsks...
	UnmatchedAsks []*order.Ask
}

// MatchMaker...
type MatchMaker interface {
	// MatchPossible
	//
	// TODO(roasbeeF): actually just return the bool (yes or no)
	//
	// TODO(roasbeef): rename back to MaybeMatch?
	MatchPossible(*order.Bid, *order.Ask) (PriceQuote, bool)

	// MatchBatch...
	//
	MatchBatch(bids []*order.Bid, asks []*order.Ask) (*MatchSet, error)

	// TODO(roasbeef): pass in clearing algo in match?
}

// PriceClearer...
//
// TODO(roasbeef): just makes this a function type instead?
type PriceClearer interface {
	// ExtractClearingPrice...
	ExtractClearingPrice(*MatchSet) (FixedRatePremium, error)
}

// OrderBatch...
type OrderBatch struct {
	// Orders...
	Orders []MatchedOrder

	// FeeReport...
	// FeeReport TradingFeeReport

	// ClearingPrice...
	ClearingPrice FixedRatePremium
}

// BatchAuctioneer
//
// TODO(roasbeef): just pass in order book instead?
//  * needs to be interface? other impl is the continuous variant?
type BatchAuctioneer interface {
	// MaybeClear...
	//
	// TODO(roasbeef): might need other info...
	// MaybeClear(BatchID) (*OrderBatch, error)

	// ConsiderBid..
	ConsiderBids(...*order.Bid) error

	// ForgetBid...
	ForgetBids(...orderT.Nonce) error

	// ConsiderAsk...
	ConsiderAsks(...*order.Ask) error

	// ForgetAsk...
	//
	// TODO(roasbeef): key w/ order nonce
	ForgetAsks(...orderT.Nonce) error

	// TODO(roasbeef): don't need? just pass in as param in constructor?
	//
	// ApplyAuctionAlgos(PriceClearer, MatchMaker)
	//
	// ApplyClearingMethod(ClearingPriceAlgo) error
}
