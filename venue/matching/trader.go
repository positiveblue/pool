package matching

import (
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
)

// AccountID is the account ID that uniquely identifies a trader.
type AccountID [33]byte

// Trader is a snapshot of a trader's state at a given point in time. We'll use
// this to generate account diffs, and compute metric groups such as trading
// fees paid.
type Trader struct {
	// AccountKey is the account key of a trader.
	AccountKey AccountID

	// AccountExpiry is the absolute block height that this account expires
	// after.
	AccountExpiry uint32

	// AccountOutPoint is the account point that will be used to generate the
	// batch execution transaction for this trader to re-spend their
	// account.
	AccountOutPoint wire.OutPoint

	// AccountBalance is the current account balance of this trader. All
	// trading fees and chain fees will be extracted from this value.
	AccountBalance btcutil.Amount
}
