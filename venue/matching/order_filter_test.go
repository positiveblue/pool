package matching

import (
	"github.com/lightninglabs/subasta/order"
	"testing"

	orderT "github.com/lightninglabs/pool/order"
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
