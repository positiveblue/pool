package feebump

import (
	"github.com/btcsuite/btcutil"
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
