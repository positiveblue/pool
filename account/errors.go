package account

import (
	"fmt"

	"github.com/btcsuite/btcutil"
)

// ErrBannedAccount is an error returned when a modification is attempted to an
// account which is banned.
type ErrBannedAccount struct {
	expiration uint32
}

// NewErrBannedAccount creates a new ErrBannedAccount error instance.
func NewErrBannedAccount(expiration uint32) ErrBannedAccount {
	return ErrBannedAccount{expiration}
}

// Error returns a string representation of the error.
func (e ErrBannedAccount) Error() string {
	return fmt.Sprintf("account is banned until height %v", e.expiration)
}

// ErrBelowMinAccountValue is an error returned when an account is being
// created/modified with a value below the minimum allowed.
type ErrBelowMinAccountValue struct {
	min btcutil.Amount
}

func newErrBelowMinAccountValue(min btcutil.Amount) ErrBelowMinAccountValue {
	return ErrBelowMinAccountValue{min}
}

func (e ErrBelowMinAccountValue) Error() string {
	return fmt.Sprintf("minimum account value allowed is %v", e.min)
}

// ErrBelowMinAccountValue is an error returned when an account is being
// created/modified with a value below the minimum allowed.
type ErrAboveMaxAccountValue struct {
	max btcutil.Amount
}

func newErrAboveMaxAccountValue(max btcutil.Amount) ErrAboveMaxAccountValue {
	return ErrAboveMaxAccountValue{max}
}

func (e ErrAboveMaxAccountValue) Error() string {
	return fmt.Sprintf("maximum account value allowed is %v", e.max)
}
