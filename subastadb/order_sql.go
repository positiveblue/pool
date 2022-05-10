package subastadb

import (
	"context"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
)

// SubmitOrder submits an order to the store. If an order with the given
// nonce already exists in the store, ErrOrderExists is returned.
func (s *SQLStore) SubmitOrder(context.Context, order.ServerOrder) error {
	return ErrNotImplemented
}

// UpdateOrder updates an order in the database according to the given
// modifiers.
func (s *SQLStore) UpdateOrder(context.Context, orderT.Nonce,
	...order.Modifier) error {

	return ErrNotImplemented
}

// GetOrder returns an order by looking up the nonce. If no order with
// that nonce exists in the store, ErrNoOrder is returned.
func (s *SQLStore) GetOrder(context.Context, orderT.Nonce) (order.ServerOrder,
	error) {

	return nil, ErrNotImplemented
}

// GetOrders returns all non-archived orders that are currently known to
// the store.
func (s *SQLStore) GetOrders(context.Context) ([]order.ServerOrder, error) {
	return nil, ErrNotImplemented
}

// GetArchivedOrders returns all archived orders that are currently
// known to the store.
func (s *SQLStore) GetArchivedOrders(context.Context) ([]order.ServerOrder,
	error) {

	return nil, ErrNotImplemented
}
