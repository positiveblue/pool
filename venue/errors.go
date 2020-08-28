package venue

import (
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/davecgh/go-spew/spew"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/venue/matching"
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
		len(e.TraderKeys), spew.Sdump(e.TraderKeys))
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

// ErrMissingChannelInfo is returned if a trader sends an OrderMatchSign message
// without including the accompanying channel info.
type ErrMissingChannelInfo struct {
	// Trader is the trader that did not provide the required channel info.
	Trader matching.AccountID

	// ChannelPoint is the identifying outpoint of the channel.
	ChannelPoint wire.OutPoint

	// OrderNonces is the set of orders that the trader was involved in.
	OrderNonces []orderT.Nonce
}

// Error implements the error interface.
func (e *ErrMissingChannelInfo) Error() string {
	return fmt.Sprintf("trader %x did not provide information for channel "+
		"%v", e.Trader[:], e.ChannelPoint)
}

// ErrNonMatchingChannelInfo is an error returned by the venue during batch
// execution when two matched traders submit non-matching information regarding
// their to be created channel.
type ErrNonMatchingChannelInfo struct {
	// Err is the underlying cause of the error.
	Err error

	// ChannelPoint is the identifying outpoint of the channel.
	ChannelPoint wire.OutPoint

	// Trader1 is one of the matched traders that will be punished.
	Trader1 matching.AccountID

	// Trader2 is the other matched trader that will be punished.
	Trader2 matching.AccountID

	// OrderNonces contains the order nonces for both traders above which
	// will be removed from the next batch attempt due to the error.
	OrderNonces []orderT.Nonce
}

// Error implements the error interface.
func (e *ErrNonMatchingChannelInfo) Error() string {
	return fmt.Sprintf("non-matching info between %x and %x for channel "+
		"%v: %v", e.Trader1[:], e.Trader2[:], e.ChannelPoint, e.Err)
}

// Unwrap returns the base error.
func (e *ErrNonMatchingChannelInfo) Unwrap() error {
	return e.Err
}
