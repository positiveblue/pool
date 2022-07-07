package accounting

import (
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightninglabs/faraday/fiat"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// safeDecimal parses a string and returns a new decimal.Decimal without
// checking its value.
func safeDecimal(n string) decimal.Decimal {
	v, _ := decimal.NewFromString(n)
	return v
}

/// newBatchEntry returns a new batch entry to test the report summary.
func newBatchEntry(aFees, cFees, tFees int64, btcPrice string) *BatchEntry {
	return &BatchEntry{
		AccruedFees:     btcutil.Amount(aFees),
		BatchTxFees:     btcutil.Amount(cFees),
		TraderChainFees: btcutil.Amount(tFees),
		BTCPrice: &fiat.Price{
			Price: safeDecimal(btcPrice),
		},
	}
}

/// newLSATEntry returns a new lsat entry to test the report summary.
func newLSATEntry(sats int64, usd string) *LSATEntry {
	return &LSATEntry{
		ProfitInSats: btcutil.Amount(sats),
		ProfitInUSD:  safeDecimal(usd),
	}
}

var calculateAccountRevenueTestCases = []struct {
	name                    string
	report                  Report
	expectedBatchFees       btcutil.Amount
	expectedBatchFeesInUSD  decimal.Decimal
	expectedLSAT            btcutil.Amount
	expectedLSATInUSD       decimal.Decimal
	expectedChainFees       btcutil.Amount
	expectedChainFeesInUSD  decimal.Decimal
	expectedNetRevenue      btcutil.Amount
	expectedNetRevenueInUSD decimal.Decimal
}{{
	name: "calculate account revenue",
	report: Report{
		BatchEntries: []*BatchEntry{
			newBatchEntry(10_000, 1100, 432, "20000"),
			newBatchEntry(20_000, 2200, -130, "60000"),
		},
		LSATEntries: []*LSATEntry{
			newLSATEntry(1000, "0.01"),
			newLSATEntry(0, "0"),
			newLSATEntry(1200, "0.01"),
		},
	},
	expectedBatchFees:       30000,
	expectedBatchFeesInUSD:  safeDecimal("14"),
	expectedLSAT:            2200,
	expectedLSATInUSD:       safeDecimal("0.02"),
	expectedChainFees:       2998,
	expectedChainFeesInUSD:  safeDecimal("1.5316"),
	expectedNetRevenue:      29202,
	expectedNetRevenueInUSD: safeDecimal("12.4884"),
}}

func TestCalculateNetRevenue(t *testing.T) {
	for _, tc := range calculateAccountRevenueTestCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.report.CalculateNetRevenue()

			summary := tc.report.Summary
			require.Equal(
				t, tc.expectedBatchFees,
				summary.LeaseBatchFees,
			)
			require.True(
				t, summary.LeaseBatchFeesInUSD.Equal(
					tc.expectedBatchFeesInUSD,
				),
			)
			require.Equal(t, tc.expectedLSAT, summary.LSAT)
			require.True(
				t, summary.LSATInUSD.Equal(
					tc.expectedLSATInUSD,
				),
			)
			require.Equal(
				t, tc.expectedChainFees, summary.ChainFees,
			)
			require.True(
				t, summary.ChainFeesInUSD.Equal(
					tc.expectedChainFeesInUSD,
				),
			)
			require.Equal(
				t, summary.NetRevenue, tc.expectedNetRevenue,
			)
			require.True(
				t, summary.NetRevenueInUSD.Equal(
					tc.expectedNetRevenueInUSD,
				),
			)
		})
	}
}
