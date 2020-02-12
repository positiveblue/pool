package agoradb

import (
	"context"
	"fmt"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/agora/account"
	clientorder "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
	"github.com/lightninglabs/loop/lsat"
)

// StoreMock is a type to hold mocked orders.
type StoreMock struct {
	Res         map[lsat.TokenID]*account.Reservation
	Accs        map[[33]byte]*account.Account
	Orders      map[clientorder.Nonce]order.ServerOrder
	BatchPubkey *btcec.PublicKey
	t           *testing.T
}

// NewStoreMock creates a new mocked store.
func NewStoreMock(t *testing.T) *StoreMock {
	return &StoreMock{
		Res:         make(map[lsat.TokenID]*account.Reservation),
		Accs:        make(map[[33]byte]*account.Account),
		Orders:      make(map[clientorder.Nonce]order.ServerOrder),
		BatchPubkey: initialBatchKey,
		t:           t,
	}
}

// Init initializes the store and should be called the first time the
// store is created.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) Init(_ context.Context) error {
	return nil
}

// HasReservation determines whether we have an existing reservation associated
// with a token. account.ErrNoReservation is returned if a reservation does not
// exist.
func (s *StoreMock) HasReservation(_ context.Context, tokenID lsat.TokenID) (
	*account.Reservation, error) {

	res, ok := s.Res[tokenID]
	if !ok {
		return nil, account.ErrNoReservation
	}
	return res, nil
}

// ReserveAccount makes a reservation for an auctioneer key for a trader
// associated to a token.
func (s *StoreMock) ReserveAccount(_ context.Context, tokenID lsat.TokenID,
	reservation *account.Reservation) error {

	s.Res[tokenID] = reservation
	return nil
}

// CompleteReservation completes a reservation for an account and adds a record
// for it within the store.
func (s *StoreMock) CompleteReservation(_ context.Context,
	a *account.Account) error {

	delete(s.Res, a.TokenID)
	var traderKey [33]byte
	copy(traderKey[:], a.TraderKeyRaw[:])
	s.Accs[traderKey] = a
	return nil
}

// UpdateAccount updates an account in the database according to the given
// modifiers.
func (s *StoreMock) UpdateAccount(_ context.Context, acct *account.Account,
	modifiers ...account.Modifier) error {

	var traderKey [33]byte
	copy(traderKey[:], acct.TraderKeyRaw[:])
	a, ok := s.Accs[traderKey]
	if !ok {
		return ErrAccountNotFound
	}
	for _, modifier := range modifiers {
		modifier(a)
	}
	s.Accs[traderKey] = a
	return nil
}

// Account retrieves the account associated with the given trader key.
func (s *StoreMock) Account(_ context.Context, traderPubKey *btcec.PublicKey) (
	*account.Account, error) {

	var traderKey [33]byte
	copy(traderKey[:], traderPubKey.SerializeCompressed())
	a, ok := s.Accs[traderKey]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return a, nil
}

// Accounts retrieves all existing accounts.
func (s *StoreMock) Accounts(_ context.Context) ([]*account.Account, error) {
	accounts := make([]*account.Account, len(s.Accs))
	idx := 0
	for _, acct := range s.Accs {
		accounts[idx] = acct
		idx++
	}
	return accounts, nil
}

// BatchKey returns the current per-batch key that must be used to tweak
// account trader keys with.
func (s *StoreMock) BatchKey(context.Context) (*btcec.PublicKey, error) {
	return s.BatchPubkey, nil
}

// SubmitOrder submits an order to the store. If an order with the given nonce
// already exists in the store, ErrOrderExists is returned.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) SubmitOrder(_ context.Context, o order.ServerOrder) error {
	s.Orders[o.Nonce()] = o
	return nil
}

// UpdateOrder updates an order in the database according to the given
// modifiers.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) UpdateOrder(_ context.Context, nonce clientorder.Nonce,
	modifiers ...order.Modifier) error {

	o, ok := s.Orders[nonce]
	if !ok {
		return fmt.Errorf("order with nonce %v does not exist", nonce)
	}
	for _, modifier := range modifiers {
		modifier(o)
	}
	s.Orders[nonce] = o
	return nil
}

// UpdateOrders atomically updates a list of orders in the database
// according to the given modifiers.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) UpdateOrders(ctx context.Context, nonces []clientorder.Nonce,
	modifiers [][]order.Modifier) error {

	for idx, nonce := range nonces {
		err := s.UpdateOrder(ctx, nonce, modifiers[idx]...)
		if err != nil {
			return err
		}
	}
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

// A compile-time check to make sure StoreMock implements the Store interface.
var _ Store = (*StoreMock)(nil)
