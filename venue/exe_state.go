package venue

import (
	"fmt"

	"github.com/lightninglabs/agora/venue/matching"
)

// ExecutionState tracks the current state of the batch execution state machine.
type ExecutionState uint32

const (
	// NoActiveBatch indicates that no active batch is present, and we're
	// waiting for a new batch to arrive. Note that we can only process a
	// single batch at a time.
	//
	// Valid transitions from this state:
	//  * NoActiveBatch -> PrepareSent (valid batch, prepare sent)
	//  * NoActiveBatch -> BatchTempError (fail batch error)
	NoActiveBatch ExecutionState = iota

	// BatchTempError is the state we go to when we have a non-terminating
	// error. A non-terminating error is one where we simply need to kick
	// some traders/orders out of the batch, and resume execution once
	// done.
	//
	// Valid transitions from this state:
	//   * BatchTempError -> NoActiveBatch (send error, wait for new batch)
	BatchTempError

	// PrepareSent is the state we enter after the prepare message has been
	// sent. From the state, if we don't get an accept from all traders,
	// then we transition to the BatchTempError as we need to kick off some
	// traders. Otherwise, we'll send the SignBegin message and proceed to
	// the next phase of execution.
	//
	// Valid transitions from this state:
	//  * PrepareSent -> BatchTempError (trader timeout, client reject, etc)
	//  * PrepareSent -> BatchSigning (got OrderMatchAccept from all traders)
	PrepareSent

	// BatchSigning in the batch signing phase, we're waiting for all the
	// traders to send back their signatures. If we don't get all sigs, or
	// a sig is invalid, then we'll go to kick a set of traders off.
	// Otherwise, we'll got to the final BatchFinalize state.
	//
	// Valid transitions from this state:
	//  * BatchSigning -> BatchTempError (trader timeout, etc)
	//  * BatchSigning -> BatchFinalize (all sigs valid)
	BatchSigning

	// BatchFinalize is the final state of the batch execution. We'll
	// generate or final signature for the batch, write it to disk, and
	// finally send the finalize message to all traders.
	//
	// Valid transitions from this state:
	//  * BatchFinalize -> BatchTempError (unable to send to client?
	//  * BatchFinalize -> BatchComplete -> NoActiveBatch (send finalize, commit to disk)
	//
	// TODO(roasbeef): can't really back out once everything has been signed
	// tho?
	BatchFinalize

	// BatchComplete is the final terminal state for a batch. In this state
	// we simply send back the response to the caller, and go back to the
	// NoActiveBatch state.
	//
	// Valid transitions from this state:
	//  * BatchComplete -> NoActiveBatch
	BatchComplete
)

// String returns a human-readable version of the ExecutionState enum.
func (e ExecutionState) String() string {
	switch e {
	case NoActiveBatch:
		return "NoActiveBatch"

	case BatchTempError:
		return "BatchTempError"

	case PrepareSent:
		return "PrepareSent"

	case BatchSigning:
		return "BatchSigning"

	case BatchFinalize:
		return "BatchFinalize"

	case BatchComplete:
		return "BatchComplete"

	default:
		return fmt.Sprintf("<unknown_event=%v>", int(e))
	}
}

// ExecutionEvent represents a new event that will drive the state machine
// forward given a state and our environment.
type ExecutionEvent uint32

const (
	// NoEvent indicates that no special event was triggered.
	NoEvent ExecutionEvent = iota

	// MsgRecv is an event triggered by receiving a new message from a
	// trader.
	MsgRecv

	// MsgTimeout is an event triggered by having a message timeout,
	// meaning we failed to receive a message from a peer in time.
	MsgTimeout

	// NewBatch is an event which is triggered by us receiving a new batch
	// to execute.
	NewBatch
)

// Strings returns a human-readble version of the target VenueEvent.
func (e ExecutionEvent) String() string {
	switch e {
	case NoEvent:
		return "NoEvent"

	case MsgRecv:
		return "MsgRecv"

	case MsgTimeout:
		return "MsgTimeout"

	case NewBatch:
		return "NewBatch"

	default:
		return fmt.Sprintf("<unknown_event=%v", int(e))
	}
}

// EventTrigger is an interface that allows a new event to package some state
// along with it. We call an event and its state a trigger.
type EventTrigger interface {
	// Trigger returns the event type of this trigger.
	Trigger() ExecutionEvent
}

// msgTimeout indicates that a trader failed to send us a message in time.
type msgTimeoutEvent struct {
	// trader is the trader that didn't send the message in time.
	trader matching.AccountID
}

// Trigger returns the VenueEvent which trigged this event.
//
// NOTE: This method is part of the EventTrigger interface.
func (m *msgTimeoutEvent) Trigger() ExecutionEvent {
	return MsgTimeout
}

// msgRecvEvent is an event triggered by us receiving a message from a trader.
type msgRecvEvent struct {
	// msg is the message sent to us.
	msg TraderMsg

	// TODO(roasbeef): also add msg type?? or there above, need to validate
}

// Trigger returns the VenueEvent which trigged this event.
//
// NOTE: This method is part of the EventTrigger interface.
func (m *msgRecvEvent) Trigger() ExecutionEvent {
	return MsgRecv
}

// newBatchEvent is sent when a new batch is ready to be executed.
type newBatchEvent struct {
	batch *matching.OrderBatch
}

// Trigger returns the VenueEvent which trigged this event.
//
// NOTE: This method is part of the EventTrigger interface.
func (n *newBatchEvent) Trigger() ExecutionEvent {
	return NewBatch
}
