package venue

import (
	"fmt"

	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/agora/venue/matching"
)

// ErrMissingTraders is returned if when we attempt to start a batch, some or
// all of the traders in the batch are not online.
type ErrMissingTraders struct {
	// TraderKeys is the set of traders who aren't online.
	TraderKeys map[matching.AccountID]struct{}

	// OrderNonces is the set of orders that can't be executed as one or
	// both sides of the order is not online.
	OrderNonces map[orderT.Nonce]struct{}
}

// Error implements the error interface.
func (e *ErrMissingTraders) Error() string {
	return fmt.Sprintf("%v traders not online in venue: %v",
		len(e.TraderKeys), e.TraderKeys)
}

// ErrMsgTimeout is returned if a trader doesn't send an expected response in
// time.
type ErrMsgTimeout struct {
	// Trader is the trader that failed to send the response.
	Trader matching.AccountID

	// Msg is the message they didn't respond ti.
	Msg string

	// OrderNonces is the set of orders that the trader was involved in.
	OrderNonces []orderT.Nonce
}

// Error implements the error interface.
func (e *ErrMsgTimeout) Error() string {
	return fmt.Sprintf("trader %x failed to send %v",
		e.Trader[:], e.Msg)
}

// ErrInvalidWitness is returned if a trader sends an invalid signature for
// their account input.
type ErrInvalidWitness struct {
	// VerifyErr is the error returned from the Script VM instance.
	VerifyErr error

	// Trader is the trader that sent the invalid signature.
	Trader matching.AccountID

	// OrderNonces is the set of orders that the trader was involved in.
	OrderNonces []orderT.Nonce
}

// Error implements the error interface.
func (e *ErrInvalidWitness) Error() string {
	return fmt.Sprintf("trader %x sent invalid witness: %v", e.Trader[:],
		e.VerifyErr)
}

// Unwrap returns the base error.
func (e *ErrInvalidWitness) Unwrap() error {
	return e.VerifyErr
}
