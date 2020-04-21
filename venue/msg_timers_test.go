package venue

import (
	"testing"
	"time"

	orderT "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/venue/matching"
)

const (

	// testTimeoutDuration is the amount of that after which, the timers should
	// expire.
	testTimeoutDuration = time.Millisecond * 300
)

// TestMsgTimersLaunchMsgTimers tests the lifecycle around launching and
// cancelling message timers.
func TestMsgTimersLaunchMsgTimers(t *testing.T) {
	t.Parallel()

	msgTimer := newMsgTimers(testTimeoutDuration)

	traders := map[matching.AccountID]*ActiveTrader{
		acctIDBig:   nil,
		acctIDSmall: nil,
	}

	// We'll now launch the set of message timers for both traders above
	// feeding into our custom events channel.
	eventsChan := make(chan EventTrigger)
	msgTimer.launchMsgTimers(eventsChan, traders)

	// We'll now wait for both timers to expire.
	time.Sleep(testTimeoutDuration)

	// At this point we should have two events tick, one for each trader.
	for range traders {
		select {
		case <-eventsChan:

		case <-time.After(time.Second * 1):
			t.Fatalf("timer failed to tick")
		}
	}

	// We'll now launch another set of timers, but this time we'll cancel
	// them all before they have a chance to tick.
	msgTimer.launchMsgTimers(eventsChan, traders)

	close(msgTimer.quit)

	// There should be now events sent out over our events channel.
	select {
	case <-eventsChan:
		t.Fatalf("timer shouldn't have ticked")

	case <-time.After(time.Second * 1):
	}

	// We'll now re-create the main timer set since we closed the primary
	// quit channel.
	msgTimer = newMsgTimers(testTimeoutDuration)
	defer msgTimer.resetAll()

	// Finally we'll launch once more in order to exercise the last way of
	// quitting: the message timer ticker.
	msgTimer.launchMsgTimers(eventsChan, traders)

	// Now we'll cancel only one of the timers for the trader, meaning that
	// we should still have one event fire below.
	close(msgTimer.timers[acctIDSmall].quit)

	select {
	case <-eventsChan:

	case <-time.After(time.Second * 1):
		t.Fatalf("timer should have ticked")
	}
	select {
	case <-eventsChan:
		t.Fatalf("timer shouldn't have ticked")

	case <-time.After(time.Second * 1):
	}
}

// TestMsgTimersProcessMsg tests that the process timeout method will only
// return an error if there's an active timer for a trader.
func TestMsgTimersProcessMsg(t *testing.T) {
	t.Parallel()

	msgTimer := newMsgTimers(testTimeoutDuration)
	defer msgTimer.resetAll()

	timeoutEvent := msgTimeoutEvent{
		trader: acctIDBig,
	}

	// If we attempt to process a timeout for a non-existent trader, then
	// we shouldn't get an error.
	err := msgTimer.processMsgTimeout(&timeoutEvent, "testMsg", nil)
	if err != nil {
		t.Fatalf("expected nil error instead got: %v", err)
	}

	// Insert an entry to the set of timers to act as if we just launched
	// one.
	msgTimer.timers[acctIDBig] = nil

	// We'll now launch a timer for the trader above, but fire the timeout
	// event early ourselves, this should return the expected error and
	// identify the trader that didn't send the message in time.
	orderIndex := map[matching.AccountID][]orderT.Nonce{
		acctIDBig: {
			bid1.Nonce(),
		},
	}
	err = msgTimer.processMsgTimeout(
		&timeoutEvent, "testMsg", orderIndex,
	)

	// Ensure that we got the proper error, and that the error is well
	// formed as we expect.
	timeoutErr, ok := err.(*ErrMsgTimeout)
	if !ok {
		t.Fatalf("wrong error type: %T", err)
	}
	if timeoutErr.Trader != acctIDBig {
		t.Fatalf("wrong trader for timeout err: expected %x, got %x",
			acctIDBig[:], timeoutErr.Trader[:])
	}
	if timeoutErr.OrderNonces[0] != bid1.Nonce() {
		t.Fatalf("order nonce entry not found")
	}
}

// TestMsgTimersCancelTimer tests that cancelling a timeout will stop an active
// timer goroutine, and also clean up state as expected.
func TestMsgTimersCancelTimer(t *testing.T) {
	t.Parallel()

	msgTimer := newMsgTimers(testTimeoutDuration)
	defer msgTimer.resetAll()

	// If we try try to cancel a timer that doesn't exist, there should be
	// a simple noop.
	msgTimer.cancelTimerForTrader(acctIDBig)

	// This time, we'll actually launch a real timer for our account ID, to
	// cancel shortly below.
	timer := newTimerWithQuit(time.NewTimer(testTimeoutDuration))
	msgTimer.timers[acctIDBig] = timer

	// We'll now cancel the timer before it has a chance to actually fire.
	msgTimer.cancelTimerForTrader(acctIDBig)

	// The quit channel within the wrapped timer should've fired.
	select {
	case <-timer.quit:

	case <-time.After(time.Second * 1):
		t.Fatalf("quit chan wasn't closed")
	}

	// The timer entry should now also no longer be in the main timer map.
	if !msgTimer.noTimersActive() {
		t.Fatalf("timer still active")
	}
}
