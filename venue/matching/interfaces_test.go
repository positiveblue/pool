package matching

import (
	"testing"
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/stretchr/testify/require"
)

var (
	orders = []MatchedOrder{{
		Details: OrderPair{
			Ask: node1Ask,
			Bid: node2Bid,
			Quote: PriceQuote{
				UnitsMatched:     2,
				TotalSatsCleared: 2,
			},
		},
		Asker: Trader{
			AccountKey: AccountID{1, 2, 3},
		},
		Bidder: Trader{
			AccountKey: AccountID{2, 3, 4},
		},
	}}
	feeReport = TradingFeeReport{
		AccountDiffs: map[AccountID]AccountDiff{
			orders[0].Asker.AccountKey: {
				StartingState: &orders[0].Asker,
				AccountTally: &orderT.AccountTally{
					EndingBalance: 600_000,
				},
			},
			orders[0].Bidder.AccountKey: {
				StartingState: &orders[0].Bidder,
				AccountTally: &orderT.AccountTally{
					EndingBalance: 500,
				},
			},
		},
	}
)

// TestOrderBatchCopy makes sure the copy method copies all fields correctly.
func TestOrderBatchCopy(t *testing.T) {
	orderBatch := &OrderBatch{
		Version: 99,
		Orders:  orders,
		SubBatches: map[uint32][]MatchedOrder{
			orderT.LegacyLeaseDurationBucket: orders,
		},
		FeeReport:         feeReport,
		CreationTimestamp: time.Unix(123_456_789, 0),
		ClearingPrices: map[uint32]orderT.FixedRatePremium{
			orderT.LegacyLeaseDurationBucket: 1234,
		},
	}
	batchCopy := orderBatch.Copy()

	require.Equal(t, orderBatch, &batchCopy)
}
