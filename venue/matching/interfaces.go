package matching

import (
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
)

// FulfillType is an enum-like variable that expresses the "nature" of a match.
// In the system we accept partial matches, so we need to be able to express
// the two types of partial matches (bid vs ask).
type FulfillType uint8

const (
	// TotalFulfill indicates that both the ask and bid can be fully
	// consumed during this matching event.
	TotalFulfill FulfillType = iota

	// PartialAskFulfill indicates that the bid was fully consumed, but
	// there's a remaining amount unfilled in the target ask.
	PartialAskFulfill

	// PartialBidFulfill indicates that the ask was fully consumed, but
	// there's a remaining amount unfilled in the target bid.
	PartialBidFulfill
)

// PriceQuote describes a potential match along with all the pricing and unit
// details at a given point in time. This struct can be used to make match
// making decisions, and also to clear all the orders once a match has been
// made.
//
// TODO(roasbef): rename to MatchDetails?
type PriceQuote struct {
	// MatchingRate is the rate that the two orders matched at. This rate
	// is the bidder's price.
	MatchingRate orderT.FixedRatePremium

	// TotalSatsCleared is the total amount of satoshis cleared, or the
	// channel size that will ultimately be created once the order is
	// executed.
	TotalSatsCleared btcutil.Amount

	// UnitsMatched is the total amount of units matched in this price
	// quote.
	UnitsMatched orderT.SupplyUnit

	// UnitsUnmatched is the total amount of units that remain unmatched in
	// this price quote.
	UnitsUnmatched orderT.SupplyUnit

	// Type is the type of fulfil possibly with this price quote.
	Type FulfillType
}

// NullQuote is a special price quote which signals that given the set of
// current asks/bids, a market cannot be made.
var NullQuote PriceQuote

// OrderPair groups together two matching orders (the ask and the bid), along
// with the matching information that relates the two.
type OrderPair struct {
	// Ask is the ask that has been fully or partially matched.
	Ask *order.Ask

	// Bid is the ask that has been fully or partially matched.
	Bid *order.Bid

	// Quote contains the matching details that led to this pairing.
	Quote PriceQuote
}

// MatchedOrder couples the details of a match (the order pair), with the two
// traders that placed the orders.
type MatchedOrder struct {
	// Asker is the trader that the ask belongs to.
	Asker Trader

	// Bidder is the trader that the bid belong to.
	Bidder Trader

	// Details packages the order details such as the parameters of each
	// ask/bid and the final rate to be executed.
	Details OrderPair
}

// MatchSet is the final output of a match making session. This packages all
// the orders that have been matched in the past batch, along with the set of
// unmatched orders as those may be used for determining the final clearing
// price.
type MatchSet struct {
	// MatchedOrders is the set of order pairs that have been matched with
	// each other. Note that an order may appear multiple times if it
	// participated in a series of partial matches.
	MatchedOrders []MatchedOrder

	// UnmatchedBids is the set of bids that weren't matched in this batch.
	UnmatchedBids []*order.Bid

	// UnmatchedAsks is the ask of asks that weren't matched in this batch.
	UnmatchedAsks []*order.Ask
}

// MatchMaker is the top-level interface that's responsible for locating any
// possible matches within the existing order book. The greater auctioneer will
// create a new instance of this interface for each new batch interval.
type MatchMaker interface {
	// MatchPossible returns a price quote (which may be null) as well as a
	// bool that indicates if a match is possible given the passed bid and
	// ask.
	MatchPossible(*order.Bid, *order.Ask) (PriceQuote, bool)

	// MatchBatch attempts to match an entire batch of orders resulting in
	// a final MatchSet. If no match is possible, then an error is
	// returned. A caller should instead first call the MatchPossible
	// method to ensure that this method can execute fully before calling
	// it.
	MatchBatch(bids []*order.Bid, asks []*order.Ask) (*MatchSet, error)
}

// PriceClearer is an interfaces that allows the auctioneer to determine a
// uniform clearing price after all match making has succeeded. The uniform
// clearing price is intended to ensure that all agents in the market receive a
// fair price based on their bidding/asking material.
type PriceClearer interface {
	// ExtractClearingPrice will determine a uniform clearing price given
	// the entire match set. Only a single price is to be returned. Many
	// possible algorithms exists to determine a uniform clearing price
	// such as: first-rejected-bid, last-accepted-bid, and so on.
	ExtractClearingPrice(*MatchSet) (orderT.FixedRatePremium, error)
}

// OrderBatch is a final matched+cleared auction batch. This batch contains
// everything needed to move onto the execution phase. The included
// TradingFeeReport instance is essentially an accounting report detailing how
// money exchanged hands in the batch.
type OrderBatch struct {
	// Orders is the set of matched orders in this batch.
	Orders []MatchedOrder

	// FeeReport is a report describing all the fees paid in the batch.
	// Note that these are only _trading_ fees and don't yet included any
	// fee that need to be paid on chain within the batch execution
	// transaction.
	FeeReport TradingFeeReport

	// ClearingPrice is the single clearing price that all traders in the
	// batch will pay as computed within the FeeReport above.
	ClearingPrice orderT.FixedRatePremium
}

// NewBatch returns a new batch with the given match data.
func NewBatch(orders []MatchedOrder, feeReport TradingFeeReport,
	price orderT.FixedRatePremium) *OrderBatch {

	return &OrderBatch{
		Orders:        orders,
		FeeReport:     feeReport,
		ClearingPrice: price,
	}
}

// EmptyBatch returns an empty batch.
func EmptyBatch() *OrderBatch {
	return &OrderBatch{}
}

// Copy performs a deep copy of the passed OrderBatch instance.
func (o *OrderBatch) Copy() OrderBatch {
	orders := make([]MatchedOrder, 0, len(o.Orders))
	for _, matchedOrder := range o.Orders {
		ask := order.Ask{
			Ask: matchedOrder.Details.Ask.Ask,
			Kit: matchedOrder.Details.Ask.Kit,
		}
		bid := order.Bid{
			Bid: matchedOrder.Details.Bid.Bid,
			Kit: matchedOrder.Details.Bid.Kit,
		}

		orders = append(orders, MatchedOrder{
			Asker:  matchedOrder.Asker,
			Bidder: matchedOrder.Bidder,
			Details: OrderPair{
				Ask:   &ask,
				Bid:   &bid,
				Quote: matchedOrder.Details.Quote,
			},
		})
	}

	feeReport := TradingFeeReport{
		AccountDiffs:          make(map[AccountID]AccountDiff),
		AuctioneerFeesAccrued: o.FeeReport.AuctioneerFeesAccrued,
	}
	for acctID, accountDiff := range o.FeeReport.AccountDiffs {
		var output *wire.TxOut
		if accountDiff.RecreatedOutput != nil {
			o := *accountDiff.RecreatedOutput
			output = &o
		}

		trader := *accountDiff.StartingState
		tally := *accountDiff.AccountTally

		feeReport.AccountDiffs[acctID] = AccountDiff{
			StartingState:   &trader,
			RecreatedOutput: output,
			AccountTally:    &tally,
		}
	}

	return OrderBatch{
		Orders:        orders,
		FeeReport:     feeReport,
		ClearingPrice: o.ClearingPrice,
	}
}

// BatchAuctioneer is the main entry point of this package. The BatchAuctioneer
// implements a variant of a frequent batched auction. Details such as how the
// uniform price are computed, the interval of the batch, etc are left up to
// the concrete implementation.
//
// TODO(roasbeef): just pass in order book instead?
//  * needs to be interface? other impl is the continuous variant?
type BatchAuctioneer interface {
	// MaybeClear attempts to clear a batch given the order filter and a
	// chain of match predicates to check. Note that it can happen that no
	// match is possible, in which case an error will be returned.
	//
	// The order filters provided will be used to exclude orders which are
	// currently not suitable to be included for matchmaking, for example
	// because the estimated batch fee rate is higher than their set
	// maximum.
	MaybeClear(acctCacher AccountCacher, filterChain []OrderFilter,
		predicateChain []MatchPredicate) (*OrderBatch, error)

	// RemoveMatches updates the order book by subtracting the given
	// matches filled volume.
	RemoveMatches(...MatchedOrder) error

	// ConsiderBid adds a set of bids to the staging arena for match
	// making. Only once a bid has been considered will it be eligible to
	// be included in an OrderBatch.
	ConsiderBids(...*order.Bid) error

	// ForgetBid removes a set of bids from the staging area.
	ForgetBids(...orderT.Nonce) error

	// ConsiderAsk adds a set of asks to the staging arena for match
	// making. Only once an ask has been considered will it be eligible to
	// be included in an OrderBatch.
	ConsiderAsks(...*order.Ask) error

	// ForgetAsk removes a set of asks from the staging area.
	ForgetAsks(...orderT.Nonce) error
}
