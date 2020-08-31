package matching

import (
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
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
