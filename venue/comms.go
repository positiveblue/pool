package venue

import (
	"github.com/lightninglabs/subasta/venue/matching"
)

type (
	// IncomingMsgLine is a type of channel used for traders to receive
	// messages from the main batch executor.
	IncomingMsgLine <-chan TraderMsg

	// OutgoingMsgLine is a type of channel used to send messages to an
	// active trader.
	OutgoingMsgLine chan<- ExecutionMsg
)

// ActiveTrader wraps a normal trader instance along with communication
// channels used to send messages back and forth.
type ActiveTrader struct {
	*matching.Trader

	// CommLine is the active communication line between the trader and the
	// auctioneer.
	CommLine *DuplexLine
}

// DuplexLine is the communication line between a trader and the batch
// executor.
type DuplexLine struct {
	// Send is the channel used for sending out to the trader.
	Send OutgoingMsgLine

	// Recv is the channel used for receiving messages from the trader.
	Recv IncomingMsgLine
}
