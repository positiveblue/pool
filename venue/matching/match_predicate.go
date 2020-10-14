package matching

import (
	"sync"
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
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

// AccountFetcher denotes a function that's able to fetch the latest state of
// an account which is identified the its trader public key (or acctID).
type AccountFetcher func(AccountID) (*account.Account, error)

// AccountCacher is an internal interface for a type that can cache accounts.
type AccountCacher interface {
	// GetCachedAccount returns the requested account from the cache or
	// fetches it if a cache miss occurs.
	GetCachedAccount(key [33]byte) (*account.Account, error)
}

// AccountPredicate is a type that implements the MatchPredicate interface to
// make sure two orders can be matched based on their account's state.
type AccountPredicate struct {
	// fetchAcct fetches the latest state of an account identified by its
	// trader public key.
	fetchAcct AccountFetcher

	// accountCache maintains a cache of trader accounts which are retrieved
	// throughout matchmaking.
	accountCache map[[33]byte]*account.Account

	// accountExpiryCutoff is used to determine when an account expires
	// "too soon" to be included in a batch. If the expiry height of an
	// account is before this cutoff, then we'll ignore it when clearing
	// the market.
	accountExpiryCutoff uint32
}

// NewAccountPredicate creates a new account predicate that can fetch and cache
// accounts during its lifetime.
func NewAccountPredicate(acctFetcher AccountFetcher,
	accountExpiryCutoff uint32) *AccountPredicate {

	return &AccountPredicate{
		fetchAcct:           acctFetcher,
		accountCache:        make(map[[33]byte]*account.Account),
		accountExpiryCutoff: accountExpiryCutoff,
	}
}

// IsMatchable returns true if this specific predicate doesn't have any
// objection about two orders being matched. This does not yet mean the match
// will succeed as many predicates are usually chained together and a match only
// succeeds if _all_ of the predicates return true.
//
// NOTE: This is part of the MatchPredicate interface.
func (p *AccountPredicate) IsMatchable(ask *order.Ask, bid *order.Bid) bool {
	bidAcct, err := p.GetCachedAccount(bid.AcctKey)
	if err != nil {
		return false
	}
	askAcct, err := p.GetCachedAccount(ask.AcctKey)
	if err != nil {
		return false
	}

	// Ensure both accounts are ready to participate in a batch.
	return isAccountReady(bidAcct, p.accountExpiryCutoff) &&
		isAccountReady(askAcct, p.accountExpiryCutoff)
}

// GetCachedAccount retrieves the account with the given key from the cache. If
// if it hasn't been cached yet, then it's retrieved from disk and cached.
//
// NOTE: This is part of the AccountCacher interface.
func (p *AccountPredicate) GetCachedAccount(key [33]byte) (*account.Account,
	error) {

	if acct, ok := p.accountCache[key]; ok {
		return acct, nil
	}

	acct, err := p.fetchAcct(key)
	if err != nil {
		return nil, err
	}
	p.accountCache[key] = acct

	return acct, nil
}

// A compile time check to make sure AccountPredicate implements the
// MatchPredicate and AccountCacher interface.
var _ MatchPredicate = (*AccountPredicate)(nil)
var _ AccountCacher = (*AccountPredicate)(nil)

// isAccountReady determines whether an account is ready to participate in a
// batch.
func isAccountReady(acct *account.Account, expiryHeightCutoff uint32) bool {
	// If the account is almost about to expire, then we won't allow it in
	// a batch to make sure we don't run into a race condition, which can
	// potentially invalidate the entire batch.
	if expiryHeightCutoff != 0 && acct.Expiry <= expiryHeightCutoff {
		return false
	}

	switch acct.State {
	// In the open state the funding or modification transaction is
	// confirmed sufficiently on chain and there is no possibility of a
	// double spend.
	case account.StateOpen:
		return true

	// If the account was recently involved in a batch and is unconfirmed or
	// not yet sufficiently confirmed, we can still safely allow it to
	// participate in the next batch. This is safe all inputs to this
	// unconfirmed outpoint are co-signed by us and therefore cannot just be
	// double spent by the account owner.
	case account.StatePendingBatch:
		return true

	// Because it is not safe to use the account for a batch if there
	// potentially are "foreign" inputs which we didn't co-sign, we can't
	// allow any other states for batch participation. Non-co-signed inputs
	// that could potentially be double spent can happen with an account
	// deposit for example. And even with an account withdrawal we could run
	// into fee issues if that update transaction is not yet confirmed and
	// has a very low fee.
	default:
		return false
	}
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
// nodes.
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

	return askNodeTier >= bid.MinNodeTier
}

// A compile-time check to ensure that MinNodeRatingPredicate implements the
// MatchPredicate interface.
var _ MatchPredicate = (*MinNodeRatingPredicate)(nil)
