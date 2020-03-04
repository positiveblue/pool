package matching

import (
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
)

// AccountID...
type AccountID [33]byte

// Trader...
type Trader struct {
	// AccountKey...
	AccountKey [33]byte

	// AccountExpiry..
	AccountExpiry uint32

	// AccountPoint...
	AccountPoint wire.OutPoint

	// AccountBalance
	AccountBalance btcutil.Amount
}
