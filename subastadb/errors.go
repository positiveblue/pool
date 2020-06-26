package subastadb

import (
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec"
)

var (
	// ErrAccountNotFound is the error wrapped by AccountNotFoundError that
	// can be used with errors.Is() to avoid type assertions.
	ErrAccountNotFound = errors.New("account not found")
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
