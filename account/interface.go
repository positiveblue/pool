package account

import (
	"context"
	"errors"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/client/clmscript"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/keychain"
)

var (
	// ErrNoReservation is an error returned when we attempt to look up a
	// reservation but one does not exist.
	ErrNoReservation = errors.New("no reservation found")
)

// Reservation contains information about the different keys required for to
// create a new account.
type Reservation struct {
	// AuctioneerKey is the base auctioneer's key in the 2-of-2 multi-sig
	// construction of a CLM account. This key will never be included in the
	// account script, but rather it will be tweaked with the per-batch
	// trader key to prevent script reuse and provide plausible deniability
	// between account outputs to third parties.
	AuctioneerKey *keychain.KeyDescriptor

	// InitialBatchKey is the initial batch key that is used to tweak the
	// trader key of an account.
	InitialBatchKey *btcec.PublicKey
}

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

	// StateClosed denotes that an account has been cooperatively closed out
	// on-chain by the trader.
	StateClosed = 3
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

// Parameters are the parameters submitted by a trader for an account.
type Parameters struct {
	// Value is the value of the account reflected in on-chain output that
	// backs the existence of an account.
	Value btcutil.Amount

	// Script is the script of the initial account output that backs the
	// existence of the account.
	Script []byte

	// OutPoint is the outpoint of the initial account output.
	OutPoint wire.OutPoint

	// Expiry is the expiration block height of an account. After this
	// point, the trader is able to withdraw the funds from their account
	// without cooperation of the auctioneer.
	Expiry uint32

	// TraderKey is the base trader's key in the 2-of-2 multi-sig
	// construction of a CLM account.
	TraderKey *btcec.PublicKey
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

	// TraderKey is the base trader's key in the 2-of-2 multi-sig
	// construction of a CLM account. This key will never be included in the
	// account script, but rather it will be tweaked with the per-batch key
	// and the account secret to prevent script reuse and provide plausible
	// deniability between account outputs to third parties.
	TraderKey *btcec.PublicKey

	// AuctioneerKey is the base auctioneer's key in the 2-of-2 multi-sig
	// construction of a CLM account. This key will never be included in the
	// account script, but rather it will be tweaked with the per-batch
	// trader key to prevent script reuse and provide plausible deniability
	// between account outputs to third parties.
	AuctioneerKey *keychain.KeyDescriptor

	// BatchKey is the batch key that is used to tweak the trader key of an
	// account with, along with the secret. This will be incremented by the
	// curve's base point each time the account is modified or participates
	// in a cleared batch to prevent output script reuse for accounts
	// on-chain.
	BatchKey *btcec.PublicKey

	// Secret is a static shared secret between the trader and the
	// auctioneer that is used to tweak the trader key of an account with,
	// along with the batch key. This ensures that only the trader and
	// auctioneer are able to successfully identify every past/future output
	// of an account.
	Secret [32]byte

	// State describes the current state of the account.
	State State

	// HeightHint is the earliest height in the chain at which we can find
	// the account output in a block.
	HeightHint uint32

	// OutPoint the outpoint of the current account output.
	OutPoint wire.OutPoint
}

// Output returns the current on-chain output associated with the account.
func (a *Account) Output() (*wire.TxOut, error) {
	script, err := clmscript.AccountScript(
		a.Expiry, a.TraderKey, a.AuctioneerKey.PubKey, a.BatchKey,
		a.Secret,
	)
	if err != nil {
		return nil, err
	}

	return &wire.TxOut{
		Value:    int64(a.Value),
		PkScript: script,
	}, nil
}

// NextOutputScript returns the next on-chain output script that is to be
// associated with the account. This is done by using the next batch key, which
// results from incrementing the current one by its curve's base point.
func (a *Account) NextOutputScript() ([]byte, error) {
	nextBatchKey := clmscript.IncrementKey(a.BatchKey)
	return clmscript.AccountScript(
		a.Expiry, a.TraderKey, a.AuctioneerKey.PubKey, nextBatchKey,
		a.Secret,
	)
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
	HasReservation(context.Context, lsat.TokenID) (*Reservation, error)

	// ReserveAccount makes a reservation for an auctioneer key for a trader
	// associated to a token.
	ReserveAccount(context.Context, lsat.TokenID, *Reservation) error

	// CompleteReservation completes a reservation for an account. This
	// method should add a record for the account into the store.
	CompleteReservation(context.Context, *Account) error

	// UpdateAccount updates an account in the store according to the given
	// modifiers.
	UpdateAccount(context.Context, *Account, ...Modifier) error

	// Account retrieves the account associated with the given trader key.
	Account(context.Context, *btcec.PublicKey) (*Account, error)

	// Accounts retrieves all existing accounts.
	Accounts(context.Context) ([]*Account, error)

	// BatchKey returns the current per-batch key that must be used to tweak
	// account trader keys with.
	BatchKey(context.Context) (*btcec.PublicKey, error)
}
