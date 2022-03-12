package feebump

import (
	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// TxFeeInfo is a struct representing holding a transaction's weight and fee.
// We use it to effectively look up the fee rate of unconfirmed batches.
type TxFeeInfo struct {
	// Fee is the total chain fee paid by this single transaction.
	Fee btcutil.Amount

	// Weight is the weight of the final transaction, (or the weight
	// estimate for unsigned transactions).
	Weight int64
}

// FeeRate returns the fee rate of the transaction.
func (u *TxFeeInfo) FeeRate() chainfee.SatPerKWeight {
	return FeeRate(u.Fee, u.Weight)
}

// feeRate calculates the fee rate given a total fee and weight.
func FeeRate(fee btcutil.Amount, w int64) chainfee.SatPerKWeight {
	return chainfee.SatPerKWeight(int64(fee) * 1000 / w)
}

// CalcEffectiveFeeRates takes the fee info for a chain of unconfirmed
// transactions and calculates the effective fee rate for each. The effective
// fee rate of a tx is the maximum fee rate of any valid transaction package
// containing that transaction.
func CalcEffectiveFeeRates(txChain []*TxFeeInfo) []chainfee.SatPerKWeight {
	var (
		totalWeight int64
		totalFees   btcutil.Amount
		pkgFeeRates = make([]chainfee.SatPerKWeight, len(txChain))
		feeRates    = make([]chainfee.SatPerKWeight, len(txChain))
	)

	// For each tx i, find the package fee rate of txs [0,i].
	for i := range txChain {
		tx := txChain[i]
		totalWeight += tx.Weight
		totalFees += tx.Fee

		// The weight and fees up until now determines the fee of the
		// package.
		pkgFeeRates[i] = FeeRate(totalFees, totalWeight)
	}

	// The effective fee rate for transaction i will either be the rate for
	// the package up to i, or if the tx itself has a fee rate lower than
	// the package, that will be its fee rate (since parents can't bump a
	// child's fee).
	for i := range txChain {
		txFeeRate := txChain[i].FeeRate()
		pkgFeeRate := pkgFeeRates[i]

		feeRates[i] = txFeeRate
		if pkgFeeRate < txFeeRate {
			feeRates[i] = pkgFeeRate
		}
	}

	// Now go backwards and find the "rational" fee rate of each tx. A miner
	// seeking to maximum fee rate will either confirm the package up to
	// tx i, or also confirm tx i+1.
	// TODO(halseth): this is not always true if the transactions gets
	// huge, so they all won't fit in a block.
	for i := len(feeRates) - 2; i >= 0; i-- {
		a := feeRates[i]
		b := feeRates[i+1]
		if a < b {
			feeRates[i] = b
		}
	}

	return feeRates
}
