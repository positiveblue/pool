package subastadb

import (
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
)

var (
	// ErrAccountNotFound is the error wrapped by AccountNotFoundError that
	// can be used with errors.Is() to avoid type assertions.
	ErrAccountNotFound = errors.New("account not found")

	// ErrAccountDiffAlreadyExists is an error returned when we attempt to
	// store an account diff, but one already exists.
	ErrAccountDiffAlreadyExists = errors.New("found existing account diff")

	// ErrNoBatchExists is returned when a caller attempts to query for a
	// batch by it's ID, yet one isn't found.
	ErrNoBatchExists = errors.New("no batch found")

	// errBatchSnapshotNotFound is an error returned when we can't locate
	// the batch snapshot that was requested.
	//
	// NOTE: The client relies on this exact error for recovery purposes.
	// When modifying it, it should also be updated at the client level. The
	// client cannot import this error since the server code is private.
	errBatchSnapshotNotFound = errors.New("batch snapshot not found")

	// ErrLifetimePackageAlreadyExists is an error returned when we attempt
	// to store a channel lifetime package, but one already exists with the
	// same key.
	ErrLifetimePackageAlreadyExists = errors.New("channel lifetime " +
		"package already exists")

	// ErrNoOrder is the error returned if no order with the given nonce
	// exists in the store.
	ErrNoOrder = errors.New("no order found")

	// ErrOrderExists is returned if an order is submitted that is already
	// known to the store.
	ErrOrderExists = errors.New("order with this nonce already exists")

	// ErrNoTerms is the error that is returned if no trader specific terms
	// can be found in the database.
	ErrNoTerms = errors.New("no trader specific terms found")

	// ErrNotImplemented is the default error for non implemented methods.
	ErrNotImplemented = errors.New("method not implemented")
)

// AccountNotFoundError is returned if a trader tries to subscribe to an account
// that we don't know. This should only happen during recovery where the trader
// goes through a number of their keys to find accounts we still know the state
// of.
type AccountNotFoundError struct {
	// AcctKey is the raw account key of the account we didn't find in our
	// database.
	AcctKey [33]byte
}

// Error implements the error interface.
func (e *AccountNotFoundError) Error() string {
	return fmt.Sprintf("account %x not found", e.AcctKey)
}

// Unwrap returns the underlying error type.
func (e *AccountNotFoundError) Unwrap() error {
	return ErrAccountNotFound
}

// NewAccountNotFoundError creates a new AccountNotFoundError error from an
// account key in the EC public key format.
func NewAccountNotFoundError(acctKey *btcec.PublicKey) *AccountNotFoundError {
	result := &AccountNotFoundError{}
	copy(result.AcctKey[:], acctKey.SerializeCompressed())
	return result
}
