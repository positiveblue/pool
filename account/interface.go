package account

import (
	"context"
	"errors"

	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/client/clmscript"
	"github.com/lightninglabs/loop/lsat"
)

var (
	// ErrNoReservation is an error returned when we attempt to look up a
	// reservation but one does not exist.
	ErrNoReservation = errors.New("no reservation found")
)

// State describes the different possible states of an account.
type State uint8

// NOTE: We avoid the use of iota as these can be persisted to disk.
const (
	// StatePendingOpen is the initial state of an account in which we wait
	// for its confirmation.
	StatePendingOpen State = 0

	// StateOpen denotes that the account's funding transaction has been
	// included in the chain with sufficient depth.
	StateOpen State = 1

	// StateExpired denotes that the account's expiration block height has
	// been reached.
	StateExpired State = 2
)

// String returns a human-readable description of an account's state.
func (s State) String() string {
	switch s {
	case StatePendingOpen:
		return "StatePendingOpen"
	case StateOpen:
		return "StateOpen"
	case StateExpired:
		return "StateExpired"
	default:
		return "unknown"
	}
}

// Account encapsulates all of the details of a CLM account on-chain from the
// auctioneer's perspective.
type Account struct {
	// TokenID is the token ID associated with the account.
	TokenID lsat.TokenID

	// Value is the value of the account reflected in on-chain output that
	// backs the existence of an account.
	Value btcutil.Amount

	// Expiry is the expiration block height of an account. After this
	// point, the trader is able to withdraw the funds from their account
	// without cooperation of the auctioneer.
	Expiry uint32

	// TraderKey is the trader's key in the 2-of-2 multi-sig construction of
	// a CLM account.
	TraderKey [33]byte

	// AuctioneerKey is the auctioneer's key in the 2-of-2 multi-sig
	// construction of a CLM account.
	AuctioneerKey [33]byte

	// State describes the current state of the account.
	State State

	// HeightHint is the earliest height in the chain at which we can find
	// the account output in a block.
	HeightHint uint32

	// OutPoint is the identifying component of an account. It is the
	// outpoint of the output used to fund the account.
	OutPoint wire.OutPoint
}

// Output returns the on-chain output associated with the account.
func (a *Account) Output() (*wire.TxOut, error) {
	script, err := clmscript.AccountScript(
		a.Expiry, a.TraderKey, a.AuctioneerKey,
	)
	if err != nil {
		return nil, err
	}
	return &wire.TxOut{
		Value:    int64(a.Value),
		PkScript: script,
	}, nil
}

// Modifier abstracts the modification of an account through a function.
type Modifier func(*Account)

// StateModifier is a functional option that modifies the state of an account.
func StateModifier(state State) Modifier {
	return func(account *Account) {
		account.State = state
	}
}

// Store is responsible for storing and retrieving account information reliably.
type Store interface {
	// HasReservation determines whether we have an existing reservation
	// associated with a token. ErrNoReservation is returned if a
	// reservation does not exist.
	HasReservation(context.Context, lsat.TokenID) ([33]byte, error)

	// ReserveAccount makes a reservation for an auctioneer key for a trader
	// associated to a token.
	ReserveAccount(context.Context, lsat.TokenID, [33]byte) error

	// CompleteReservation completes a reservation for an account. This
	// method should add a record for the account into the store.
	CompleteReservation(context.Context, *Account) error

	// UpdateAccount updates an account in the store according to the given
	// modifiers.
	UpdateAccount(context.Context, *Account, ...Modifier) error

	// Account retrieves the account associated with the given trader key.
	Account(context.Context, [33]byte) (*Account, error)

	// Accounts retrieves all existing accounts.
	Accounts(context.Context) ([]*Account, error)
}
