package matching

import (
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/client/clmscript"
)

// AccountID is the account ID that uniquely identifies a trader.
type AccountID [33]byte

// Trader is a snapshot of a trader's state at a given point in time. We'll use
// this to generate account diffs, and compute metric groups such as trading
// fees paid.
type Trader struct {
	// AccountKey is the account key of a trader.
	AccountKey AccountID

	// NextBatchKey is the NEXT batch key of the trader, this will be used
	// to generate all the scripts we need for the trader's outputs in the
	// batch execution transaction.
	NextBatchKey [33]byte

	// VenueSecret is a shared secret that the venue shares with the
	// trader.
	VenueSecret [32]byte

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

// NewTraderFromAccount creates a new trader instance from a given account.
func NewTraderFromAccount(acct *account.Account) Trader {
	t := Trader{
		AccountKey:      acct.TraderKeyRaw,
		AccountExpiry:   acct.Expiry,
		AccountOutPoint: acct.OutPoint,
		AccountBalance:  acct.Value,
		VenueSecret:     acct.Secret,
	}

	if acct.BatchKey != nil {
		nextBatchKey := clmscript.IncrementKey(acct.BatchKey)
		copy(t.NextBatchKey[:], nextBatchKey.SerializeCompressed())
	}

	return t
}

// TODO(roasbeef): methods to update state given execution context?
// * auctioneer calls this after each batch, calls with info from disk
