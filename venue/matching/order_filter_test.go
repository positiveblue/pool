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
	order4 = &order.Bid{
		Bid: orderT.Bid{
			Kit: orderT.Kit{
				MaxBatchFeeRate: 345,
				LeaseDuration:   2016,
			},
		},
		IsSidecar: true,
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

func TestTraderOnlineFilter(t *testing.T) {
	t.Parallel()

	online := false
	filter := NewTraderOnlineFilter(func(_ [33]byte) bool {
		return online
	})

	require.False(t, filter.IsSuitable(order1))
	require.False(t, filter.IsSuitable(order2))

	online = true
	require.True(t, filter.IsSuitable(order1))
	require.True(t, filter.IsSuitable(order2))

	provider := true
	sidecar := true
	filter = NewTraderOnlineFilter(func(_ [33]byte) bool {
		return provider && sidecar
	})
	require.True(t, filter.IsSuitable(order4))

	provider = false
	sidecar = true
	require.False(t, filter.IsSuitable(order4))

	provider = true
	sidecar = false
	require.False(t, filter.IsSuitable(order4))

	provider = false
	sidecar = false
	require.False(t, filter.IsSuitable(order4))
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
	f := NewAccountFilter(fetcher, cutoff, allowedChecker)

	// Make sure that by default we accept the two test orders.
	assert.True(t, f.IsSuitable(node1Ask))
	assert.True(t, f.IsSuitable(node2Bid))

	// Banned accounts shouldn't be matchable.
	acct1banned = true
	assert.False(t, f.IsSuitable(node1Ask))
	acct1banned = false
	acct2banned = true
	assert.False(t, f.IsSuitable(node2Bid))
	acct1banned = false
	acct2banned = false

	// Accounts close to expiry should also not match.
	acct1.Expiry = 144
	assert.False(t, f.IsSuitable(node1Ask))
	acct1.Expiry = 2016

	// Accounts in the wrong state also shouldn't be matched.
	acct1.State = account.StatePendingOpen
	assert.False(t, f.IsSuitable(node1Ask))
}
