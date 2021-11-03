package order

import (
	"context"
	"errors"
	"net"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/pool/order"
	"github.com/lightningnetwork/lnd/lnwire"
)

var (
	// ErrInvalidAmt is returned if the amount chosen for an order is
	// invalid either because it is outside of the system's limits or the
	// user's account doesn't have enough balance to process the order.
	ErrInvalidAmt = errors.New("invalid amount or balance insufficient")
)

// ServerOrder is an interface to allow generic handling of both ask and bid
// orders by both store and manager.
type ServerOrder interface {
	// Order is the embedded interface of the client order as we share all
	// basic behavior on the server.
	order.Order

	// ServerDetails returns the server Kit of the order.
	ServerDetails() *Kit
}

// Kit stores all the common fields that are used to express the decision to
// participate in the auction process. A kit is always wrapped by either a bid
// or an ask.
type Kit struct {
	// Sig is the order signature over the order nonce, signed with the
	// user's account sub key.
	Sig lnwire.Sig

	// MultiSigKey is a key of the node creating the order that will be used
	// to craft the channel funding TX's 2-of-2 multi signature output.
	MultiSigKey [33]byte

	// NodeKey is the identity public key of the node creating the order.
	NodeKey [33]byte

	// NodeAddrs is the list of network addresses of the node creating the
	// order.
	NodeAddrs []net.Addr

	// Lsat is the LSAT token that was used to submit the order.
	Lsat lsat.TokenID

	// UserAgent is the string that identifies the software running on the
	// user's side that was used to initially submit this order.
	UserAgent string
}

// ServerDetails returns the Kit of the server order.
//
// NOTE: This method is part of the ServerOrder interface.
func (k *Kit) ServerDetails() *Kit {
	return k
}

// Ask is the specific order type representing the willingness of an auction
// participant to lend out their funds by opening channels to other auction
// participants.
type Ask struct {
	// Ask is the embedded struct of the client side order.
	order.Ask

	// Kit contains all the common order parameters.
	Kit
}

// LeaseDuration is the maximum number of blocks the liquidity provider is
// willing to provide the channel funds for.
func (a *Ask) LeaseDuration() uint32 {
	return a.Ask.LeaseDuration
}

// Bid is the specific order type representing the willingness of an auction
// participant to pay for inbound liquidity provided by other auction
// participants.
type Bid struct {
	// Bid is the embedded struct of the client side order.
	order.Bid

	// Kit contains all the common order parameters.
	Kit

	// IsSidecar denotes whether this bid was submitted as a sidecar order.
	IsSidecar bool
}

// LeaseDuration is the minimal duration the channel resulting from this bid
// should be kept open, expressed in blocks.
func (b *Bid) LeaseDuration() uint32 {
	return b.Bid.LeaseDuration
}

// This is a compile time check to make certain that both Ask and Bid implement
// the Order interface.
var _ ServerOrder = (*Ask)(nil)
var _ ServerOrder = (*Bid)(nil)

// Modifier abstracts the modification of an account through a function.
type Modifier func(ServerOrder)

// StateModifier is a functional option that modifies the state of an account.
func StateModifier(state order.State) Modifier {
	return func(order ServerOrder) {
		order.Details().State = state
	}
}

// UnitsFulfilledModifier is a functional option that modifies the number of
// unfulfilled units of an order.
func UnitsFulfilledModifier(newUnfulfilledUnits order.SupplyUnit) Modifier {
	return func(order ServerOrder) {
		order.Details().UnitsUnfulfilled = newUnfulfilledUnits
	}
}

// Store is the interface a store has to implement to support persisting orders.
type Store interface {
	// SubmitOrder submits an order to the store. If an order with the given
	// nonce already exists in the store, ErrOrderExists is returned.
	SubmitOrder(context.Context, ServerOrder) error

	// UpdateOrder updates an order in the database according to the given
	// modifiers.
	UpdateOrder(context.Context, order.Nonce, ...Modifier) error

	// GetOrder returns an order by looking up the nonce. If no order with
	// that nonce exists in the store, ErrNoOrder is returned.
	GetOrder(context.Context, order.Nonce) (ServerOrder, error)

	// GetOrders returns all non-archived orders that are currently known to
	// the store.
	GetOrders(context.Context) ([]ServerOrder, error)

	// GetArchivedOrders returns all archived orders that are currently
	// known to the store.
	GetArchivedOrders(context.Context) ([]ServerOrder, error)
}
