package account

import (
	"context"
	"errors"
	"math/big"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/llm/clmscript"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/keychain"
)

var (
	// ErrNoReservation is an error returned when we attempt to look up a
	// reservation but one does not exist.
	ErrNoReservation = errors.New("no reservation found")

	// ErrNoDiff is an error returned when we attempt to retrieve a staged
	// diff for an account but it is not found.
	ErrNoDiff = errors.New("no account diff found")

	// ErrNoAuctioneerAccount is returned when a caller attempts to fetch
	// the auctioneer account, but it hasn't been initialized yet.
	ErrNoAuctioneerAccount = errors.New("no auctioneer account")
)

// Reservation contains information about the different keys required for to
// create a new account.
type Reservation struct {
	// Value is the value of the account reflected in the on-chain output
	// that backs the existence of an account.
	Value btcutil.Amount

	// AuctioneerKey is the base auctioneer's key in the 2-of-2 multi-sig
	// construction of a CLM account. This key will never be included in the
	// account script, but rather it will be tweaked with the per-batch
	// trader key to prevent script reuse and provide plausible deniability
	// between account outputs to third parties.
	AuctioneerKey *keychain.KeyDescriptor

	// InitialBatchKey is the initial batch key that is used to tweak the
	// trader key of an account.
	InitialBatchKey *btcec.PublicKey

	// Expiry is the expiration block height of an account. After this
	// point, the trader is able to withdraw the funds from their account
	// without cooperation of the auctioneer.
	Expiry uint32

	// HeightHint is the block height of the auctioneer at the time of the
	// reservation. The trader shouldn't have published the transaction yet
	// but we record the hint in case a recovery from a reservation becomes
	// necessary.
	HeightHint uint32

	// TraderKeyRaw is the base trader's key in the 2-of-2 multi-sig
	// construction of a CLM account. We store it in the reservation already
	// in case the trader crashes or loses data just after publishing the
	// opening transaction but before confirming it with the auctioneer. In
	// that case they can still recover, if the transaction is ever
	// confirmed.
	TraderKeyRaw [33]byte
}

// State describes the different possible states of an account.
type State uint8

// NOTE: We avoid the use of iota as these can be persisted to disk.
const (
	// StatePendingOpen is the initial state of an account in which we wait
	// for its confirmation.
	StatePendingOpen State = 0

	// StatePendingUpdate denotes that the account has undergone an update
	// on-chain either as part of a matched order or a trader modification
	// and we are currently waiting for its confirmation.
	StatePendingUpdate State = 1

	// StateOpen denotes that the account's funding transaction has been
	// included in the chain with sufficient depth.
	StateOpen State = 2

	// StateExpired denotes that the account's expiration block height has
	// been reached.
	StateExpired State = 3

	// StateClosed denotes that an account has been cooperatively closed out
	// on-chain by the trader.
	StateClosed State = 4
)

// String returns a human-readable description of an account's state.
func (s State) String() string {
	switch s {
	case StatePendingOpen:
		return "StatePendingOpen"
	case StatePendingUpdate:
		return "StatePendingUpdate"
	case StateOpen:
		return "StateOpen"
	case StateExpired:
		return "StateExpired"
	case StateClosed:
		return "StateClosed"
	default:
		return "unknown"
	}
}

// OnChainState describes the different possible states an account can have
// regarding its on-chain state.
type OnChainState uint8

const (
	// OnChainStateRecreated denotes that the leftover account balance was
	// large enough for a new account output to be created on-chain.
	OnChainStateRecreated OnChainState = iota

	// OnChainStateFullySpent denotes that the leftover account balance was
	// considered dust and no new account output has been created on-chain.
	// The dust balance has either been credited to the user through an
	// off-chain mechanism or has been fully consumed as chain fees.
	OnChainStateFullySpent
)

// Parameters are the parameters submitted by a trader for an account.
type Parameters struct {
	// Value is the value of the account reflected in the on-chain output
	// that backs the existence of an account.
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

	// TraderKeyRaw is the base trader's key in the 2-of-2 multi-sig
	// construction of a CLM account. This key will never be included in the
	// account script, but rather it will be tweaked with the per-batch key
	// and the account secret to prevent script reuse and provide plausible
	// deniability between account outputs to third parties.
	TraderKeyRaw [33]byte

	// traderKey is the fully materialized version of TraderKeyRaw. We use
	// a private variable to memoize this value so we only need to compute
	// it once lazily.
	traderKey *btcec.PublicKey

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

	// CloseTx is the closing transaction of an account. This will only be
	// populated if the account is in any of the following states:
	//
	//	- StateClosed
	CloseTx *wire.MsgTx
}

// Output returns the current on-chain output associated with the account.
func (a *Account) Output() (*wire.TxOut, error) {
	traderKey, err := a.TraderKey()
	if err != nil {
		return nil, err
	}

	script, err := clmscript.AccountScript(
		a.Expiry, traderKey, a.AuctioneerKey.PubKey, a.BatchKey,
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

// TraderKey returns the base trader key that is used to derive the set of keys
// that appear in the multi-sig portion of an account script.
func (a *Account) TraderKey() (*btcec.PublicKey, error) {
	if a.traderKey != nil {
		return a.traderKey, nil
	}

	return btcec.ParsePubKey(a.TraderKeyRaw[:], btcec.S256())
}

// NextOutputScript returns the next on-chain output script that is to be
// associated with the account. This is done by using the next batch key, which
// results from incrementing the current one by its curve's base point.
func (a *Account) NextOutputScript() ([]byte, error) {
	traderKey, err := a.TraderKey()
	if err != nil {
		return nil, err
	}

	nextBatchKey := clmscript.IncrementKey(a.BatchKey)
	return clmscript.AccountScript(
		a.Expiry, traderKey, a.AuctioneerKey.PubKey, nextBatchKey,
		a.Secret,
	)
}

// Copy returns a deep copy of the account with the given modifiers applied.
func (a *Account) Copy(modifiers ...Modifier) *Account {
	accountCopy := &Account{
		TokenID:      a.TokenID,
		Value:        a.Value,
		Expiry:       a.Expiry,
		TraderKeyRaw: a.TraderKeyRaw,
		AuctioneerKey: &keychain.KeyDescriptor{
			KeyLocator: a.AuctioneerKey.KeyLocator,
			PubKey: &btcec.PublicKey{
				X:     big.NewInt(0).Set(a.AuctioneerKey.PubKey.X),
				Y:     big.NewInt(0).Set(a.AuctioneerKey.PubKey.Y),
				Curve: a.AuctioneerKey.PubKey.Curve,
			},
		},
		BatchKey: &btcec.PublicKey{
			X:     big.NewInt(0).Set(a.BatchKey.X),
			Y:     big.NewInt(0).Set(a.BatchKey.Y),
			Curve: a.BatchKey.Curve,
		},
		Secret:     a.Secret,
		State:      a.State,
		HeightHint: a.HeightHint,
		OutPoint:   a.OutPoint,
	}

	for _, modifier := range modifiers {
		modifier(accountCopy)
	}

	return accountCopy
}

// Modifier abstracts the modification of an account through a function.
type Modifier func(*Account)

// StateModifier is a functional option that modifies the state of an account.
func StateModifier(state State) Modifier {
	return func(account *Account) {
		account.State = state
	}
}

// ValueModifier is a functional option that modifies the value of an account.
func ValueModifier(value btcutil.Amount) Modifier {
	return func(account *Account) {
		account.Value = value
	}
}

// ExpiryModifier is a functional option that modifies the expiry of an account.
func ExpiryModifier(expiry uint32) Modifier {
	return func(account *Account) {
		account.Expiry = expiry
	}
}

// IncrementBatchKey is a functional option that increments the batch key of an
// account by adding the curve's base point.
func IncrementBatchKey() Modifier {
	return func(account *Account) {
		account.BatchKey = clmscript.IncrementKey(account.BatchKey)
	}
}

// OutPointModifier is a functional option that modifies the out point of an
// account.
func OutPointModifier(outPoint wire.OutPoint) Modifier {
	return func(account *Account) {
		account.OutPoint = outPoint
	}
}

// CloseTxModifier is a functional option that modifies the closing transaction
// of an account.
func CloseTxModifier(tx *wire.MsgTx) Modifier {
	return func(account *Account) {
		account.CloseTx = tx
	}
}

// Store is responsible for storing and retrieving account information reliably.
type Store interface {
	// FetchAuctioneerAccount retrieves the current information pertaining
	// to the current auctioneer output state.
	FetchAuctioneerAccount(context.Context) (*Auctioneer, error)

	// HasReservation determines whether we have an existing reservation
	// associated with a token. ErrNoReservation is returned if a
	// reservation does not exist.
	HasReservation(context.Context, lsat.TokenID) (*Reservation, error)

	// HasReservationForKey determines whether we have an existing
	// reservation associated with a trader key. ErrNoReservation is
	// returned if a reservation does not exist.
	HasReservationForKey(context.Context, *btcec.PublicKey) (*Reservation,
		*lsat.TokenID, error)

	// ReserveAccount makes a reservation for an auctioneer key for a trader
	// associated to a token.
	ReserveAccount(context.Context, lsat.TokenID, *Reservation) error

	// CompleteReservation completes a reservation for an account. This
	// method should add a record for the account into the store.
	CompleteReservation(context.Context, *Account) error

	// UpdateAccount updates an account in the store according to the given
	// modifiers.
	UpdateAccount(context.Context, *Account, ...Modifier) error

	// StoreAccountDiff stores a pending set of updates that should be
	// applied to an account after an invocation of CommitAccountDiff.
	//
	// In contrast to UpdateAccount, this should be used whenever we need to
	// stage a pending update of the account that will be committed at some
	// later point.
	StoreAccountDiff(context.Context, *btcec.PublicKey, []Modifier) error

	// CommitAccountDiff commits the stored pending set of updates for an
	// account after a successful modification. If a diff does not exist,
	// account.ErrNoDiff is returned.
	CommitAccountDiff(context.Context, *btcec.PublicKey) error

	// Account retrieves the account associated with the given trader key.
	// The boolean indicates whether the account's diff should be returned
	// instead. If a diff does not exist, then the existing account state is
	// returned.
	Account(context.Context, *btcec.PublicKey, bool) (*Account, error)

	// Accounts retrieves all existing accounts.
	Accounts(context.Context) ([]*Account, error)

	// BatchKey returns the current per-batch key that must be used to tweak
	// account trader keys with.
	BatchKey(context.Context) (*btcec.PublicKey, error)

	// IsAccountBanned determines whether the given account is banned at the
	// current height. The ban's expiration height is returned.
	IsAccountBanned(context.Context, *btcec.PublicKey, uint32) (bool,
		uint32, error)
}

// EndingState determines the new on-chain state for an account with the given
// ending balance after a batch execution.
func EndingState(endingBalance btcutil.Amount) OnChainState {
	if endingBalance < orderT.MinNoDustAccountSize {
		return OnChainStateFullySpent
	}
	return OnChainStateRecreated
}
