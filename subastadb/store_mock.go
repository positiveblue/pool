package subastadb

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/aperture/lsat"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/ban"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/traderterms"
)

// StoreMock is a type to hold mocked orders.
type StoreMock struct {
	mu               sync.Mutex
	Res              map[lsat.TokenID]*account.Reservation
	Accs             map[[33]byte]*account.Account
	BannedAccounts   map[[33]byte]*ban.Info
	BannedNodes      map[[33]byte]*ban.Info
	Orders           map[orderT.Nonce]order.ServerOrder
	BatchPubkey      *btcec.PublicKey
	MasterAcct       *account.Auctioneer
	Snapshots        map[orderT.BatchID]*BatchSnapshot
	lifetimePackages []*chanenforcement.LifetimePackage
	TraderTerms      map[lsat.TokenID]*traderterms.Custom
	t                *testing.T
}

// NewStoreMock creates a new mocked store.
func NewStoreMock(t *testing.T) *StoreMock {
	return &StoreMock{
		Res:            make(map[lsat.TokenID]*account.Reservation),
		Accs:           make(map[[33]byte]*account.Account),
		BannedAccounts: make(map[[33]byte]*ban.Info),
		BannedNodes:    make(map[[33]byte]*ban.Info),
		Orders:         make(map[orderT.Nonce]order.ServerOrder),
		BatchPubkey:    InitialBatchKey,
		Snapshots:      make(map[orderT.BatchID]*BatchSnapshot),
		TraderTerms:    make(map[lsat.TokenID]*traderterms.Custom),
		t:              t,
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
	modifiers ...account.Modifier) (*account.Account, error) {

	a, ok := s.Accs[acct.TraderKeyRaw]
	if !ok {
		return nil, &AccountNotFoundError{AcctKey: acct.TraderKeyRaw}
	}
	for _, modifier := range modifiers {
		modifier(a)
	}
	s.Accs[acct.TraderKeyRaw] = a
	return a, nil
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
	orders := make([]order.ServerOrder, 0, len(s.Orders))
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
	batchSnapshot *BatchSnapshot, nextBatchKey *btcec.PublicKey,
	lifetimePkgs []*chanenforcement.LifetimePackage) error {

	err := s.UpdateOrders(ctx, orders, orderModifiers)
	if err != nil {
		return err
	}

	for idx, acctKey := range accounts {
		acct, err := s.Account(ctx, acctKey, false)
		if err != nil {
			return err
		}
		_, err = s.UpdateAccount(ctx, acct, accountModifiers[idx]...)
		if err != nil {
			return err
		}
	}

	s.MasterAcct = masterAcct
	s.Snapshots[batchID] = batchSnapshot
	s.lifetimePackages = lifetimePkgs

	var batchKey [33]byte
	copy(batchKey[:], nextBatchKey.SerializeCompressed())
	s.MasterAcct.BatchKey = batchKey

	return nil
}

// GetBatchSnapshot returns the self-contained snapshot of a batch with
// the given ID as it was recorded at the time.
//
// NOTE: This is part of the Store interface.
func (s *StoreMock) GetBatchSnapshot(_ context.Context, id orderT.BatchID) (
	*BatchSnapshot, error) {

	snapshot, ok := s.Snapshots[id]
	if !ok {
		return nil, errBatchSnapshotNotFound
	}
	return snapshot, nil
}

// LeaseDurations retrieves all lease duration buckets.
func (s *StoreMock) LeaseDurations(ctx context.Context) (
	map[uint32]order.DurationBucketState, error) {

	return nil, nil
}

// StoreLeaseDuration persists to disk the given lease duration bucket.
func (s *StoreMock) StoreLeaseDuration(ctx context.Context, duration uint32,
	marketState order.DurationBucketState) error {

	return nil
}

// RemoveLeaseDuration removes a single lease duration bucket from the
// database.
func (s *StoreMock) RemoveLeaseDuration(ctx context.Context,
	duration uint32) error {

	return nil
}

// ConfirmBatch finalizes a batch on disk, marking it as pending (unconfirmed)
// no longer.
func (s *StoreMock) ConfirmBatch(ctx context.Context, batchID orderT.BatchID) error {
	return nil
}

// BatchConfirmed returns true if the target batch has been marked finalized
// (confirmed) on disk.
func (s *StoreMock) BatchConfirmed(context.Context, orderT.BatchID) (bool, error) {
	return false, nil
}

// AllTraderTerms returns all trader terms currently in the store.
func (s *StoreMock) AllTraderTerms(_ context.Context) ([]*traderterms.Custom,
	error) {

	result := make([]*traderterms.Custom, 0, len(s.TraderTerms))
	for _, terms := range s.TraderTerms {
		result = append(result, terms)
	}

	return result, nil
}

// GetTraderTerms returns the trader terms for the given trader or
// ErrNoTerms if there are no terms stored for that trader.
func (s *StoreMock) GetTraderTerms(_ context.Context,
	traderID lsat.TokenID) (*traderterms.Custom, error) {

	terms, ok := s.TraderTerms[traderID]
	if !ok {
		return nil, ErrNoTerms
	}

	return terms, nil
}

// PutTraderTerms stores a trader terms item, replacing the previous one
// if an item with the same ID existed.
func (s *StoreMock) PutTraderTerms(_ context.Context,
	terms *traderterms.Custom) error {

	s.TraderTerms[terms.TraderID] = terms

	return nil
}

// DelTraderTerms removes the trader specific terms for the given trader
// ID.
func (s *StoreMock) DelTraderTerms(_ context.Context,
	traderID lsat.TokenID) error {

	delete(s.TraderTerms, traderID)

	return nil
}

// StoreLifetimePackage persists to disk the given channel lifetime
// package.
func (s *StoreMock) StoreLifetimePackage(ctx context.Context,
	pkg *chanenforcement.LifetimePackage) error {

	s.lifetimePackages = append(s.lifetimePackages, pkg)
	return nil
}

// LifetimePackages retrieves all channel lifetime enforcement packages
// which still need to be acted upon.
func (s *StoreMock) LifetimePackages(
	ctx context.Context) ([]*chanenforcement.LifetimePackage, error) {

	return s.lifetimePackages, nil
}

// DeleteLifetimePackage deletes all references to a channel's lifetime
// enforcement package once we've determined that a violation was not
// present.
func (s *StoreMock) DeleteLifetimePackage(ctx context.Context,
	pkg *chanenforcement.LifetimePackage) error {

	for idx, p := range s.lifetimePackages {
		if pkg.ChannelPoint.String() == p.ChannelPoint.String() {
			s.lifetimePackages = append(
				s.lifetimePackages[:idx],
				s.lifetimePackages[idx+1:]...,
			)
			return nil
		}
	}
	return nil
}

// EnforceLifetimeViolation punishes the channel initiator due to a channel
// lifetime violation.
//
// TODO(positiveblue): delete this from the store interface after migrating
// to postgres.
func (s *StoreMock) EnforceLifetimeViolation(ctx context.Context,
	pkg *chanenforcement.LifetimePackage, accKey, nodeKey *btcec.PublicKey,
	accInfo, nodeInfo *ban.Info) error {

	return nil
}

// BanAccount creates/updates the ban Info for the given accountKey.
func (s *StoreMock) BanAccount(ctx context.Context, accKey *btcec.PublicKey,
	banInfo *ban.Info) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], accKey.SerializeCompressed())

	s.BannedAccounts[accountKey] = banInfo

	return nil
}

// GetAccountBan returns the ban Info for the given accountKey.
// Info will be nil if the account is not currently banned.
func (s *StoreMock) GetAccountBan(_ context.Context,
	accKey *btcec.PublicKey, _ uint32) (*ban.Info, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], accKey.SerializeCompressed())

	return s.BannedAccounts[accountKey], nil
}

// BanNode creates/updates the ban Info for the given nodeKey.
func (s *StoreMock) BanNode(_ context.Context, nodKey *btcec.PublicKey,
	banInfo *ban.Info) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var nodeKey [33]byte
	copy(nodeKey[:], nodKey.SerializeCompressed())

	s.BannedNodes[nodeKey] = banInfo

	return nil
}

// GetNodeBan returns the ban Info for the given nodeKey.
// Info will be nil if the node is not currently banned.
func (s *StoreMock) GetNodeBan(_ context.Context,
	nodKey *btcec.PublicKey, _ uint32) (*ban.Info, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	var nodeKey [33]byte
	copy(nodeKey[:], nodKey.SerializeCompressed())

	return s.BannedNodes[nodeKey], nil
}

// ListBannedAccounts returns a map of all accounts that are currently banned.
// The map key is the account's trader key and the value is the ban info.
func (s *StoreMock) ListBannedAccounts(
	_ context.Context, _ uint32) (map[[33]byte]*ban.Info, error) {

	list := make(map[[33]byte]*ban.Info, len(s.BannedAccounts))
	for key, banInfo := range s.BannedAccounts {
		list[key] = banInfo
	}
	return list, nil
}

// ListBannedNodes returns a map of all nodes that are currently banned.
// The map key is the node's identity pubkey and the value is the ban info.
func (s *StoreMock) ListBannedNodes(
	_ context.Context, _ uint32) (map[[33]byte]*ban.Info, error) {

	list := make(map[[33]byte]*ban.Info, len(s.BannedNodes))
	for key, banInfo := range s.BannedNodes {
		list[key] = banInfo
	}
	return list, nil
}

// RemoveAccountBan removes the ban information for a given trader's account
// key. Returns an error if no ban exists.
func (s *StoreMock) RemoveAccountBan(_ context.Context,
	accKey *btcec.PublicKey) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], accKey.SerializeCompressed())

	delete(s.BannedAccounts, accountKey)
	return nil
}

// RemoveNodeBan removes the ban information for a given trader's node identity
// key. Returns an error if no ban exists.
func (s *StoreMock) RemoveNodeBan(_ context.Context,
	nodKey *btcec.PublicKey) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var nodeKey [33]byte
	copy(nodeKey[:], nodKey.SerializeCompressed())

	delete(s.BannedNodes, nodeKey)
	return nil
}

// A compile-time check to make sure StoreMock implements the Store interface.
var _ Store = (*StoreMock)(nil)
