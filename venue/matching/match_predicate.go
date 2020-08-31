package matching

import "github.com/lightninglabs/subasta/order"

var (
	// DefaultPredicateChain is the default chain of match predicates that
	// is applied to all orders.
	DefaultPredicateChain = []MatchPredicate{
		MatchPredicateFunc(DifferentAccountsPredicate),
		MatchPredicateFunc(DifferentNodesPredicate),
		MatchPredicateFunc(AskRateSmallerOrEqualPredicate),
		MatchPredicateFunc(AskDurationGreaterOrEqualPredicate),
	}
)

// MatchPredicate is an interface that implements a generic matching predicate.
type MatchPredicate interface {
	// IsMatchable returns true if this specific predicate doesn't have any
	// objection about two orders being matched. This does not yet mean the
	// match will succeed as many predicates are usually chained together
	// and a match only succeeds if _all_ of the predicates return true.
	IsMatchable(ask *order.Ask, bid *order.Bid) bool
}

// MatchPredicateFunc is a simple function type that implements the
// MatchPredicate interface.
type MatchPredicateFunc func(ask *order.Ask, bid *order.Bid) bool

// IsMatchable returns true if this specific predicate doesn't have any
// objection about two orders being matched. This does not yet mean the match
// will succeed as many predicates are usually chained together and a match only
// succeeds if _all_ of the predicates return true.
//
// NOTE: This is part of the MatchPredicate interface.
func (f MatchPredicateFunc) IsMatchable(ask *order.Ask, bid *order.Bid) bool {
	return f(ask, bid)
}

// DifferentAccountsPredicate is a matching predicate that returns true if two
// orders don't belong to the same account.
func DifferentAccountsPredicate(ask *order.Ask, bid *order.Bid) bool {
	return ask.AcctKey != bid.AcctKey
}

// DifferentNodesPredicate is a matching predicate that returns true if two
// orders don't come from the same node.
func DifferentNodesPredicate(ask *order.Ask, bid *order.Bid) bool {
	return ask.NodeKey != bid.NodeKey
}

// AskRateSmallerOrEqualPredicate is a matching predicate that returns true if
// the ask's rate is lower or equal to the bid's rate.
func AskRateSmallerOrEqualPredicate(ask *order.Ask, bid *order.Bid) bool {
	return ask.FixedRate <= bid.FixedRate
}

// AskDurationGreaterOrEqualPredicate is a matching predicate that returns true
// if the ask's max duration is greater than the bid's min duration. The bid
// declares the minimum amount of time channels are needed, while asks declare
// the maximum time. If the minimum for a bid is above the ask, then no match
// can occur.
func AskDurationGreaterOrEqualPredicate(ask *order.Ask, bid *order.Bid) bool {
	return ask.MaxDuration() >= bid.MinDuration()
}

// TODO(roasbeef): need to consider other fields like the min accepted
// channel and such

// ChainMatches returns true if all predicates in the given chain are matchable
// to the given ask and bid orders.
func ChainMatches(ask *order.Ask, bid *order.Bid,
	chain ...MatchPredicate) bool {

	for _, pred := range chain {
		if !pred.IsMatchable(ask, bid) {
			return false
		}
	}

	return true
}
