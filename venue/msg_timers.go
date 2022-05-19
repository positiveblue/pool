package venue

import (
	"sync"
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/venue/matching"
)

// timerWithQuit couples a timer along with a quit channel that can be used to
// stop a goroutine for a timer that's no longer needed.
type timerWithQuit struct {
	*time.Timer
	quit chan struct{}
}

// newTimerWithQuit creates a new timerWithQuit instance backed by the passed
// quit channel.
func newTimerWithQuit(t *time.Timer) *timerWithQuit {
	return &timerWithQuit{
		Timer: t,
		quit:  make(chan struct{}),
	}
}

// msgTimers encapsulates all the state we need to enforced a deadline for
// message responses for traders during a batch's execution phase.
type msgTimers struct {
	// timeoutDuration is the amount of time a trader has to send a
	// response after we send a request.
	timeoutDuration time.Duration

	// timers stores the set of active timers for each active trader in the
	// batch.
	timers map[matching.AccountID]*timerWithQuit

	quit chan struct{}

	wg sync.WaitGroup
}

// newMsgTimers returns a new msgTimers instance that gives each trader a
// period of time T to respond.
func newMsgTimers(t time.Duration) *msgTimers {
	return &msgTimers{
		timeoutDuration: t,
		timers:          make(map[matching.AccountID]*timerWithQuit),
		quit:            make(chan struct{}),
	}
}

// launchMsgTimers launches a timer for each active trader. If the trader
// doesn't send us a message in time, then an event will fire to inform the
// main state machine loop to remove the trader from this batch.
func (m *msgTimers) launchMsgTimers(venueEvents chan EventTrigger,
	traders map[matching.AccountID]*ActiveTrader) {

	// For each trader, we'll create a new active time for it, then launch
	// a goroutine that'll send a timeout event after the timeout period.
	m.wg.Add(len(traders))
	for trader := range traders {
		timer := newTimerWithQuit(time.NewTimer(m.timeoutDuration))
		m.timers[trader] = timer

		go func(t matching.AccountID) {
			defer m.wg.Done()

			select {
			// The timer has fired, so we'll send the timeout event
			// to the main state machine loop.
			case <-timer.C:

				log.Debugf("Firing msg timer for trader=%x", t[:])

				select {
				case venueEvents <- &msgTimeoutEvent{
					trader: t,
				}:

				case <-timer.quit:

				case <-m.quit:
				}

			case <-timer.quit:

			case <-m.quit:
			}

			// TODO(roasbeef): need to always signal old timers to exit?
		}(trader)
	}
}

// processMsgTimeout is called once a timeout event is received, if this isn't
// a duplicate timeout event for a trader, then we'll return a new environment
// that has a tempErr set to remove the target trader.
func (m *msgTimers) processMsgTimeout(event EventTrigger, msgName string,
	ordersIndex map[matching.AccountID][]orderT.Nonce) error {

	// In this case, we'll mark that the batch needs to be failed, and will
	// cache the proper error to signal that the peer should be removed
	// from the batch and re-submitted.
	msgTimeout := event.(*msgTimeoutEvent)

	// It's possible that timer fired *right after* we got the msg from the
	// trader, so we'll ensure that they still have an active timer entry.
	if _, ok := m.timers[msgTimeout.trader]; !ok {
		// In this case this is a false alarm, so we'll just stay in
		// this state.
		return nil
	}

	log.Warnf("trader=%x failed to send msg=%v in time",
		msgTimeout.trader[:], msgName)

	// In this case, we now know that a trader didn't send us the message
	// in time. As a result, we'll exit with the expected error.
	return &ErrMsgTimeout{
		Trader:      msgTimeout.trader,
		Msg:         msgName,
		OrderNonces: ordersIndex[msgTimeout.trader],
	}
}

// cancelTimerForTrader stops the timer goroutine waiting for a trader's
// response, attempts to train the ticker, and finally deletes the timer entry
// for the target trader.
func (m *msgTimers) cancelTimerForTrader(trader matching.AccountID) {
	msgTimer, ok := m.timers[trader]
	if !ok {
		// No timer for this message, could be a client sending a message
		// twice which we ignore.
		return
	}

	log.Debugf("Stopping timer for trader=%x", trader[:])

	// Signal the quit for the timer, so it doesn't linger around anymore
	// as we no longer need it.
	close(msgTimer.quit)

	// As we've received the expected message from this trader, we'll
	// cancel it timer now, draining out the channel if it
	// already ticked.
	if !msgTimer.Stop() {
		select {
		case <-msgTimer.C:
		default:
		}
	}

	delete(m.timers, trader)
}

// noTimersActive returns true if there're no active timers, meaning all
// traders responded with the proper request.
func (m *msgTimers) noTimersActive() bool {
	return len(m.timers) == 0
}

// resetAll signals that all active timer goroutines should exit.
func (m *msgTimers) resetAll() {
	close(m.quit)

	m.wg.Wait()
}
