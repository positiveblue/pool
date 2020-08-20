package subasta

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/llm/clmscript"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/llm/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/subscribe"
	"github.com/stretchr/testify/require"
)

var (
	key = [chainhash.HashSize]byte{ // nolint:unused
		0x81, 0xb6, 0x37, 0xd8, 0xfc, 0xd2, 0xc6, 0xda,
		0x68, 0x59, 0xe6, 0x96, 0x31, 0x13, 0xa1, 0x17,
		0xd, 0xe7, 0x93, 0xe4, 0xb7, 0x25, 0xb8, 0x4d,
		0x1e, 0xb, 0x4c, 0xf9, 0x9e, 0xc5, 0x8c, 0xe9,
	}

	_, pubKey = btcec.PrivKeyFromBytes(btcec.S256(), key[:])
)

func randomPubKey(t *testing.T) *btcec.PublicKey {
	var testPriv [32]byte
	if _, err := rand.Read(testPriv[:]); err != nil {
		t.Fatalf("could not create private key: %v", err)
	}

	_, pub := btcec.PrivKeyFromBytes(btcec.S256(), testPriv[:])
	return pub
}

type mockAuctioneerState struct {
	sync.RWMutex

	state AuctionState

	stateTransitions chan AuctionState

	acct *account.Auctioneer

	orders map[orderT.Nonce]order.ServerOrder

	batchKey *btcec.PublicKey

	// batchStates tracks the current state for a batch. True means the
	// batch is confirmed.
	batchStates map[orderT.BatchID]bool

	snapshots map[orderT.BatchID]*wire.MsgTx

	bannedAccounts map[matching.AccountID]struct{}
}

func newMockAuctioneerState(batchKey *btcec.PublicKey) *mockAuctioneerState {
	return &mockAuctioneerState{
		batchKey:         batchKey,
		stateTransitions: make(chan AuctionState, 100),
		orders:           make(map[orderT.Nonce]order.ServerOrder),
		batchStates:      make(map[orderT.BatchID]bool),
		snapshots:        make(map[orderT.BatchID]*wire.MsgTx),
		bannedAccounts:   make(map[matching.AccountID]struct{}),
	}
}

func (m *mockAuctioneerState) UpdateAuctioneerAccount(ctx context.Context,
	acct *account.Auctioneer) error {

	m.Lock()
	defer m.Unlock()

	m.acct = acct

	return nil
}

func (m *mockAuctioneerState) FetchAuctioneerAccount(ctx context.Context,
) (*account.Auctioneer, error) {

	m.RLock()
	defer m.RUnlock()

	if m.acct == nil {
		return nil, account.ErrNoAuctioneerAccount
	}

	return m.acct, nil
}

func (m *mockAuctioneerState) BatchKey(context.Context) (*btcec.PublicKey, error) {
	m.RLock()
	defer m.RUnlock()

	return m.batchKey, nil
}

func (m *mockAuctioneerState) UpdateAuctionState(state AuctionState) error {
	m.Lock()
	m.state = state
	m.Unlock()

	m.stateTransitions <- state

	return nil
}

func (m *mockAuctioneerState) AuctionState() (AuctionState, error) {
	m.RLock()
	defer m.RUnlock()

	return m.state, nil
}

func (m *mockAuctioneerState) ConfirmBatch(ctx context.Context,
	bid orderT.BatchID) error {

	m.Lock()
	defer m.Unlock()

	m.batchStates[bid] = true

	return nil
}

func (m *mockAuctioneerState) BatchConfirmed(ctx context.Context,
	bid orderT.BatchID) (bool, error) {

	m.Lock()
	defer m.Unlock()

	confirmed, ok := m.batchStates[bid]
	if !ok {
		return false, subastadb.ErrNoBatchExists
	}

	return confirmed, nil
}

func (m *mockAuctioneerState) GetBatchSnapshot(ctx context.Context,
	bid orderT.BatchID) (*matching.OrderBatch, *wire.MsgTx, error) {

	m.Lock()
	defer m.Unlock()

	tx, ok := m.snapshots[bid]
	if !ok {
		return nil, nil, fmt.Errorf("unable to find snapshot")
	}

	return nil, tx, nil
}

func (m *mockAuctioneerState) GetOrder(ctx context.Context,
	nonce orderT.Nonce) (order.ServerOrder, error) {

	m.Lock()
	defer m.Unlock()

	order, ok := m.orders[nonce]
	if !ok {
		return nil, fmt.Errorf("order not found")
	}

	return order, nil
}

func (m *mockAuctioneerState) GetOrders(context.Context) ([]order.ServerOrder, error) {
	m.Lock()
	defer m.Unlock()

	orders := make([]order.ServerOrder, 0, len(m.orders))
	for _, order := range m.orders {
		orders = append(orders, order)
	}

	return orders, nil
}

func (m *mockAuctioneerState) BanAccount(_ context.Context,
	accountKey *btcec.PublicKey, _ uint32) error {

	m.Lock()
	defer m.Unlock()

	var k matching.AccountID
	copy(k[:], accountKey.SerializeCompressed())
	m.bannedAccounts[k] = struct{}{}

	return nil
}

func (m *mockAuctioneerState) isBannedTrader(trader matching.AccountID) bool {
	m.Lock()
	defer m.Unlock()

	_, ok := m.bannedAccounts[trader]
	return ok
}

var _ AuctioneerDatabase = (*mockAuctioneerState)(nil)

type mockWallet struct {
	sync.RWMutex

	balance btcutil.Amount

	lastTxs []*wire.MsgTx
}

func (m *mockWallet) SendOutputs(cctx context.Context, outputs []*wire.TxOut,
	_ chainfee.SatPerKWeight) (*wire.MsgTx, error) {

	m.Lock()
	defer m.Unlock()

	lastTx := wire.NewMsgTx(2)
	lastTx.TxIn = []*wire.TxIn{
		{},
	}
	lastTx.TxOut = outputs

	m.lastTxs = append(m.lastTxs, lastTx)

	return lastTx, nil
}

func (m *mockWallet) ConfirmedWalletBalance(context.Context) (btcutil.Amount, error) {
	m.RLock()
	defer m.RUnlock()

	return m.balance, nil
}

func (m *mockWallet) ListTransactions(context.Context) ([]*wire.MsgTx, error) {
	m.RLock()
	defer m.RUnlock()

	return m.lastTxs, nil
}

func (m *mockWallet) PublishTransaction(ctx context.Context, tx *wire.MsgTx) error {
	m.Lock()
	defer m.Unlock()

	m.lastTxs = append(m.lastTxs, tx)
	return nil
}

func (m *mockWallet) DeriveNextKey(context.Context, int32) (
	*keychain.KeyDescriptor, error) {

	return &keychain.KeyDescriptor{
		PubKey: pubKey,
	}, nil
}

func (m *mockWallet) EstimateFee(context.Context, int32) (
	chainfee.SatPerKWeight, error) {

	return chainfee.FeePerKwFloor, nil
}

var _ Wallet = (*mockWallet)(nil)

type mockCallMarket struct {
	orders map[orderT.Nonce]order.ServerOrder

	shouldClear bool

	sync.Mutex
}

func newMockCallMarket() *mockCallMarket {
	return &mockCallMarket{
		orders: make(map[orderT.Nonce]order.ServerOrder),
	}
}

func (m *mockCallMarket) MaybeClear(chainfee.SatPerKWeight) (
	*matching.OrderBatch, error) {

	m.Lock()
	defer m.Unlock()

	if !m.shouldClear {
		return nil, matching.ErrNoMarketPossible
	}

	return &matching.OrderBatch{}, nil
}

func (m *mockCallMarket) RemoveMatches(matches ...matching.MatchedOrder) error {
	return nil
}

func (m *mockCallMarket) ConsiderBids(bids ...*order.Bid) error {
	m.Lock()
	defer m.Unlock()

	for _, bid := range bids {
		m.orders[bid.Nonce()] = bid
	}

	return nil
}

func (m *mockCallMarket) ForgetBids(nonces ...orderT.Nonce) error {
	m.Lock()
	defer m.Unlock()

	for _, nonce := range nonces {
		delete(m.orders, nonce)
	}

	return nil
}

func (m *mockCallMarket) ConsiderAsks(asks ...*order.Ask) error {
	m.Lock()
	defer m.Unlock()

	for _, ask := range asks {
		m.orders[ask.Nonce()] = ask
	}

	return nil
}

func (m *mockCallMarket) ForgetAsks(nonces ...orderT.Nonce) error {
	m.Lock()
	defer m.Unlock()

	for _, nonce := range nonces {
		delete(m.orders, nonce)
	}

	return nil
}

var _ matching.BatchAuctioneer = (*mockCallMarket)(nil)

type mockBatchExecutor struct {
	submittedBatch *matching.OrderBatch
	resChan        chan *venue.ExecutionResult

	sync.Mutex
}

func newMockBatchExecutor() *mockBatchExecutor {
	return &mockBatchExecutor{
		resChan: make(chan *venue.ExecutionResult, 1),
	}
}

func (m *mockBatchExecutor) Submit(b *matching.OrderBatch, _ terms.FeeSchedule,
	_ chainfee.SatPerKWeight) (chan *venue.ExecutionResult, error) {

	m.Lock()
	defer m.Unlock()

	m.submittedBatch = b

	return m.resChan, nil
}

var _ BatchExecutor = (*mockBatchExecutor)(nil)

type mockChannelEnforcer struct {
	sync.Mutex
	lifetimePkgs []*chanenforcement.LifetimePackage
}

func newMockChannelEnforcer() *mockChannelEnforcer {
	return &mockChannelEnforcer{}
}

func (m *mockChannelEnforcer) EnforceChannelLifetimes(
	pkgs ...*chanenforcement.LifetimePackage) error {

	m.Lock()
	defer m.Unlock()

	m.lifetimePkgs = append(m.lifetimePkgs, pkgs...)
	return nil
}

var _ ChannelEnforcer = (*mockChannelEnforcer)(nil)

type auctioneerTestHarness struct {
	t *testing.T

	db *mockAuctioneerState

	notifier *account.MockChainNotifier

	wallet *mockWallet

	auctioneer *Auctioneer

	orderFeed *subscribe.Server

	callMarket *mockCallMarket

	executor *mockBatchExecutor

	channelEnforcer *mockChannelEnforcer
}

func newAuctioneerTestHarness(t *testing.T) *auctioneerTestHarness {
	mockDB := newMockAuctioneerState(pubKey)
	wallet := &mockWallet{}
	notifier := account.NewMockChainNotifier()

	orderFeeder := subscribe.NewServer()
	if err := orderFeeder.Start(); err != nil {
		t.Fatalf("unable to start feeder: %v", err)
	}

	callMarket := newMockCallMarket()
	executor := newMockBatchExecutor()
	channelEnforcer := newMockChannelEnforcer()

	// We always use a batch ticker w/ a very long interval so it'll only
	// tick when we force one.
	auctioneer := NewAuctioneer(AuctioneerConfig{
		DB:                mockDB,
		Wallet:            wallet,
		ChainNotifier:     notifier,
		OrderFeed:         orderFeeder,
		StartingAcctValue: 1_000_000,
		BatchTicker:       NewIntervalAwareForceTicker(time.Hour * 24),
		CallMarket:        callMarket,
		BatchExecutor:     executor,
		ChannelEnforcer:   channelEnforcer,
	})

	return &auctioneerTestHarness{
		db:              mockDB,
		notifier:        notifier,
		auctioneer:      auctioneer,
		wallet:          wallet,
		orderFeed:       orderFeeder,
		callMarket:      callMarket,
		executor:        executor,
		channelEnforcer: channelEnforcer,
		t:               t,
	}
}

func (a *auctioneerTestHarness) StartAuctioneer() {
	a.t.Helper()

	if err := a.auctioneer.Start(); err != nil {
		a.t.Fatalf("unable to start auctioneer: %v", err)
	}
}

func (a *auctioneerTestHarness) StopAuctioneer() {
	a.t.Helper()

	if err := a.auctioneer.Stop(); err != nil {
		a.t.Fatalf("unable to stop auctioneer: %v", err)
	}
}

func (a *auctioneerTestHarness) AssertStateTransitions(states ...AuctionState) {
	a.t.Helper()

	// TODO(roasbeef): assert starting state?
	for _, state := range states {
		var nextState AuctionState
		select {
		case nextState = <-a.db.stateTransitions:
		case <-time.After(5 * time.Second):
			a.t.Fatalf("no state transition happened")
		}

		if nextState != state {
			a.t.Fatalf("expected transitiion to state=%v, "+
				"instead went to state=%v", state, nextState)
		}
	}
}

func (a *auctioneerTestHarness) AssertNoStateTransitions() {
	a.t.Helper()

	select {
	case state := <-a.db.stateTransitions:
		a.t.Fatalf("expected no transitions but now at state=%v", state)

	case <-time.After(time.Millisecond * 100):
		return
	}
}

func (a *auctioneerTestHarness) UpdateBalance(newBalance btcutil.Amount) {
	a.wallet.Lock()
	a.wallet.balance = newBalance
	a.wallet.Unlock()
}

func (a *auctioneerTestHarness) MineBlock(height int32) {
	a.notifier.BlockChan <- height
}

func (a *auctioneerTestHarness) AssertTxBroadcast() *wire.MsgTx {
	checkBroadcast := func() error {
		a.wallet.RLock()
		defer a.wallet.RUnlock()

		if len(a.wallet.lastTxs) != 1 {
			return fmt.Errorf("no tx broadcast")
		}

		return nil
	}

	err := wait.NoError(checkBroadcast, time.Second*5)
	if err != nil {
		a.t.Fatal(err)
	}

	a.wallet.RLock()
	defer a.wallet.RUnlock()
	return a.wallet.lastTxs[0]
}

func (a *auctioneerTestHarness) AssertNTxsBroadcast(n int) []*wire.MsgTx {
	var txs []*wire.MsgTx
	checkBroadcast := func() error {
		a.wallet.RLock()
		defer a.wallet.RUnlock()

		if len(a.wallet.lastTxs) != n {
			return fmt.Errorf("%d txs broadcast",
				len(a.wallet.lastTxs))
		}

		txs = a.wallet.lastTxs
		return nil
	}

	err := wait.NoError(checkBroadcast, time.Second*5)
	if err != nil {
		a.t.Fatal(err)
	}

	return txs
}

func (a *auctioneerTestHarness) RestartAuctioneer() {
	a.StopAuctioneer()

	a.db.state = DefaultState
	a.db.stateTransitions = make(chan AuctionState, 100)

	a.auctioneer = NewAuctioneer(AuctioneerConfig{
		DB:                a.db,
		Wallet:            a.wallet,
		ChainNotifier:     a.notifier,
		OrderFeed:         a.orderFeed,
		StartingAcctValue: 1_000_000,
		BatchTicker:       NewIntervalAwareForceTicker(time.Hour * 24),
	})

	a.StartAuctioneer()
}

func (a *auctioneerTestHarness) SendConf(txs ...*wire.MsgTx) {
	for _, tx := range txs {
		a.notifier.ConfChan <- &chainntnfs.TxConfirmation{
			Tx: tx,
		}
	}
}

func genAskOrder(fixedRate, duration uint32) (*order.Ask, error) {
	var nonce orderT.Nonce
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("unable to read nonce: %v", err)
	}

	kit := orderT.NewKit(nonce)
	kit.FixedRate = fixedRate
	kit.UnitsUnfulfilled = orderT.SupplyUnit(fixedRate * duration)

	return &order.Ask{
		Ask: orderT.Ask{
			Kit:         *kit,
			MaxDuration: duration,
		},
	}, nil
}

func genBidOrder(fixedRate, duration uint32) (*order.Bid, error) {
	var nonce orderT.Nonce
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("unable to read nonce: %v", err)
	}

	kit := orderT.NewKit(nonce)
	kit.FixedRate = fixedRate
	kit.UnitsUnfulfilled = orderT.SupplyUnit(fixedRate * duration)

	return &order.Bid{
		Bid: orderT.Bid{
			Kit:         *kit,
			MinDuration: duration,
		},
	}, nil
}

func (a *auctioneerTestHarness) NotifyAskOrder(fixedRate uint32,
	duration uint32) orderT.Nonce {

	askOrder, err := genAskOrder(fixedRate, duration)
	if err != nil {
		a.t.Fatalf("unable to gen ask order: %v", err)
	}
	if err := a.orderFeed.SendUpdate(&order.NewOrderUpdate{
		Order: askOrder,
	}); err != nil {
		a.t.Fatalf("unable to send order update: %v", err)
	}

	return askOrder.Nonce()
}

func (a *auctioneerTestHarness) NotifyBidOrder(fixedRate uint32,
	duration uint32) orderT.Nonce {

	bidOrder, err := genBidOrder(fixedRate, duration)
	if err != nil {
		a.t.Fatalf("unable to gen bid order: %v", err)
	}
	if err := a.orderFeed.SendUpdate(&order.NewOrderUpdate{
		Order: bidOrder,
	}); err != nil {
		a.t.Fatalf("unable to send order update: %v", err)
	}

	return bidOrder.Nonce()
}

func (a *auctioneerTestHarness) NotifyOrderCancel(asks, bids []orderT.Nonce) {
	for _, nonce := range bids {
		if err := a.orderFeed.SendUpdate(&order.CancelledOrderUpdate{
			Ask:   false,
			Nonce: nonce,
		}); err != nil {
			a.t.Fatalf("unable to send update: %v", err)
		}
	}

	for _, nonce := range asks {
		if err := a.orderFeed.SendUpdate(&order.CancelledOrderUpdate{
			Ask:   true,
			Nonce: nonce,
		}); err != nil {
			a.t.Fatalf("unable to send update: %v", err)
		}
	}
}

func (a *auctioneerTestHarness) AssertOrdersPresent(nonces ...orderT.Nonce) {
	a.t.Helper()
	err := wait.NoError(func() error {
		a.callMarket.Lock()
		defer a.callMarket.Unlock()

		for _, nonce := range nonces {
			if _, ok := a.callMarket.orders[nonce]; !ok {
				return fmt.Errorf("nonce %x not found",
					nonce[:])
			}
		}

		return nil
	}, time.Second*5)
	if err != nil {
		a.t.Fatal(err)
	}
}

func (a *auctioneerTestHarness) AssertNoOrdersPreesnt() {
	a.t.Helper()

	err := wait.NoError(func() error {
		a.callMarket.Lock()
		defer a.callMarket.Unlock()

		if len(a.callMarket.orders) > 0 {
			return fmt.Errorf("found orders in call market, " +
				"none should exist")
		}

		return nil
	}, time.Second*5)
	if err != nil {
		a.t.Fatal(err)
	}
}

func (a *auctioneerTestHarness) OrderFeederPause() {
	a.auctioneer.orderFeederSignals <- orderFeederPause
}

func (a *auctioneerTestHarness) OrderFeederResume() {
	a.auctioneer.orderFeederSignals <- orderFeederDeliver
}

func (a *auctioneerTestHarness) AddDiskOrder(isAsk bool, fixedRate uint32,
	duration uint32) orderT.Nonce {

	a.db.Lock()
	defer a.db.Unlock()

	var (
		order order.ServerOrder
		err   error
	)
	if isAsk {
		order, err = genAskOrder(fixedRate, duration)
	} else {
		order, err = genBidOrder(fixedRate, duration)
	}

	if err != nil {
		a.t.Fatalf("unable to gen order: %v", err)
	}

	nonce := order.Nonce()
	a.db.orders[nonce] = order

	return nonce
}

func (a *auctioneerTestHarness) AssertLifetimesEnforced() {
	a.t.Helper()

	a.channelEnforcer.Lock()
	defer a.channelEnforcer.Unlock()
	require.NotEmpty(a.t, a.channelEnforcer.lifetimePkgs)
}

func (a *auctioneerTestHarness) MarkBatchUnconfirmed(batchKey *btcec.PublicKey,
	tx *wire.MsgTx) {

	a.db.Lock()
	defer a.db.Unlock()

	bid := orderT.NewBatchID(batchKey)

	a.db.batchStates[bid] = false
	a.db.snapshots[bid] = tx
}

func (a *auctioneerTestHarness) AssertBatchConfirmed(batchKey *btcec.PublicKey) {
	err := wait.NoError(func() error {
		a.db.Lock()
		defer a.db.Unlock()

		bid := orderT.NewBatchID(batchKey)
		if _, ok := a.db.batchStates[bid]; !ok {
			return fmt.Errorf("batch %x still unconfirmed: ", bid[:])
		}

		return nil
	}, time.Second*5)
	if err != nil {
		a.t.Fatal(err)
	}
}

func (a *auctioneerTestHarness) QueueNoMarketClear() {
	a.callMarket.Lock()
	a.callMarket.shouldClear = false
	a.callMarket.Unlock()
}

func (a *auctioneerTestHarness) QueueMarketClear() {
	a.callMarket.Lock()
	a.callMarket.shouldClear = true
	a.callMarket.Unlock()
}

func (a *auctioneerTestHarness) ForceBatchTick() {
	a.auctioneer.cfg.BatchTicker.Force <- time.Time{}
}

func (a *auctioneerTestHarness) ReportExecutionFailure(err error) {
	a.executor.resChan <- &venue.ExecutionResult{
		Err: err,
	}
}

func (a *auctioneerTestHarness) ReportExecutionSuccess() {
	a.executor.Lock()
	batch := a.executor.submittedBatch
	a.executor.Unlock()

	a.executor.resChan <- &venue.ExecutionResult{
		Batch: batch,
		BatchTx: &wire.MsgTx{
			TxOut: []*wire.TxOut{
				{
					PkScript: key[:],
				},
			},
		},
		LifetimePackages: []*chanenforcement.LifetimePackage{
			{ChannelPoint: wire.OutPoint{Index: 1}},
		},
	}
}

func (a *auctioneerTestHarness) AssertBannedTrader(trader matching.AccountID) {
	a.t.Helper()

	if !a.db.isBannedTrader(trader) {
		a.t.Fatalf("trader %x not banned", trader)
	}
}

func (a *auctioneerTestHarness) AssertOrdersRemoved(nonces []orderT.Nonce) {
	a.t.Helper()

	for _, nonce := range nonces {
		_, ok := a.auctioneer.removedOrders[nonce]
		if !ok {
			a.t.Fatalf("order %v not removed", nonce)
		}
	}
}

// TestAuctioneerStateMachineDefaultAccountPresent tests that the state machine
// carries out the proper state transitions when the auctioneer account is
// already present on disk.
func TestAuctioneerStateMachineDefaultAccountPresent(t *testing.T) {
	t.Parallel()

	// We'll start our state with a pre-existing account to force the
	// expected state transition.
	testHarness := newAuctioneerTestHarness(t)
	err := testHarness.db.UpdateAuctioneerAccount(
		context.Background(),
		&account.Auctioneer{},
	)
	if err != nil {
		t.Fatalf("unable to add acct: %v", err)
	}

	testHarness.StartAuctioneer()
	defer testHarness.StopAuctioneer()

	// Upon startup, it should realize the we already have an auctioneer
	// account on disk, and transition to the OrderSubmitState phase.
	testHarness.AssertStateTransitions(OrderSubmitState)
}

// TestAuctioneerStateMachineMasterAcctInit tests that the auctioneer state
// machine is able to execute all the step required to create an auctioneer
// account within the chain. We also ensure it's able to resume the process at
// any of the marked states.
func TestAuctioneerStateMachineMasterAcctInit(t *testing.T) {
	t.Parallel()

	// First, we'll start up the auctioneer as normal.
	testHarness := newAuctioneerTestHarness(t)
	testHarness.StartAuctioneer()

	// As we don't have an account on disk, we expect the state machine to
	// transition to the NoMasterAcctState. It should end in this state, as
	// we have no coins in the main wallet.
	testHarness.AssertStateTransitions(NoMasterAcctState)

	// We'll now mine a block to simulate coins being deposited into the
	// wallet.
	balUpdate := btcutil.Amount(10_000)
	testHarness.UpdateBalance(balUpdate)
	testHarness.MineBlock(1)

	// However, the balance above wasn't enough, so we should go back to
	// NoMasterAcctState still.
	testHarness.AssertNoStateTransitions()

	// Mine another block, which adds enough coins to the backing wallet
	// for us to proceed.
	balUpdate = btcutil.Amount(10_000_000)
	testHarness.UpdateBalance(balUpdate)
	testHarness.MineBlock(2)

	// We should now proceed to the MasterAcctPending state, and should
	// have broadcasted the transaction to fund the master account along
	// the way.
	testHarness.AssertStateTransitions(MasterAcctPending)

	broadcastTx := testHarness.AssertTxBroadcast()

	// We simulate a restart now by closing down the old auctioneer, then
	// starting up a new one.
	testHarness.RestartAuctioneer()

	// At this point, it should transition all the way to MasterAcctPending
	// as we broadcasted the transaction before we went down.
	testHarness.AssertStateTransitions(MasterAcctPending)

	// Now we'll dispatch a confirmation to simulate the transaction being
	// confirmed.
	testHarness.SendConf(broadcastTx)

	// With the confirmation notification dispatched, we should now go to
	// the MasterAcctConfirmed state, then to the OrderSubmitState where we
	// terminate.
	testHarness.AssertStateTransitions(
		MasterAcctConfirmed, OrderSubmitState,
	)

	// At this point, we should have a new master account on disk.
	acct, err := testHarness.db.FetchAuctioneerAccount(context.Background())
	if err != nil {
		t.Fatalf("unable to fetch master account: %v", err)
	}

	if acct == nil {
		t.Fatalf("master account not updated: %v", err)
	}
}

// TestAuctioneerOrderFeederStates tests that the order feeder will properly
// handle transitioning between its two states of sending and caching order
// updates.
func TestAuctioneerOrderFeederStates(t *testing.T) {
	t.Parallel()

	// First, we'll start up the auctioneer as normal.
	testHarness := newAuctioneerTestHarness(t)
	testHarness.StartAuctioneer()

	// At this point, if add a new order, then we should see it reflected
	// in the call market, as the orders should be applied directly.
	orderPrice := uint32(100)
	orderDuration := uint32(10)
	askNonce := testHarness.NotifyAskOrder(orderPrice, orderDuration)
	bidNonce := testHarness.NotifyBidOrder(orderPrice, orderDuration)

	testHarness.AssertOrdersPresent(askNonce, bidNonce)

	// Now we'll cancel the order, which should result in it being removed
	// from the call market.
	testHarness.NotifyOrderCancel(
		[]orderT.Nonce{askNonce}, []orderT.Nonce{bidNonce},
	)
	testHarness.AssertNoOrdersPreesnt()

	// We'll now flip the state to orderFeederPause.
	testHarness.OrderFeederPause()

	// As a result of the state flip above, the next orders we add
	// shouldn't trigger any new updates.
	askNonce = testHarness.NotifyAskOrder(orderPrice, orderDuration)
	bidNonce = testHarness.NotifyBidOrder(orderPrice, orderDuration)

	// We'll now flip it back to the normal state and should find that the
	// call market has these new orders.
	testHarness.OrderFeederResume()
	testHarness.AssertOrdersPresent(askNonce, bidNonce)
}

// TestAuctioneerLoadDiskOrders tests that if the database contains a set of
// orders, upon start up, we'll have them all loaded into the call market.
func TestAuctioneerLoadDiskOrders(t *testing.T) {
	t.Parallel()

	// First, we'll start up the auctioneer as normal.
	testHarness := newAuctioneerTestHarness(t)

	// Before we start the harness, we'll load a few orders into the
	// database.
	const (
		numOrders            = 4
		orderPrice    uint32 = 100
		orderDuration uint32 = 10
	)
	orderNonces := make([]orderT.Nonce, 0, numOrders)
	for i := 0; i < numOrders; i++ {
		isAsk := i%2 == 0

		orderNonces = append(
			orderNonces,
			testHarness.AddDiskOrder(
				isAsk, orderPrice, orderDuration,
			),
		)
	}

	// We'll now start up the auctioneer itself.
	testHarness.StartAuctioneer()

	// At this point, we should find that all the orders we added above
	// have been loaded into the call market.
	testHarness.AssertOrdersPresent(orderNonces...)
}

// TestAuctioneerPendingBatchRebroadcast tests that if we come online, and
// there are pending batches on disk, then we'll re-broadcast them.
func TestAuctioneerPendingBatchRebroadcast(t *testing.T) {
	t.Parallel()

	const numPending = 3

	// First, we'll start up the auctioneer as normal.
	testHarness := newAuctioneerTestHarness(t)

	// We'll grab the current batch key, then increment it by every time we
	// create a new batch below.
	currentBatchKey := testHarness.db.batchKey

	var batchKeys []*btcec.PublicKey
	for i := 0; i < numPending; i++ {
		// Create a batch tx, using the script to encode what batch
		// this corresponds to.
		var s [32]byte
		copy(s[:], key[:])
		s[0] = byte(i)
		batchTx := &wire.MsgTx{
			TxOut: []*wire.TxOut{
				{
					PkScript: s[:],
				},
			},
		}

		// We'll now insert a pending batch snapshot and transaction,
		// marking it as unconfirmed.
		testHarness.MarkBatchUnconfirmed(currentBatchKey, batchTx)
		batchKeys = append(batchKeys, currentBatchKey)

		// Now that the new batch has been marked unconfirmed, we
		// increment the current batch key by one. The new batch key
		// will be the current batch key from the PoV of the
		// auctioneer.
		currentBatchKey = clmscript.IncrementKey(currentBatchKey)
		testHarness.db.batchKey = currentBatchKey
	}

	// Now that the batch is marked unconfirmed, we'll start up the
	// auctioneer. It should recognize this batch is still unconfirmed, and
	// publish the unconfirmed batch transactions again.
	testHarness.StartAuctioneer()
	broadcastTxs := testHarness.AssertNTxsBroadcast(numPending)

	if len(broadcastTxs) != numPending {
		t.Fatalf("expected %d transactions, found %d",
			numPending, len(broadcastTxs))
	}

	// The order of the broadcasted transactions should be the same as the
	// original ordering.
	for i, tx := range broadcastTxs {
		b := tx.TxOut[0].PkScript[0]
		if b != byte(i) {
			t.Fatalf("tx %d had script byte %d", i, b)
		}
	}

	// The auctioneer should now be waiting for these transactions to
	// confirm, so we'll dispatch a confirmation.
	testHarness.SendConf(broadcastTxs...)

	// At this point, the batch that was marked unconfirmed, should now
	// show up as being confirmed.
	for _, batchKey := range batchKeys {
		testHarness.AssertBatchConfirmed(batchKey)
	}
}

// TestAuctioneerBatchTickNoop tests that if a master account is present, and
// we're unable to make a market, then we just go back to the order submit
// state and await the next tick.
func TestAuctioneerBatchTickNoop(t *testing.T) {
	t.Parallel()

	// First, we'll start up the auctioneer as normal.
	testHarness := newAuctioneerTestHarness(t)

	// Before we start things, we'll insert a master account so we can skip
	// creating the master account and go straight to the order matching
	// phase.
	err := testHarness.db.UpdateAuctioneerAccount(
		context.Background(),
		&account.Auctioneer{},
	)
	if err != nil {
		t.Fatalf("unable to add acct: %v", err)
	}

	testHarness.StartAuctioneer()
	defer testHarness.StopAuctioneer()

	// We should now transition to the order submit state.
	testHarness.AssertStateTransitions(OrderSubmitState)

	// We haven't yet added any orders to the market, so we'll queue up a
	// signal that no market can cleared in this instance.
	testHarness.QueueNoMarketClear()

	// Next, we'll force a batch tick so the main state machine wakes up.
	testHarness.ForceBatchTick()

	// We should now go to the MatchMakingState, then back to the
	// OrderSubmitState as there's no market to be cleared.
	testHarness.AssertStateTransitions(MatchMakingState, OrderSubmitState)

	// There should be no further state transitions at this point.
	testHarness.AssertNoStateTransitions()
}

// TestAuctioneerMarketLifecycle tests the full life cycle of a successful
// auction clear. We'll come up, find a market to clear, then remove traders
// until execution is successful, finally ending with a broadcast of the bath
// execution transaction, and the batch being committed to disk.
func TestAuctioneerMarketLifecycle(t *testing.T) {
	t.Parallel()

	// First, we'll start up the auctioneer as normal.
	testHarness := newAuctioneerTestHarness(t)
	err := testHarness.db.UpdateAuctioneerAccount(
		context.Background(),
		&account.Auctioneer{},
	)
	if err != nil {
		t.Fatalf("unable to add acct: %v", err)
	}

	testHarness.StartAuctioneer()
	defer testHarness.StopAuctioneer()

	// We should now transition to the order submit state.
	testHarness.AssertStateTransitions(OrderSubmitState)

	// We'll now enter the main loop to execute a batch, but before that
	// we'll ensure all calls to the call market return a proper batch.
	testHarness.QueueMarketClear()

	// Now that the call market is set up, we'll trigger a batch force tick
	// to kick off this cycle. We should go from the MatchMakingState to
	// the BatchExecutionState.
	testHarness.ForceBatchTick()
	testHarness.AssertStateTransitions(MatchMakingState, BatchExecutionState)

	// At this point, the order feeder should be stopped, we'll simulate a
	// set of new orders being added while the market has been halted. At
	// the very end, these should be present in the cal market.
	const numNewOrders = 4
	newOrders := make([]orderT.Nonce, numNewOrders)
	for i := 0; i < numNewOrders; i++ {
		if i%2 == 0 {
			newOrders[i] = testHarness.NotifyAskOrder(20, 20)
			continue
		}

		newOrders[i] = testHarness.NotifyBidOrder(30, 30)
	}

	// In this scenario, we'll return an error that a sub-set of the
	// traders are missing.
	const numOrders = 10
	missingNonces := make([]orderT.Nonce, numOrders)
	for i := 0; i < numOrders; i++ {
		missingNonces[i] = testHarness.AddDiskOrder(i%2 == 0, 10, 10)
	}
	testHarness.ReportExecutionFailure(
		&venue.ErrMissingTraders{
			OrderNonces: map[orderT.Nonce]struct{}{
				missingNonces[0]: {},
				missingNonces[1]: {},
			},
		},
	)

	// We should now transition back to the match making state, then
	// finally execution to give things another go.
	testHarness.AssertStateTransitions(MatchMakingState, BatchExecutionState)

	// The set of orders referenced above should now have been removed from
	// the call market
	testHarness.AssertOrdersRemoved(missingNonces[:2])

	// This time, we'll report a failure that one of the traders gave us an
	// invalid witness.
	testHarness.ReportExecutionFailure(&venue.ErrInvalidWitness{
		OrderNonces: missingNonces[2:4],
	})

	// Once again, the set of orders should be removed, and we should step
	// again until we retry execution.
	testHarness.AssertStateTransitions(MatchMakingState, BatchExecutionState)
	testHarness.AssertOrdersRemoved(missingNonces[2:4])

	// We'll now simulate one of the traders failing to send a message in
	// time.
	testHarness.ReportExecutionFailure(&venue.ErrMsgTimeout{
		OrderNonces: missingNonces[4:6],
	})
	testHarness.AssertStateTransitions(MatchMakingState, BatchExecutionState)
	testHarness.AssertOrdersRemoved(missingNonces[4:6])

	// We'll now simulate one of the traders failing to include required
	// channel information.
	testHarness.ReportExecutionFailure(&venue.ErrMissingChannelInfo{
		Trader:       matching.AccountID{1},
		ChannelPoint: wire.OutPoint{Index: 1},
		OrderNonces:  missingNonces[6:8],
	})
	testHarness.AssertStateTransitions(MatchMakingState, BatchExecutionState)
	testHarness.AssertOrdersRemoved(missingNonces[6:8])

	// We'll now simulate one of the traders providing non-matching channel
	// information. Both traders should be banned and their orders removed.
	bannedTrader1 := matching.AccountID(toRawKey(randomPubKey(t)))
	bannedTrader2 := matching.AccountID(toRawKey(randomPubKey(t)))
	testHarness.ReportExecutionFailure(&venue.ErrNonMatchingChannelInfo{
		ChannelPoint: wire.OutPoint{Index: 1},
		Trader1:      bannedTrader1,
		Trader2:      bannedTrader2,
		OrderNonces:  missingNonces[8:10],
	})
	testHarness.AssertStateTransitions(MatchMakingState, BatchExecutionState)
	testHarness.AssertBannedTrader(bannedTrader1)
	testHarness.AssertBannedTrader(bannedTrader2)
	testHarness.AssertOrdersRemoved(missingNonces[8:10])

	// At long last, we're now ready to trigger a successful batch
	// execution.
	testHarness.ReportExecutionSuccess()

	// Now that the batch was successful, we should transition to the
	// BatchCommitState.
	testHarness.AssertStateTransitions(BatchCommitState)

	// In this state, we expect that the batch transaction was properly
	// broadcast and the channel lifetimes are being enforced.
	broadcastTx := testHarness.AssertTxBroadcast()
	testHarness.AssertLifetimesEnforced()

	// Now we trigger a confirmation, the batch should be marked as being
	// confirmed on disk.
	startingBatchKey := testHarness.db.batchKey
	testHarness.SendConf(broadcastTx)
	testHarness.AssertBatchConfirmed(startingBatchKey)

	// Finally, we should go back to the order submit state, where we'll
	// terminate the state machine, and await another batch tick.
	testHarness.AssertStateTransitions(OrderSubmitState)

	// At this point, there should be no further state transitions, as we
	// should be waiting for a new batch tick.
	testHarness.AssertNoStateTransitions()

	// Along the way, we should've resumed the order feeder, so the orders
	// that we added after we transitioned from the order submit phase
	// should now be a part of the call market.
	testHarness.AssertOrdersPresent(newOrders...)

	// We'll now tick again, but this time with an "empty" call market to
	// ensure we can process another tick right after processing a batch.
	testHarness.QueueNoMarketClear()
	testHarness.ForceBatchTick()

	// We should go to the match making state, then back to the order
	// submit state as we can't make a market with things as is, then make
	// no further state transitions.
	testHarness.AssertStateTransitions(MatchMakingState, OrderSubmitState)

	// Also all the orders that we removed earlier should now also be once
	// again part of the call market.
	testHarness.AssertOrdersPresent(missingNonces...)

	testHarness.AssertNoStateTransitions()
}
