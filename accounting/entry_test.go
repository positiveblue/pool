package accounting

import (
	"fmt"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/faraday/fiat"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

var (
	node1Key = matching.NodeID{1, 2, 3, 4}
	acct1Key = [33]byte{1, 2, 3, 4}
	node2Key = matching.NodeID{2, 3, 4, 5}
	acct2Key = [33]byte{2, 3, 4, 5}

	node1Ask = &order.Ask{
		Ask: orderT.Ask{
			Kit: orderT.Kit{
				AcctKey: acct1Key,
			},
		},
		Kit: order.Kit{
			NodeKey: node1Key,
		},
	}
	node2Bid = &order.Bid{
		Bid: orderT.Bid{
			Kit: orderT.Kit{
				AcctKey: acct2Key,
			},
		},
		Kit: order.Kit{
			NodeKey: node2Key,
		},
		IsSidecar: true,
	}
	batchID = [33]byte{1, 2, 3, 4}
	//batchSnapshot = &subastadb.BatchSnapshot{}
)

// newOrders return a copy of the matcher oders used for testing.
func newOrders() []matching.MatchedOrder {
	return []matching.MatchedOrder{{
		Details: matching.OrderPair{
			Ask: node1Ask,
			Bid: node2Bid,
			Quote: matching.PriceQuote{
				UnitsMatched:     2,
				TotalSatsCleared: 200_000,
			},
		},
		Asker: matching.Trader{
			AccountKey:     matching.AccountID{1, 2, 3},
			AccountBalance: 800_200,
		},
		Bidder: matching.Trader{
			AccountBalance: 600,
			AccountKey:     matching.AccountID{2, 3, 4},
		},
	}}
}

// newFeeReport return a copy of the TradingFeeReport used for testing.
func newFeeReport(orders []matching.MatchedOrder) matching.TradingFeeReport {
	return matching.TradingFeeReport{
		AccountDiffs: map[matching.AccountID]*matching.AccountDiff{
			orders[0].Asker.AccountKey: {
				StartingState: &orders[0].Asker,
				AccountTally: &orderT.AccountTally{
					TotalExecutionFeesPaid: 200,
					TotalMakerFeesAccrued:  100,
					EndingBalance:          600_000,
				},
			},
			orders[0].Bidder.AccountKey: {
				StartingState: &orders[0].Bidder,
				AccountTally: &orderT.AccountTally{
					TotalExecutionFeesPaid: 100,
					TotalMakerFeesAccrued:  200,
					EndingBalance:          300,
				},
			},
		},
	}
}

// newOrderBatch return a copy of the order batch used for testing.
func newOrderBatch() *matching.OrderBatch {
	orders := newOrders()
	return &matching.OrderBatch{
		Version:           99,
		Orders:            orders,
		FeeReport:         newFeeReport(orders),
		CreationTimestamp: time.Unix(123_456_789, 0),
		ClearingPrices: map[uint32]orderT.FixedRatePremium{
			orderT.LegacyLeaseDurationBucket: 1234,
		},
	}
}

var extractEntryTestCases = []struct {
	name          string
	cfg           *Config
	batchID       orderT.BatchID
	batch         *subastadb.BatchSnapshot
	expectedEntry *BatchEntry
	expectedErr   string
}{{
	name:    "extractEntry fails if we cannot get the fiat price",
	batchID: batchID,
	batch: &subastadb.BatchSnapshot{
		BatchTx:    &wire.MsgTx{},
		OrderBatch: newOrderBatch(),
	},
	cfg: &Config{
		GetPrice: func(_ time.Time) (*fiat.Price, error) {
			return nil, fmt.Errorf("expectedErr")
		},
	},
	expectedErr: "expectedErr",
}, {
	name:    "extractEntry is populated correctly",
	batchID: batchID,
	batch: &subastadb.BatchSnapshot{
		BatchTx:    &wire.MsgTx{},
		OrderBatch: newOrderBatch(),
	},
	cfg: &Config{
		GetPrice: func(_ time.Time) (*fiat.Price, error) {
			return &fiat.Price{
				Timestamp: time.Unix(123_456_789, 0),
				Price:     decimal.New(10_000, 0),
				Currency:  "BTC",
			}, nil
		},
	},
	expectedEntry: &BatchEntry{
		BatchID:         batchID,
		Timestamp:       time.Unix(123_456_789, 0),
		TxID:            (&wire.MsgTx{}).TxHash().String(),
		TraderChainFees: 500,
		ProfitInSats:    500,
		// ProfitInUSD needs to be specified like this because of
		// how assert.Equal works. decimal.NewFromFloat(0.05) is
		// the same value but the deep equal would fail.
		ProfitInUSD: decimal.New(500000000000000, -16), // 0.05 USD
		BTCPrice: &fiat.Price{
			Timestamp: time.Unix(123_456_789, 0),
			Price:     decimal.New(10_000, 0),
			Currency:  "BTC",
		},
	},
	expectedErr: "",
}}

func TestExtractEntry(t *testing.T) {
	for _, tc := range extractEntryTestCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			entry, err := extractBatchEntry(
				tc.cfg, tc.batchID, tc.batch,
			)
			if tc.expectedErr != "" {
				assert.EqualError(t, err, tc.expectedErr)
				return
			}
			assert.NoError(t, err)

			assert.Equal(t, tc.expectedEntry, entry)
		})
	}
}

var getTraderFeesTestCases = []struct {
	name         string
	orderBatch   *matching.OrderBatch
	expectedFees btcutil.Amount
}{{
	name:         "trader fees are 0 for empty batches",
	orderBatch:   &matching.OrderBatch{},
	expectedFees: 0,
}, {
	name:         "calculate trader fees from a batch with matched orders",
	orderBatch:   newOrderBatch(),
	expectedFees: 500,
}}

// TestGetTraderFees checks that we are able to extract the total fees paid
// by the traders
func TestGetTraderFees(t *testing.T) {
	for _, tc := range getTraderFeesTestCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			fees := getTraderChainFees(tc.orderBatch)
			assert.Equal(t, fees, tc.expectedFees)
		})
	}
}
