package subasta

import (
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/venue/batchtx"
)

// AuctionState is an enum-like interface that describes the current phase of
// the auction from the PoV of either the server or the client.
type AuctionState interface {
	// String returns the name of the state.
	String() string
}

// DefaultState is the default state of the auction. This auction will always
// start in this state, but then possibly skip to a later state upon auction
// opening.
type DefaultState struct{}

// String returns the string representation of the DefaultState.
func (s DefaultState) String() string {
	return "DefaultState"
}

// NoMasterAcctState indicates that no master account exists in the database.
//
// The possible transitions from this state are:
//     * NoMasterAcctState -> MasterAcctPending
type NoMasterAcctState struct{}

// String returns the string representation of the NoMasterAcctState.
func (s NoMasterAcctState) String() string {
	return "NoMasterAcctState"
}

// MasterAcctPending indicates that the transaction to broadcast the master
// account is now in the mempool, and we're only waiting for a confirmation.
//
// The possible transitions from this state are:
//     * MasterAcctPending -> MasterAcctConfirmed
type MasterAcctPending struct{}

// String returns the string representation of the MasterAcctPending state.
func (s MasterAcctPending) String() string {
	return "MasterAcctPending"
}

// MasterAcctConfirmed is the state we transition to once the genesis
// transaction for the master account has been confirmed.
//
// The possible transitions from this state are:
//     * MasterAcctConfirmed -> OrderSubmitState
type MasterAcctConfirmed struct{}

// String returns the string representation of the MasterAcctConfirmed state.
func (s MasterAcctConfirmed) String() string {
	return "MasterAcctConfirmed"
}

// OrderSubmitState is the state the client is in once they have an active and
// valid account. During this phase the client is free to submit new orders and
// modify any existing orders.
//
// The possible transitions from this state are:
//     * OrderSubmitState -> OrderSubmitState (tick but no market)
//     * OrderSubmitState -> MatchMakingState (tick and market)
type OrderSubmitState struct{}

// String returns the string representation of the OrderSubmitState.
func (s OrderSubmitState) String() string {
	return "OrderSubmitState"
}

// MatchMakingState is the state we enter into when we're attempting to make
// anew market. From this state, we'll either go to execute the market, or
// possibly go back to the OrderSubmitState if there're no orders that actually
// make a market.
//
// The possible transitions from this state are:
//     * MatchMakingState -> OrderSubmitState (fail to make market)
//     * MatchMakingState -> BatchExecutionState (market made)
type MatchMakingState struct {
}

// String returns the string representation of the MatchMakingState.
func (s MatchMakingState) String() string {
	return "MatchMakingState"
}

// BatchExecutionState is the phase the auction enters once the
// MatchMakingState has ended, and the auctioneer is able to make a market.
// During this phase, the auctioneer enters into an interactive protocol with
// each active trader which is a part of this batch to sign relevant inputs for
// the funding transaction, and also to carry out the normal funding flow
// process so they receive valid commitment transactions.
//
// The possible transitions from this state are:
//     * BatchClearingState -> MatchMakingState (execution fail)
//     * BatchClearingState -> BatchClearingState
type BatchExecutionState struct {
	// exeCtx is the execution context for the batch to execute, including
	// the assembled batch transaction.
	exeCtx *batchtx.ExecutionContext
}

// String returns the string representation of the BatchExecutionState.
func (s BatchExecutionState) String() string {
	return "BatchExecutionState"
}

// BatchCommitState is the final phase of an auction. In this state,
// we'll "commit" the batch by broadcasting the batch execution
// transaction. As multiple pending batches can exist, once this batch
// is confirmed, it'll be marked as finalized on disk.
//
// The possible transitions from this state are:
//     * BatchCommitState -> OrderSubmitState
type BatchCommitState struct{}

// String returns the string representation of the BatchCommitState.
func (s BatchCommitState) String() string {
	return "BatchCommitState"
}

// AuctionEvent represents a particular event that is either internally or
// externally triggered. The state machine will only step forward with each new
// event.
type AuctionEvent uint32

const (
	// NoEvent is the default event trigger.
	NoEvent AuctionEvent = iota

	// NewBlockTrigger is an event sent when a new block arrives.
	NewBlockEvent

	// ConfirmationEvent is an event sent when a transaction that we're
	// waiting on confirms.
	ConfirmationEvent

	// BatchTickEvent is an event that fires once our batch auction
	// interval has passed.
	BatchTickEvent
)

// EventTrigger is an interface which wraps a new AuctionEvent along side some
// optional additional data.
type EventTrigger interface {
	// Trigger returns the AuctionEvent which trigged this event.
	Trigger() AuctionEvent
}

// initEvent is the default trigger, this contains no additional data.
type initEvent struct{}

// Trigger returns the AuctionEvent which trigged this event.
//
// NOTE: This method is part of the EventTrigger interface.
func (i *initEvent) Trigger() AuctionEvent {
	return NoEvent
}

// newBlockEvent is an event sent each time a new block confirms.
type newBlockEvent struct {
	// bestHeight is the new best height.
	bestHeight uint32
}

// Trigger returns the AuctionEvent which trigged this event.
//
// NOTE: This method is part of the EventTrigger interface.
func (n *newBlockEvent) Trigger() AuctionEvent {
	return NewBlockEvent
}

// masterAcctReady is an event sent once the master account has been fully
// confirmed and is available for use.
type masterAcctReady struct {
	acct *account.Auctioneer
}

// Trigger returns the AuctionEvent which trigged this event.
//
// NOTE: This method is part of the EventTrigger interface.
func (n *masterAcctReady) Trigger() AuctionEvent {
	return ConfirmationEvent
}

// batchIntervalEvent is fired once the batch ticker fires, meaning we should
// try to make a new market.
type batchIntervalEvent struct{}

// Trigger returns the AuctionEvent which trigged this event.
//
// NOTE: This method is part of the EventTrigger interface.
func (b *batchIntervalEvent) Trigger() AuctionEvent {
	return BatchTickEvent
}
