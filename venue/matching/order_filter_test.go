package matching

import (
	"testing"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	order1 = &order.Ask{
		Ask: orderT.Ask{
			Kit: orderT.Kit{
				MaxBatchFeeRate: 123,
				LeaseDuration:   144,
			},
		},
	}
	order2 = &order.Ask{
		Ask: orderT.Ask{
			Kit: orderT.Kit{
				MaxBatchFeeRate: 234,
				LeaseDuration:   1440,
			},
		},
	}
	order3 = &order.Ask{
		Ask: orderT.Ask{
			Kit: orderT.Kit{
				MaxBatchFeeRate: 345,
				LeaseDuration:   2016,
			},
		},
	}
)

func TestBatchFeeRateFilter(t *testing.T) {
	t.Parallel()

	filter := NewBatchFeeRateFilter(222)
	require.False(t, filter.IsSuitable(order1))
	require.True(t, filter.IsSuitable(order2))
	require.True(t, filter.IsSuitable(order3))

	filter = NewBatchFeeRateFilter(123)
	require.True(t, filter.IsSuitable(order1))
	require.True(t, filter.IsSuitable(order2))
	require.True(t, filter.IsSuitable(order3))

	filter = NewBatchFeeRateFilter(444)
	require.False(t, filter.IsSuitable(order1))
	require.False(t, filter.IsSuitable(order2))
	require.False(t, filter.IsSuitable(order3))
}

func TestLeaseDurationFilter(t *testing.T) {
	t.Parallel()

	filter := NewLeaseDurationFilter(144)
	require.True(t, filter.IsSuitable(order1))
	require.False(t, filter.IsSuitable(order2))
	require.False(t, filter.IsSuitable(order3))

	filter = NewLeaseDurationFilter(2016)
	require.False(t, filter.IsSuitable(order1))
	require.False(t, filter.IsSuitable(order2))
	require.True(t, filter.IsSuitable(order3))
}

func TestAccountPredicate(t *testing.T) {
	t.Parallel()

	fetcher := func(acctKey AccountID) (*account.Account, error) {
		if acctKey == acct1Key {
			return acct1, nil
		}
		return acct2, nil
	}

	acct1banned := false
	acct2banned := false
	allowedChecker := func(_, acctKey [33]byte) bool {
		if acctKey == acct1Key && acct1banned {
			return false
		}

		if acctKey == acct2Key && acct2banned {
			return false
		}

		return true
	}

	cutoff := uint32(144)
	p := NewAccountPredicate(fetcher, cutoff, allowedChecker)

	// Make sure that by default we can match the two test orders.
	assert.True(t, p.IsMatchable(node1Ask, node2Bid))

	// Banned accounts shouldn't be matchable.
	acct1banned = true
	assert.False(t, p.IsMatchable(node1Ask, node2Bid))
	acct1banned = false
	acct2banned = true
	assert.False(t, p.IsMatchable(node1Ask, node2Bid))
	acct1banned = false
	acct2banned = false

	// Accounts close to expiry should also not match.
	acct1.Expiry = 144
	assert.False(t, p.IsMatchable(node1Ask, node2Bid))
	acct1.Expiry = 2016

	// Accounts in the wrong state also shouldn't be matched.
	acct1.State = account.StatePendingOpen
	assert.False(t, p.IsMatchable(node1Ask, node2Bid))
}
