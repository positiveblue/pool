package feebump_test

import (
	"testing"

	"github.com/lightninglabs/subasta/feebump"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// TestCalcEffectiveFeeRates tests that the effective fee rate of a chain of
// unconfirmed transactions are what we expect.
func TestCalcEffectiveFeeRates(t *testing.T) {
	// Create transactions having fee rates of 100, 200 and 400, 800 sat/kw.
	tx100 := &feebump.TxFeeInfo{
		Fee:    100,
		Weight: 1000,
	}
	tx200 := &feebump.TxFeeInfo{
		Fee:    200,
		Weight: 1000,
	}
	tx400 := &feebump.TxFeeInfo{
		Fee:    400,
		Weight: 1000,
	}
	tx800 := &feebump.TxFeeInfo{
		Fee:    800,
		Weight: 1000,
	}

	testCases := []struct {
		txs         []*feebump.TxFeeInfo
		expFeeRates []chainfee.SatPerKWeight
	}{
		{
			// We will have three transcation, all having the same
			// feerate. The effective feerate will therefore be the
			// same.
			txs: []*feebump.TxFeeInfo{
				tx100, tx100, tx100,
			},
			expFeeRates: []chainfee.SatPerKWeight{
				100, 100, 100,
			},
		},
		{
			// Transactions with increasing fee rate. Since
			// the last transaction will bump the fee rate of the
			// previous ones, they will all have effective fee
			// rate (100+200+400+800)/(1+1+1+1) = 375.
			txs: []*feebump.TxFeeInfo{
				tx100, tx200, tx400, tx800,
			},
			expFeeRates: []chainfee.SatPerKWeight{
				375, 375, 375, 375,
			},
		},
		{
			// Transactions with decresing fee rates. Since the
			// next transcation won't bump previous ones, they will
			// have effective fee rates equal to their original fee
			// rate.
			txs: []*feebump.TxFeeInfo{
				tx800, tx400, tx200, tx100,
			},
			expFeeRates: []chainfee.SatPerKWeight{
				800, 400, 200, 100,
			},
		},
		{
			// Transactions with increasing then decreasing fee
			// rates. The first transactions will be bumped by the
			// following one, but when the fee rate decreases those
			// transactions will have an effective fee rate equal
			// to their original fee rate.
			txs: []*feebump.TxFeeInfo{
				tx100, tx200, tx400, tx800, tx800, tx400, tx200,
				tx100,
			},
			expFeeRates: []chainfee.SatPerKWeight{
				460, 460, 460, 460, 460, 400, 200, 100,
			},
		},
		{
			// Transactions with increasing fee rates, then a final
			// transaction with a fee rate that is slightly _more_
			// than the package fee rate up to that point. This
			// means that the final transaction will bump the
			// previous ones slightly.
			txs: []*feebump.TxFeeInfo{
				tx100, tx200, tx400, tx800, tx400,
			},
			expFeeRates: []chainfee.SatPerKWeight{
				380, 380, 380, 380, 380,
			},
		},
		{
			// Transactions with increasing fee rates, then a final
			// transaction with a fee rate that is slightly _less_
			// than the package fee rate up to that point. This
			// means that the final transaction will NOT bump the
			// previous ones, and just get an effective fee rate
			// equal to its original one.
			txs: []*feebump.TxFeeInfo{
				tx100, tx200, tx400, tx800, tx200,
			},
			expFeeRates: []chainfee.SatPerKWeight{
				375, 375, 375, 375, 200,
			},
		},
		{
			// Transactions with decreasing then increasing fee
			// rates. The first two transactions will have
			// effective fee rates equal to their original fee
			// rate, but the third will be bumped by the following
			// transactions, since the total package will have a
			// fee rate of (800+400+200+100+200+400)/6 = 350.
			txs: []*feebump.TxFeeInfo{
				tx800, tx400, tx200, tx100, tx200, tx400,
			},
			expFeeRates: []chainfee.SatPerKWeight{
				800, 400, 350, 350, 350, 350,
			},
		},
	}

	for _, testCase := range testCases {
		feeRates := feebump.CalcEffectiveFeeRates(testCase.txs)

		for i := range feeRates {
			if feeRates[i] != testCase.expFeeRates[i] {
				t.Fatalf("expected tx %v to have eff fee "+
					"rate %v had %v", i,
					testCase.expFeeRates[i], feeRates[i])
			}
		}
	}
}
