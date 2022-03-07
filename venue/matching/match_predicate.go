package matching

import (
	"sync"
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/ratings"
)

var (
	// DefaultPredicateChain is the default chain of match predicates that
	// is applied to all orders.
	DefaultPredicateChain = []MatchPredicate{
		MatchPredicateFunc(DifferentAccountsPredicate),
		MatchPredicateFunc(DifferentNodesPredicate),
		MatchPredicateFunc(AskRateSmallerOrEqualPredicate),
		MatchPredicateFunc(AskDurationGreaterOrEqualPredicate),
		MatchPredicateFunc(SelfChanBalanceEnabledPredicate),
		MatchPredicateFunc(MatchChannelType),
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
	return ask.LeaseDuration() >= bid.LeaseDuration()
}

// SelfChanBalanceEnabledPredicate is a matching predicate that makes sure the
// asker's node is up-to-date and knows how to account for the self channel
// balance feature of bid orders. Old asker nodes wouldn't correctly calculate
// an account's diff (and also not open the channel correctly) if they didn't
// know of the self channel balance feature. Therefore we need to make sure we
// exclude old asker clients from being matched with bid orders that have a self
// channel balance set.
func SelfChanBalanceEnabledPredicate(ask *order.Ask, bid *order.Bid) bool {
	return bid.SelfChanBalance == 0 ||
		ask.Version >= orderT.VersionSelfChanBalance
}

// MatchChannelType is a matching predicate that makes sure the channel type of
// an ask and bid are compatible with each other.
func MatchChannelType(ask *order.Ask, bid *order.Bid) bool {
	switch {
	// Both orders can specify their desired channel type, match if they're
	// compatible.
	case ask.Version >= orderT.VersionChannelType &&
		bid.Version >= orderT.VersionChannelType:
		return ask.ChannelType == bid.ChannelType

	// Only the ask can specify its desired channel type. We'll attempt to
	// match it with any bids that can't specify their desired channel type,
	// as long as the ask does not want a specific channel type.
	case ask.Version >= orderT.VersionChannelType &&
		bid.Version < orderT.VersionChannelType:
		return ask.ChannelType == orderT.ChannelTypePeerDependent

	// Only the bid can specify its desired channel type. We'll attempt to
	// match it with any asks that can't specify their desired channel type,
	// as long as the bid does not want a specific channel type.
	case ask.Version < orderT.VersionChannelType &&
		bid.Version >= orderT.VersionChannelType:
		return bid.ChannelType == orderT.ChannelTypePeerDependent

	default:
		return true
	}
}

// MinPartialMatchPredicate is a matching predicate that returns true if the
// bid's and ask's supply each satisfy the other's minimum units to match.
// Matchmaking keeps track of each order's unfulfilled supply in-memory, which
// is why the supply needs to be provided out-of-band.
type MinPartialMatchPredicate struct {
	// BidSupply is the latest known unfulfilled supply of the bid order.
	BidSupply orderT.SupplyUnit

	// AskSupply is the latest known unfulfilled supply of the ask order.
	AskSupply orderT.SupplyUnit
}

// IsMatchable returns true if the bid's and ask's supply each satisfy the
// other's minimum units to match.
func (p MinPartialMatchPredicate) IsMatchable(ask *order.Ask, bid *order.Bid) bool {
	return p.AskSupply >= bid.MinUnitsMatch && p.BidSupply >= ask.MinUnitsMatch
}

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

// NodeID is a type alias for a node's raw pubkey.
type NodeID = [33]byte

// NodeConflictMap is a composite type for the map in which node conflicts are
// stored for easy lookup by node IDs.
type NodeConflictMap = map[NodeID]map[NodeID][]*NodeConflict

// NodeConflict denotes an entry in a NodeConflictTracker's conflict map. It
// contains the string based reason for the reported conflict as well as a
// timestamp.
type NodeConflict struct {
	// Reason is the string based reason that was reported for the conflict.
	Reason string

	// Reported is the time stamp the reason was reported at.
	Reported time.Time
}

// NodeConflictTracker is an interface that keeps track of conflicts between
// nodes. It is overloaded to keep conflicts between traders too. That comes
// handy when we want to stop matching orders with sidecars (we do not have
// the recipients nodeID).
type NodeConflictTracker interface {
	// ReportConflict signals that the reporter node has a problem with the
	// subject node. The order of the arguments is important as the reporter
	// will be held accountable for reporting too many false positives.
	ReportConflict(reporter, subject NodeID, reason string)

	// HasConflict returns true if there exists an entry in the conflict map
	// that either a has a conflict with b or b has a conflict with a.
	HasConflict(a, b NodeID) bool

	// Export returns the full conflict map.
	Export() NodeConflictMap

	// Clear empties the conflict map and removes all entries.
	Clear()
}

// NewNodeConflictPredicate creates a new node conflict predicate that also
// implements the NodeConflictTracker interface.
func NewNodeConflictPredicate() *NodeConflictPredicate {
	return &NodeConflictPredicate{
		reports: make(NodeConflictMap),
	}
}

// NodeConflictPredicate is a type that implements the MatchPredicate interface
// to make sure two orders don't come from peers that have a conflict between
// them. A conflict occurs either because of technical reasons (nodes can't
// connect or open channels between each other) or because some nodes don't want
// channels from particular peers (bad reputation or just already existing
// channels).
type NodeConflictPredicate struct {
	// reports is the internal conflict map we keep track of.
	reports NodeConflictMap

	reportsMtx sync.Mutex
}

// IsMatchable returns true if this specific predicate doesn't have any
// objection about two orders being matched. This does not yet mean the match
// will succeed as many predicates are usually chained together and a match only
// succeeds if _all_ of the predicates return true.
//
// NOTE: This is part of the MatchPredicate interface.
func (p *NodeConflictPredicate) IsMatchable(ask *order.Ask,
	bid *order.Bid) bool {

	// For sidecars, we need to check if we have recorded any conflict
	// between the ask node and the order itself (we do not have the
	// recipient's node key).
	if bid.IsSidecar {
		return !p.HasConflict(ask.NodeKey, bid.MultiSigKey)
	}

	return !p.HasConflict(ask.NodeKey, bid.NodeKey)
}

// ReportConflict signals that the reporter node has a problem with the
// subject node. The order of the arguments is important as the reporter
// will be held accountable for reporting too many false positives.
//
// NOTE: This is part of the NodeConflictTracker interface.
func (p *NodeConflictPredicate) ReportConflict(reporter, subject [33]byte,
	reason string) {

	p.reportsMtx.Lock()
	defer p.reportsMtx.Unlock()

	_, ok := p.reports[reporter]
	if !ok {
		p.reports[reporter] = make(map[NodeID][]*NodeConflict)
	}

	p.reports[reporter][subject] = append(
		p.reports[reporter][subject], &NodeConflict{
			Reason:   reason,
			Reported: time.Now(),
		},
	)
}

// HasConflict returns true if there exists an entry in the conflict map
// that either a has a conflict with b or b has a conflict with a.
//
// NOTE: This is part of the NodeConflictTracker interface.
func (p *NodeConflictPredicate) HasConflict(a, b [33]byte) bool {
	p.reportsMtx.Lock()
	defer p.reportsMtx.Unlock()

	// See if a has a conflict with b.
	aReportMap, ok := p.reports[a]
	if ok {
		reports, ok := aReportMap[b]
		if ok && len(reports) > 0 {
			return true
		}
	}

	// See if b has a conflict with a.
	bReportMap, ok := p.reports[b]
	if ok {
		reports, ok := bReportMap[a]
		if ok && len(reports) > 0 {
			return true
		}
	}

	return false
}

// Export returns a copy of the full conflict map.
//
// NOTE: This is part of the NodeConflictTracker interface.
func (p *NodeConflictPredicate) Export() NodeConflictMap {
	p.reportsMtx.Lock()
	defer p.reportsMtx.Unlock()

	clone := make(NodeConflictMap, len(p.reports))
	for reporter, subjectMap := range p.reports {
		clone[reporter] = make(map[NodeID][]*NodeConflict)
		for subject, conflicts := range subjectMap {
			clone[reporter][subject] = conflicts
		}
	}

	return clone
}

// Clear empties the conflict map and removes all entries.
//
// NOTE: This is part of the NodeConflictTracker interface.
func (p *NodeConflictPredicate) Clear() {
	p.reportsMtx.Lock()
	defer p.reportsMtx.Unlock()

	p.reports = make(NodeConflictMap)
}

// A compile time check to make sure NodeConflictPredicate implements the
// NodeConflictTracker and MatchPredicate interface.
var _ NodeConflictTracker = (*NodeConflictPredicate)(nil)
var _ MatchPredicate = (*NodeConflictPredicate)(nil)

// MinNodeRatingPredicate is an order matching predicate that only matches bids
// with an ask backing node that is at or above the target mid node tier.
type MinNodeRatingPredicate struct {
	agency ratings.Agency
}

// NewMinNodeRatingPredicate  returns a new instance of the
// MinNodeRatingPredicate backed by an active ratings agency.
func NewMinNodeRatingPredicate(agency ratings.Agency) *MinNodeRatingPredicate {
	return &MinNodeRatingPredicate{
		agency: agency,
	}
}

// IsMatchable returns true if this specific predicate doesn't have any
// objection about two orders being matched. In this specific instance, we
// require that the tier of the node backed by the ask is greater than or equal
// to the specified min node tier.
func (m *MinNodeRatingPredicate) IsMatchable(ask *order.Ask, bid *order.Bid) bool {
	askNode := ask.NodeKey
	askNodeTier := m.agency.RateNode(askNode)

	result := askNodeTier >= bid.MinNodeTier

	if !result {
		log.Tracef("Cannot match ask %s against bid %s because of "+
			"tier mismatch: wanted min tier %d but ask node has %d",
			ask.Nonce(), bid.Nonce(), bid.MinNodeTier, askNodeTier)
	}

	return result
}

// A compile-time check to ensure that MinNodeRatingPredicate implements the
// MatchPredicate interface.
var _ MatchPredicate = (*MinNodeRatingPredicate)(nil)
