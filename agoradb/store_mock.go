package agoradb

import (
	"context"
	"testing"

	clientorder "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
)

// StoreMock is a type to hold mocked orders.
type StoreMock struct {
	Orders map[clientorder.Nonce]order.ServerOrder
	t      *testing.T
}

// NewStoreMock creates a new mocked store.
func NewStoreMock(t *testing.T) *StoreMock {
	return &StoreMock{
		Orders: make(map[clientorder.Nonce]order.ServerOrder),
		t:      t,
	}
}

// Init initializes the store and should be called the first time the
// store is created.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) Init(_ context.Context) error {
	return nil
}

// UpdateOrder stores an order by using the order's nonce as an identifier. If any
// order with the same identifier already existed in the store, it will be
// overwritten.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) UpdateOrder(_ context.Context, order order.ServerOrder) error {
	s.Orders[order.Nonce()] = order
	return nil
}

// GetOrder returns an order by looking up the nonce. If no order with that
// nonce exists in the store, ErrNoOrder is returned.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) GetOrder(_ context.Context, nonce clientorder.Nonce) (
	order.ServerOrder, error) {

	o, ok := s.Orders[nonce]
	if !ok {
		return nil, ErrNoOrder
	}
	return o, nil
}

// GetOrders returns all orders that are currently known to the store.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) GetOrders(_ context.Context) ([]order.ServerOrder, error) {
	orders := make([]order.ServerOrder, len(s.Orders))
	idx := 0
	for _, o := range s.Orders {
		orders[idx] = o
	}
	return orders, nil
}
