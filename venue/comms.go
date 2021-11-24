package venue

import (
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/pool/order"
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

	// TokenID is the unique identifier of the trader's daemon. One daemon
	// can have multiple accounts and therefore map to multiple "traders"
	// from the point of view of the venue. But one running daemon only has
	// one LSAT per connection.
	TokenID lsat.TokenID

	// CommLine is the active communication line between the trader and the
	// auctioneer.
	CommLine *DuplexLine

	// IsSidecar indicates that this trader is exclusively handling sidecar
	// channels. Such a trader doesn't have their own account.
	IsSidecar bool

	// BatchVersion indicates the version that this trader will use to verify
	// the batches.
	BatchVersion order.BatchVersion
}

// DuplexLine is the communication line between a trader and the batch
// executor.
type DuplexLine struct {
	// Send is the channel used for sending out to the trader.
	Send OutgoingMsgLine

	// Recv is the channel used for receiving messages from the trader.
	Recv IncomingMsgLine
}
