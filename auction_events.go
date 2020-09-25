package subasta

import (
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/feebump"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
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
//     * OrderSubmitState -> FeeEstimationState (on tick)
type OrderSubmitState struct{}

// String returns the string representation of the OrderSubmitState.
func (s OrderSubmitState) String() string {
	return "OrderSubmitState"
}

// FeeEstimationState is the state we'll go to after we have decided to
// assemble a new batch, and start by getting a fee estimate to use during
// match making.
//
// The possible transitions from this state are:
//     * FeeEstimationState -> MatchMakingState
type FeeEstimationState struct {
}

// String returns the string representation of the FeeEstimationState.
func (s FeeEstimationState) String() string {
	return "FeeEstimationState"
}

// MatchMakingState is the state we enter into when we're attempting to make
// a new market. From this state, we'll either go to execute the market, or
// possibly go back to the OrderSubmitState if there're no orders that actually
// make a market.
//
// The possible transitions from this state are:
//     * MatchMakingState -> OrderSubmitState (fail to make market)
//     * MatchMakingState -> FeeCheckState (market made)
type MatchMakingState struct {
	// batchFeeRate is the fee rate we will use when assembling the batch
	// transaction. This will impact which orders can participate in match
	// making, decided by their MaxBatchFeeRate.
	batchFeeRate chainfee.SatPerKWeight

	// targetFeeRate is the effective fee rate we want the final batch tx
	// to have in order to confirm within a reasonable time. This might
	// differ from the batchFeeRate when we have unonfirmed batches
	// lingering, since it might decrease the effective fee rate of the new
	// batch. In such scenarios we might have to use an increased
	// batchFeeRate in order to meet our effective targetFeeRate.
	targetFeeRate chainfee.SatPerKWeight

	// feeBumping is a boolean that indicates whether we are attempting to
	// fee bump unconfirmed batches with this new batch. If this is true we
	// allow the created batch to be empty, as it still serves the purpose
	// of bumping the fee of the prior batches.
	feeBumping bool
}

// FeeCheckState is a state where we'll check the fee rate of the assembled
// batch together with any still unconfirmed batches.
//
// The possible transitions from this state are:
//     * FeeCheckState -> MatchMakingState (fee had to be bumped)
//     * FeeCheckState -> BatchExecutionState (fee was sufficient)
type FeeCheckState struct {
	// exeCtx is the execution context for the batch to execute, including
	// the assembled batch transaction.
	exeCtx *batchtx.ExecutionContext

	// targetFeeRate is the effective fee rate we want the final batch tx
	// to have in order to confirm within a reasonable time.
	targetFeeRate chainfee.SatPerKWeight

	// feeBumping is a boolean that indicates whether we are attempting to
	// fee bump unconfirmed batches with this new batch. If this is true we
	// allow the created batch to be empty, as it still serves the purpose
	// of bumping the fee rate of prior batches.
	feeBumping bool

	// pendingBatches is the fee info for all our unconfirmed batches,
	// including the new one we haven't yet finalized.
	pendingBatches []*feebump.TxFeeInfo
}

// String returns the string representation of the FeeCheckState.
func (s FeeCheckState) String() string {
	return "FeeCheckState"
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
//     * BatchExecutionState -> FeeEstimationState (execution fail)
//     * BatchExecutionState -> BatchCommitState
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
