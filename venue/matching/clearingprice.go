package matching

import orderT "github.com/lightninglabs/agora/client/order"

// LastAcceptedBid is a uniform clearing price algorithm that selects the
// clearing price to the lowest bid within the candidate batch.
type LastAcceptedBid struct {
}

// ExtractClearingPrice will determine a uniform clearing price given the
// entire match set. Only a single price is to be returned. Many possible
// algorithms exists to determine a uniform clearing price such as:
// first-rejected-bid, last-accepted-bid, and so on.
//
// NOTE: This is a part of the PriceClearer interface.
//
// NOTE: This method requires that there're a non-zero number of orders in the
// given matchSet.
func (l *LastAcceptedBid) ExtractClearingPrice(matchSet *MatchSet) (
	orderT.FixedRatePremium, error) {

	// TODO(roasbeef): buyer's bid??
	//  * actually need to find the intersection point? (only for above?)

	matchedOrders := matchSet.MatchedOrders
	numMatchedOrders := len(matchedOrders)

	lastAcceptedBid := matchedOrders[numMatchedOrders-1].Details.Bid

	return orderT.FixedRatePremium(lastAcceptedBid.FixedRate), nil
}

// A compile-time assertion to ensure that the LastAcceptedBid meets the
// PriceClearer interface.
var _ PriceClearer = (*LastAcceptedBid)(nil)
