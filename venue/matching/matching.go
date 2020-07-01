package matching

import (
	"sort"

	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
)

// MultiUnitMatchMaker is a conrete implemtantion of the MatchMaker interface.
// This match maker is multi-uni tin that it accepts partial fills of bids and
// asks. Note that since this struct is stateful a _new_ instance should be
// created for each new match making attempt.
type MultiUnitMatchMaker struct {
	// orderRemainders is the amount left over after a partial fill for
	// each active order.
	orderRemainders map[orderT.Nonce]orderT.SupplyUnit

	// fetchAcct fetches the latest state of an account identified by its
	// trader public key.
	fetchAcct AccountFetcher

	// accountCache maintains a cache of trader accounts which are retrieved
	// throughout matchmaking.
	accountCache map[[33]byte]*account.Account
}

// AccountFetcher denotes a function that's able to fetch the latest state of
// an account which is identified the its trader public key (or acctID).
type AccountFetcher func(AccountID) (*account.Account, error)

// NewMultiUnitMatchMaker creates a new instance of the MultiUnitMatchMaker.
//
// TODO(roasbeef): comparator function for tie-breaking? can be sued to give
// preferred fulfils if needed
func NewMultiUnitMatchMaker(acctFetcher AccountFetcher) *MultiUnitMatchMaker {
	return &MultiUnitMatchMaker{
		orderRemainders: make(map[orderT.Nonce]orderT.SupplyUnit),
		fetchAcct:       acctFetcher,
		accountCache:    make(map[[33]byte]*account.Account),
	}
}

// getCachedAccount retrieves the account with the given key from the cache. If
// if it hasn't been cached yet, then it's retrieved from disk and cached.
func (m *MultiUnitMatchMaker) getCachedAccount(key [33]byte) (*account.Account,
	error) {

	if account, ok := m.accountCache[key]; ok {
		return account, nil
	}

	account, err := m.fetchAcct(key)
	if err != nil {
		return nil, err
	}
	m.accountCache[key] = account

	return account, nil
}

// isAccountReady determines whether an account is ready to participate in a
// batch.
func isAccountReady(acct *account.Account) bool {
	return acct.State == account.StateOpen
}

// MatchPossible returns a price quote, and a bool indicating if a match is
// possible. Note that this method doesn't clear any orders (update balances or
// partial fills). Instead this method is a pure function and should be used to
// determine if a match can take place, and how to clear the matched orders.
//
// NOTE: This method is part of the MatchMaker interface.
func (m *MultiUnitMatchMaker) MatchPossible(bid *order.Bid,
	ask *order.Ask) (PriceQuote, bool) {

	var (
		matchType FulfillType

		unitsMatched, unitsUnmatched orderT.SupplyUnit
	)

	bidAcct, err := m.getCachedAccount(bid.AcctKey)
	if err != nil {
		return NullQuote, false
	}
	askAcct, err := m.getCachedAccount(ask.AcctKey)
	if err != nil {
		return NullQuote, false
	}

	// To start with, we'll obtain the total amount of units tendered for
	// each ask/bid. If there's an entry in our partial match (remainders)
	// map, then we'll use that as the final number of units for each side.
	bidSupply, askSupply := bid.UnitsUnfulfilled, ask.UnitsUnfulfilled
	if bidRemainder, ok := m.orderRemainders[bid.Nonce()]; ok {
		bidSupply = bidRemainder
	}
	if askRemainder, ok := m.orderRemainders[ask.Nonce()]; ok {
		askSupply = askRemainder
	}

	// TODO(roasbeef): need to factor in the TIME as well
	//  * just need sanity check for compat? will need zip based ordering
	//    in the end then?

	// The two items passed should be the highest bid, and the lowest ask.
	// This is our base case, and is where we employ the bulk of our
	// algorithm: if the highest bid is lower than the lowest ask, then
	// there is no market to be made.
	switch {
	// Ensure that if the order is made by the same trader, then we reject
	// the match as making a channel to yourself doesn't make any sense.
	case ask.AcctKey == bid.AcctKey:
		return NullQuote, false

	// Ensure we don't match any orders of the same node. It doesn't make
	// sense to open a channel to one self, and the protocol doesn't allow
	// it anyway.
	case ask.NodeKey == bid.NodeKey:
		return NullQuote, false

	// Ensure both accounts are ready to participate in a batch.
	case !isAccountReady(bidAcct) || !isAccountReady(askAcct):
		return NullQuote, false

	// If the highest bid is below the lowest ask, then no match at all is
	// possible.
	case bid.FixedRate < ask.FixedRate:
		return NullQuote, false

	// Next we'll compare the lease duration values for compatibility. The
	// bid declares the minimum amount of time channels are needed, while
	// asks declare the maximum time. If the minimum for a bid is above the
	// ask, then no match can occur.
	case bid.MinDuration() > ask.MaxDuration():
		return NullQuote, false

	// If the highest bid is greater than or equal to the lowest ask, then
	// we may have a match, but it depends on other constraints like the
	// available supply.
	case bid.FixedRate >= ask.FixedRate:

		// At this point we know we have a match, the only remaining work
		// is to ascertain exactly _what_ type of match it is.
		switch {

		// We have a match! However, it's a partial fill for the bid.
		// In this case we'll mark it as a proper match, but that the
		// bid still has units to be fulfilled.
		case bidSupply > askSupply:
			matchType = PartialBidFulfill

			unitsMatched = askSupply
			unitsUnmatched = bidSupply - askSupply

		// Like the above case, this is also a match. However, in this
		// case that Ask can still be paired with other bids to attempt
		// to fully fill the order.
		case bidSupply < askSupply:
			matchType = PartialAskFulfill

			unitsMatched = bidSupply
			unitsUnmatched = askSupply - bidSupply

		// If the bid and ask supply match exactly, then we have a
		// total fulfil. In this case there're no unmatched units, and
		// we match the entire of the supply.
		case bidSupply == askSupply:
			matchType = TotalFulfill

			unitsMatched = bidSupply
			unitsUnmatched = 0
		}

		// TODO(roasbeef): need to consider other fields like the min accepted
		// channel and such
	}

	// TODO(roasbeef): split into two diff methods?
	//  * MatchPossible... (external)
	//  * clearMatch... (updates internal state, private method?)

	return PriceQuote{
		MatchingRate:     orderT.FixedRatePremium(bid.FixedRate),
		TotalSatsCleared: unitsMatched.ToSatoshis(),
		UnitsMatched:     unitsMatched,
		UnitsUnmatched:   unitsUnmatched,
		Type:             matchType,
	}, true
}

// MatchBatch attempts to match an entire batch of orders resulting in a final
// MatchSet. If no match is possible, then an error is returned. A caller
// should instead first call the MatchPossible method to ensure that this
// method can execute fully before calling it. If matches are indeed possible,
// then this method will return a set of all matched orders, clearing any
// intermediate balances for partially fulfilled orders along the way. The set
// of return unmatched orders will also be updated to reflect the amount of
// unfilled units.
//
// TODO(roasbeef): instead return the order book?
//  * add method to manager to create order book
//
// NOTE: This method is part of the MatchMaker interface.
func (m *MultiUnitMatchMaker) MatchBatch(bids []*order.Bid,
	asks []*order.Ask) (*MatchSet, error) {

	// First we need to sort the bids in order of descending price (highest
	// first), and the asks in order of ascending price (lowest first). At
	// this point, the caller should've already been notified that a marker
	// is possible or not via the MatchPossible method.
	//
	// Note that we do a stable sort purposefully here, as we'll retain the
	// order of the main list in order to implement discriminatory pricing if
	// needed (not all orders can be fully matched, so we'll use the
	// ordering as a tie breaker).
	sort.SliceStable(bids, func(i, j int) bool {
		return bids[i].FixedRate > bids[j].FixedRate
	})
	sort.SliceStable(asks, func(i, j int) bool {
		return asks[i].FixedRate < asks[j].FixedRate
	})

	// Now that the orders have been sorted, we'll attempt to make a market
	// given the current uniform clearing price.
	var matchedOrders []MatchedOrder
	matchedIndex := make(map[orderT.Nonce]struct{})

	// In our scan we'll maintain two top-level pointers to the top of the
	// sorted ask/bid lists respectively), and one local pointer for our
	// current matching attempt. Our current algorithm is O(M*N) in the
	// worst-case, for each bid, we'll end up running through each ask to
	// check for a possible match.
	for bidPointer := range bids {
		// Within our inner loop, we'll attempt to match the current
		// top bid in our list with the current unfilled match. In the
		// worst case we run through the entire ask list for this bid.
		// If at any time during this inner loop, the bid becomes
		// exhausted, then we'll exit early.
		for askPointer := range asks {
			ask, bid := asks[askPointer], bids[bidPointer]

			// If the available supply of either the ask is zero,
			// then we'll skip it as we can't lease a non-existent
			// amount of coins for a channel.
			if ask.UnitsUnfulfilled == 0 {
				continue
			}

			matchDetails, ok := m.MatchPossible(bid, ask)

			// If no match is possible between this Ask, then we
			// actually aren't done as we're actually doing
			// multi-dimensional matching. Instead, we'll proceed
			// to the next available ask which may have more
			// compatible constraints.
			if !ok {
				continue
			}

			askNonce := ask.Nonce()
			bidNonce := bid.Nonce()
			switch matchDetails.Type {
			// Both orders have fully executed, in this case the
			// orders are finalized, and we can move onto the next
			// bid and ask.
			case TotalFulfill:
				// We increment the next bid to move out top
				// level pointer, and also clear out any state
				// for unfulfilled units.
				m.orderRemainders[bidNonce] = 0
				bid.UnitsUnfulfilled = 0

				// Next we'll update the supply tallies for the
				// ask, and break out so we move unto the next
				// bid.
				m.orderRemainders[askNonce] = 0
				ask.UnitsUnfulfilled = 0

			// The bid has only partially be fulfilled, but the ask
			// has been exhausted. In this case, we'll move onto
			// the next ask, and note the remaining portion of the
			// bid to be filled.
			case PartialBidFulfill:
				// Now we'll update the supply tallies for this
				// bid, as it still has more units to give.
				unitsRemaining := matchDetails.UnitsUnmatched
				m.orderRemainders[bidNonce] = unitsRemaining
				bid.UnitsUnfulfilled = unitsRemaining

				// As the ask is fully satisfied, we'll set the
				// number of unfilled units to zero.
				ask.UnitsUnfulfilled = 0
				m.orderRemainders[askNonce] = 0

			// The bid has been fully consumed, but we have more
			// supply remaining that's unmatched for this ask. As a
			// result, we'll move onto the next bid and update the
			// remaining ask amount left for the untouched order.
			case PartialAskFulfill:
				m.orderRemainders[askNonce] = matchDetails.UnitsUnmatched
				ask.UnitsUnfulfilled = matchDetails.UnitsUnmatched

				// As the bid is fully satisfied, we'll set the
				// number of unfilled units to zero.
				bid.UnitsUnfulfilled = 0
				m.orderRemainders[bidNonce] = 0
			}

			// TODO(roasbeef): move the updates of the map into an order
			// clearing method?
			//  * rename to maker/taker?

			matchedIndex[bid.Nonce()] = struct{}{}
			matchedIndex[ask.Nonce()] = struct{}{}

			bidAcct, err := m.getCachedAccount(bid.AcctKey)
			if err != nil {
				return nil, err
			}
			askAcct, err := m.getCachedAccount(ask.AcctKey)
			if err != nil {
				return nil, err
			}

			matchedOrder := MatchedOrder{
				Asker:  NewTraderFromAccount(askAcct),
				Bidder: NewTraderFromAccount(bidAcct),
				Details: OrderPair{
					Ask:   ask,
					Bid:   bid,
					Quote: matchDetails,
				},
			}

			matchedOrders = append(matchedOrders, matchedOrder)

			// If the order type wasn't a partial bid fulfil, then
			// this means that the bid has been fully consumed. As
			// a result, we break early from the inner loop to move
			// onto the next unmatched bid.
			if matchDetails.Type != PartialBidFulfill {
				break
			}
		}
	}

	// Now we'll reconstruct the set of bids and asks that weren't matched
	// at all. These will be re-added to the next batch as is.
	unfilledAsks := make([]*order.Ask, 0, len(asks))
	unfilledBids := make([]*order.Bid, 0, len(bids))
	for _, ask := range asks {
		if _, ok := matchedIndex[ask.Nonce()]; ok {
			continue
		}
		unfilledAsks = append(unfilledAsks, ask)
	}
	for _, bid := range bids {
		if _, ok := matchedIndex[bid.Nonce()]; ok {
			continue
		}
		unfilledBids = append(unfilledBids, bid)
	}

	return &MatchSet{
		MatchedOrders: matchedOrders,
		UnmatchedBids: unfilledBids,
		UnmatchedAsks: unfilledAsks,
	}, nil
}

// A compile-time assertion to ensure that the MultiUnitMatchMaker meets the
// MatchMaker interface.
var _ MatchMaker = (*MultiUnitMatchMaker)(nil)
