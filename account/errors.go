package account

import (
	"fmt"

	"github.com/btcsuite/btcutil"
)

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
