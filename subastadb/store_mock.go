package subastadb

import (
	"context"
	"fmt"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
)

// StoreMock is a type to hold mocked orders.
type StoreMock struct {
	Res         map[lsat.TokenID]*account.Reservation
	Accs        map[[33]byte]*account.Account
	BannedAccs  map[[33]byte][2]uint32 // 0: ban start height, 1: delta
	Orders      map[orderT.Nonce]order.ServerOrder
	BatchPubkey *btcec.PublicKey
	MasterAcct  *account.Auctioneer
	Snapshots   map[orderT.BatchID]*matching.OrderBatch
	BatchTx     *wire.MsgTx
	t           *testing.T
}

// NewStoreMock creates a new mocked store.
func NewStoreMock(t *testing.T) *StoreMock {
	return &StoreMock{
		Res:         make(map[lsat.TokenID]*account.Reservation),
		Accs:        make(map[[33]byte]*account.Account),
		BannedAccs:  make(map[[33]byte][2]uint32),
		Orders:      make(map[orderT.Nonce]order.ServerOrder),
		BatchPubkey: initialBatchKey,
		Snapshots:   make(map[orderT.BatchID]*matching.OrderBatch),
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

// HasReservationForKey determines whether we have an existing reservation
// associated with a trader key. ErrNoReservation is returned if a reservation
// does not exist.
func (s *StoreMock) HasReservationForKey(_ context.Context,
	traderKey *btcec.PublicKey) (*account.Reservation, *lsat.TokenID,
	error) {

	var traderKeyRaw [33]byte
	copy(traderKeyRaw[:], traderKey.SerializeCompressed())

	for _, res := range s.Res {
		if res.TraderKeyRaw == traderKeyRaw {
			return res, &lsat.TokenID{}, nil
		}
	}

	return nil, nil, account.ErrNoReservation
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
	s.Accs[a.TraderKeyRaw] = a
	return nil
}

// UpdateAccount updates an account in the database according to the given
// modifiers.
func (s *StoreMock) UpdateAccount(_ context.Context, acct *account.Account,
	modifiers ...account.Modifier) error {

	a, ok := s.Accs[acct.TraderKeyRaw]
	if !ok {
		return &AccountNotFoundError{AcctKey: acct.TraderKeyRaw}
	}
	for _, modifier := range modifiers {
		modifier(a)
	}
	s.Accs[acct.TraderKeyRaw] = a
	return nil
}

// StoreAccountDiff stores a pending set of updates that should be applied to an
// account after a successful modification. As a result, the account is
// transitioned to StatePendingUpdate until the transaction applying the account
// updates has confirmed.
func (s *StoreMock) StoreAccountDiff(ctx context.Context,
	traderKey *btcec.PublicKey, modifiers []account.Modifier) error {

	return nil
}

// CommitAccountDiff commits the latest stored pending set of updates for an
// account after a successful modification. Once the updates are committed, the
// account is transitioned back to StateOpen.
func (s *StoreMock) CommitAccountDiff(ctx context.Context,
	traderKey *btcec.PublicKey) error {

	return nil
}

// Account retrieves the account associated with the given trader key.
func (s *StoreMock) Account(_ context.Context, traderPubKey *btcec.PublicKey,
	_ bool) (*account.Account, error) {

	var traderKey [33]byte
	copy(traderKey[:], traderPubKey.SerializeCompressed())
	a, ok := s.Accs[traderKey]
	if !ok {
		return nil, NewAccountNotFoundError(traderPubKey)
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
func (s *StoreMock) UpdateOrder(_ context.Context, nonce orderT.Nonce,
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
func (s *StoreMock) UpdateOrders(ctx context.Context, nonces []orderT.Nonce,
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
func (s *StoreMock) GetOrder(_ context.Context, nonce orderT.Nonce) (
	order.ServerOrder, error) {

	o, ok := s.Orders[nonce]
	if !ok {
		return nil, ErrNoOrder
	}
	return o, nil
}

// GetOrders returns all non-archived  orders that are currently known to the
// store.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) GetOrders(_ context.Context) ([]order.ServerOrder, error) {
	orders := make([]order.ServerOrder, len(s.Orders))
	for _, o := range s.Orders {
		if o.Details().State.Archived() {
			continue
		}

		orders = append(orders, o)
	}
	return orders, nil
}

// GetArchivedOrders returns all archived orders that are currently known to the
// store.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) GetArchivedOrders(_ context.Context) ([]order.ServerOrder,
	error) {

	orders := make([]order.ServerOrder, 0, len(s.Orders))
	for _, o := range s.Orders {
		if !o.Details().State.Archived() {
			continue
		}

		orders = append(orders, o)
	}
	return orders, nil
}

// FetchAuctioneerAccount retrieves the current information pertaining to the
// current auctioneer output state.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) FetchAuctioneerAccount(_ context.Context) (
	*account.Auctioneer, error) {

	if s.MasterAcct == nil {
		return nil, fmt.Errorf("no master account set")
	}
	return s.MasterAcct, nil
}

// UpdateAuctioneerAccount updates the current auctioneer output in-place and
// also updates the per batch key according to the state in the auctioneer's
// account.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) UpdateAuctioneerAccount(_ context.Context,
	acct *account.Auctioneer) error {

	s.MasterAcct = acct
	return nil
}

// PersistBatchResult atomically updates all modified orders/accounts, persists
// a snapshot of the batch and switches to the next batch ID. If any single
// operation fails, the whole set of changes is rolled back.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) PersistBatchResult(ctx context.Context,
	orders []orderT.Nonce, orderModifiers [][]order.Modifier,
	accounts []*btcec.PublicKey, accountModifiers [][]account.Modifier,
	masterAcct *account.Auctioneer, batchID orderT.BatchID,
	batch *matching.OrderBatch, nextBatchKey *btcec.PublicKey,
	tx *wire.MsgTx) error {

	err := s.UpdateOrders(ctx, orders, orderModifiers)
	if err != nil {
		return err
	}

	for idx, acctKey := range accounts {
		acct, err := s.Account(ctx, acctKey, false)
		if err != nil {
			return err
		}
		err = s.UpdateAccount(ctx, acct, accountModifiers[idx]...)
		if err != nil {
			return err
		}
	}

	s.MasterAcct = masterAcct
	s.BatchTx = tx
	s.Snapshots[batchID] = batch

	var batchKey [33]byte
	copy(batchKey[:], nextBatchKey.SerializeCompressed())
	s.MasterAcct.BatchKey = batchKey

	return nil
}

// PersistBatchSnapshot persists a self-contained snapshot of a batch
// including all involved orders and accounts.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) PersistBatchSnapshot(_ context.Context, id orderT.BatchID,
	batch *matching.OrderBatch, tx *wire.MsgTx) error {

	s.Snapshots[id] = batch
	s.BatchTx = tx
	return nil
}

// GetBatchSnapshot returns the self-contained snapshot of a batch with
// the given ID as it was recorded at the time.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) GetBatchSnapshot(_ context.Context, id orderT.BatchID) (
	*matching.OrderBatch, *wire.MsgTx, error) {

	snapshot, ok := s.Snapshots[id]
	if !ok {
		return nil, nil, errBatchSnapshotNotFound
	}
	return snapshot, s.BatchTx, nil
}

// BanAccount attempts to ban the account associated with a trader starting from
// the current height of the chain. The duration of the ban will depend on how
// many times the node has been banned before and grows exponentially, otherwise
// it is 144 blocks.
func (s *StoreMock) BanAccount(ctx context.Context, traderKey *btcec.PublicKey,
	currentHeight uint32) error {

	var accountKey [33]byte
	copy(accountKey[:], traderKey.SerializeCompressed())

	banTuple := [2]uint32{currentHeight, initialBanDuration}

	if existingBan, isBanned := s.BannedAccs[accountKey]; isBanned {
		banTuple[1] = existingBan[1] * 2
	}

	s.BannedAccs[accountKey] = banTuple
	return nil
}

// IsAccountBanned determines whether the given account is banned at the current
// height. The ban's expiration height is returned.
func (s *StoreMock) IsAccountBanned(_ context.Context,
	traderKey *btcec.PublicKey, bestHeight uint32) (bool, uint32, error) {

	var accountKey [33]byte
	copy(accountKey[:], traderKey.SerializeCompressed())

	banTuple, isBanned := s.BannedAccs[accountKey]
	if !isBanned {
		return false, 0, nil
	}

	expiration := banTuple[0] + banTuple[1]
	return bestHeight < expiration, expiration, nil
}

// A compile-time check to make sure StoreMock implements the Store interface.
var _ Store = (*StoreMock)(nil)
