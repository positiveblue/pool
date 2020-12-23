package matching

import (
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// OrderFilter is an interface that implements a generic filter that skips all
// orders from being included in the matchmaking process that don't meet the
// current criteria.
type OrderFilter interface {
	// IsSuitable returns true if this specific predicate doesn't have any
	// objection about an order being included in the matchmaking process.
	IsSuitable(order.ServerOrder) bool
}

// OrderFilterFunc is a simple function type that implements the OrderFilter
// interface.
type OrderFilterFunc func(order.ServerOrder) bool

// IsSuitable returns true if this specific predicate doesn't have any objection
// about an order being included in the matchmaking process.
//
// NOTE: This is part of the OrderFilter interface.
func (f OrderFilterFunc) IsSuitable(o order.ServerOrder) bool {
	return f(o)
}

// SuitsFilterChain returns true if all filters in the given chain see the
// given order as suitable.
func SuitsFilterChain(o order.ServerOrder, chain ...OrderFilter) bool {
	for _, filter := range chain {
		if !filter.IsSuitable(o) {
			return false
		}
	}

	return true
}

// NewBatchFeeRateFilter returns a new filter that filters out orders based on
// the current batch fee rate. All orders that have a max batch fee rate lower
// than the current estimated batch fee rate are skipped.
func NewBatchFeeRateFilter(batchFeeRate chainfee.SatPerKWeight) OrderFilter {
	return OrderFilterFunc(func(o order.ServerOrder) bool {
		if o.Details().MaxBatchFeeRate < batchFeeRate {
			log.Debugf("Filtered out order %v with max fee rate %v",
				o.Nonce(), o.Details().MaxBatchFeeRate)
			return false
		}

		return true
	})
}

// NewLeaseDurationFilter returns a new filter that filters out all orders that
// don't have the given lease duration.
func NewLeaseDurationFilter(leaseDuration uint32) OrderFilter {
	return OrderFilterFunc(func(o order.ServerOrder) bool {
		if o.Details().LeaseDuration != leaseDuration {
			log.Debugf("Filtered out order %v with lease duration "+
				"%v", o.Nonce(), o.Details().LeaseDuration)
			return false
		}

		return true
	})
}

// AccountFetcher denotes a function that's able to fetch the latest state of
// an account which is identified the its trader public key (or acctID).
type AccountFetcher func(AccountID) (*account.Account, error)

// AllowedChecker is a function that is able to check if an account or trader is
// currently ready to be used, so not banned. It returns true if no ban exists
// and the trader/account is generally allowed to participate.
type AllowedChecker func(nodeKey, acctKey [33]byte) bool

// AccountCacher is an internal interface for a type that can cache accounts.
type AccountCacher interface {
	// GetCachedAccount returns the requested account from the cache or
	// fetches it if a cache miss occurs.
	GetCachedAccount(key [33]byte) (*account.Account, error)
}

// AccountFilter is a type that implements the OrderFilter interface to
// make sure an order's account is in the correct state for matchmaking.
type AccountFilter struct {
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

	// allowedChecker is a function that can check if an account or trader
	// is allowed to participate in a batch.
	allowedChecker AllowedChecker
}

// NewAccountFilter creates a new account filter that can fetch and cache
// accounts during its lifetime.
func NewAccountFilter(acctFetcher AccountFetcher, accountExpiryCutoff uint32,
	allowedChecker AllowedChecker) *AccountFilter {

	return &AccountFilter{
		fetchAcct:           acctFetcher,
		accountCache:        make(map[[33]byte]*account.Account),
		accountExpiryCutoff: accountExpiryCutoff,
		allowedChecker:      allowedChecker,
	}
}

// IsSuitable returns true if this specific predicate doesn't have any objection
// about an order being included in the matchmaking process.
//
// NOTE: This is part of the OrderFilter interface.
func (p *AccountFilter) IsSuitable(o order.ServerOrder) bool {
	if !p.allowedChecker(o.ServerDetails().NodeKey, o.Details().AcctKey) {
		log.Debugf("Filtered out order %v with banned trader ("+
			"node=%x, acct=%x)", o.Nonce(),
			o.ServerDetails().NodeKey[:], o.Details().AcctKey[:])

		return false
	}

	acct, err := p.GetCachedAccount(o.Details().AcctKey)
	if err != nil {
		log.Debugf("Filtered out order %v with account not in cache: "+
			"%v", o.Nonce(), err)

		return false
	}

	// Ensure the account is ready to participate in a batch.
	isReady := isAccountReady(acct, p.accountExpiryCutoff)
	if !isReady {
		log.Debugf("Filtered out order %v with account not ready, "+
			"state=%v, expiry=%v, cutoff=%v", o.Nonce(),
			acct.State, acct.State, p.accountExpiryCutoff)
	}

	return isReady
}

// GetCachedAccount retrieves the account with the given key from the cache. If
// if it hasn't been cached yet, then it's retrieved from disk and cached.
//
// NOTE: This is part of the AccountCacher interface.
func (p *AccountFilter) GetCachedAccount(key [33]byte) (*account.Account,
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

// A compile time check to make sure AccountFilter implements the OrderFilter
// and AccountCacher interface.
var _ OrderFilter = (*AccountFilter)(nil)
var _ AccountCacher = (*AccountFilter)(nil)

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

// TraderOnlineFilter is a filter that filters out orders for offline traders.
type TraderOnlineFilter struct {
	isOnline func([33]byte) bool
}

// A compile time check to make sure TraderOnlineFilter implements the
// OrderFilter interface.
var _ OrderFilter = (*TraderOnlineFilter)(nil)

// NewTraderOnlineFilter creates a new order filter using the passed method
// checking whether a trader is online.
func NewTraderOnlineFilter(isOnline func([33]byte) bool) *TraderOnlineFilter {
	return &TraderOnlineFilter{
		isOnline: isOnline,
	}
}

// IsSuitable returns true if this specific predicate doesn't have any objection
// about an order being included in the matchmaking process.
//
// NOTE: This is part of the OrderFilter interface.
func (p *TraderOnlineFilter) IsSuitable(o order.ServerOrder) bool {
	if !p.isOnline(o.Details().AcctKey) {
		log.Debugf("Filtered out order %v with offline trader ("+
			"node=%x, acct=%x)", o.Nonce(),
			o.ServerDetails().NodeKey[:], o.Details().AcctKey[:])
		return false
	}

	return true
}
