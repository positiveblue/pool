package venue

import (
	"github.com/lightninglabs/agora/venue/matching"
)

type (
	// IncomingMsgLine...
	IncomingMsgLine <-chan TraderMsg

	// OutgoingMsgLine...
	OutgoingMsgLine chan<- ExecutionMsg
)

// ActiveTrader...
type ActiveTrader struct {
	*matching.Trader

	// CommLine...
	CommLine *DuplexLine
}

// DuplexLine...
type DuplexLine struct {
	// Send...
	Send OutgoingMsgLine

	// Recv...
	Recv IncomingMsgLine
}
