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
	"github.com/lightninglabs/lndclient"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/feebump"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/subscribe"
	"github.com/lightningnetwork/lnd/sweep"
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

	defaultFeeSchedule = terms.NewLinearFeeSchedule(1, 1000)
)

func randomPubKey(t *testing.T) *btcec.PublicKey {
	var testPriv [32]byte
	if _, err := rand.Read(testPriv[:]); err != nil {
		t.Fatalf("could not create private key: %v", err)
	}

	_, pub := btcec.PrivKeyFromBytes(btcec.S256(), testPriv[:])
	return pub
}

type fetchStateReq struct {
	resp chan AuctionState
}

type mockAuctioneerState struct {
	sync.RWMutex

	state            AuctionState
	stateTransitions chan AuctionState

	// We use a channel to synchronize acccess to the state, such that we
	// can fetch the current state even though we are waiting for a state
	// transition to happen.
	stateSem       chan struct{}
	fetchStateChan chan *fetchStateReq

	acct *account.Auctioneer

	orders map[orderT.Nonce]order.ServerOrder

	batchKey *btcec.PublicKey

	// batchStates tracks the current state for a batch. True means the
	// batch is confirmed.
	batchStates map[orderT.BatchID]bool

	snapshots map[orderT.BatchID]*subastadb.BatchSnapshot

	bannedAccounts map[matching.AccountID]struct{}

	quit chan struct{}
}

func newMockAuctioneerState(batchKey *btcec.PublicKey,
	bufferStateTransitions bool) *mockAuctioneerState {

	stateTransitionsBuffer := 0
	if bufferStateTransitions {
		stateTransitionsBuffer = 100
	}

	// Initialize semaphore with one element.
	stateSem := make(chan struct{}, 1)
	stateSem <- struct{}{}

	return &mockAuctioneerState{
		state:            DefaultState{},
		batchKey:         batchKey,
		stateTransitions: make(chan AuctionState, stateTransitionsBuffer),
		fetchStateChan:   make(chan *fetchStateReq),
		stateSem:         stateSem,
		orders:           make(map[orderT.Nonce]order.ServerOrder),
		batchStates:      make(map[orderT.BatchID]bool),
		snapshots:        make(map[orderT.BatchID]*subastadb.BatchSnapshot),
		bannedAccounts:   make(map[matching.AccountID]struct{}),
		quit:             make(chan struct{}),
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
	// To update the auction state, we must obtain the exclusive state
	// access semaphore.
	select {
	case <-m.stateSem:
	case <-m.quit:
		return fmt.Errorf("mockAuctioneerState exiting")
	}
	defer func() {
		m.stateSem <- struct{}{}
	}()

	for {
		select {
		// When the state transition is read, we can update
		// our state variable and return.
		case m.stateTransitions <- state:
			m.state = state
			return nil

		// If a request to read the current state comes in, we return
		// the old state, as the state transition hasn't been triggered
		// yet.
		case req := <-m.fetchStateChan:
			req.resp <- m.state

		case <-m.quit:
			return fmt.Errorf("mockAuctioneerState exiting")
		}
	}
}

func (m *mockAuctioneerState) AuctionState() (AuctionState, error) {
	req := &fetchStateReq{
		resp: make(chan AuctionState, 1),
	}

	select {
	// If we get the semaphore we have exclusive access to the state and
	// can return it directly.
	case <-m.stateSem:
		defer func() {
			m.stateSem <- struct{}{}
		}()

		return m.state, nil

	// Otherwise some other goroutine has the semaphore, so we ask it to
	// return the state to us.
	case m.fetchStateChan <- req:
		return <-req.resp, nil

	case <-m.quit:
		return nil, fmt.Errorf("mockAuctioneerState exiting")
	}
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
	bid orderT.BatchID) (*subastadb.BatchSnapshot, error) {

	m.Lock()
	defer m.Unlock()

	s, ok := m.snapshots[bid]
	if !ok {
		return nil, fmt.Errorf("unable to find snapshot")
	}

	return s, nil
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

func (m *mockAuctioneerState) IsTraderBanned(_ context.Context, accountKey,
	_ [33]byte, _ uint32) (bool, error) {

	m.Lock()
	defer m.Unlock()

	return m.isBannedTrader(accountKey), nil
}

var _ AuctioneerDatabase = (*mockAuctioneerState)(nil)

type mockWallet struct {
	sync.RWMutex

	balance btcutil.Amount

	lastTxs []*wire.MsgTx
}

func (m *mockWallet) SendOutputs(cctx context.Context, outputs []*wire.TxOut,
	_ chainfee.SatPerKWeight, _ string) (*wire.MsgTx, error) {

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

func (m *mockWallet) ListTransactions(context.Context, int32, int32) (
	[]lndclient.Transaction, error) {

	m.RLock()
	defer m.RUnlock()

	transactions := make([]lndclient.Transaction, len(m.lastTxs))
	for idx, tx := range m.lastTxs {
		transactions[idx] = lndclient.Transaction{Tx: tx}
	}
	return transactions, nil
}

func (m *mockWallet) PublishTransaction(ctx context.Context, tx *wire.MsgTx,
	label string) error {

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
	bids []*order.Bid
	asks []*order.Ask

	shouldClear bool

	sync.Mutex
}

func newMockCallMarket() *mockCallMarket {
	return &mockCallMarket{}
}

func (m *mockCallMarket) MaybeClear(_ chainfee.SatPerKWeight,
	_ matching.AccountCacher, _ []matching.MatchPredicate) (
	*matching.OrderBatch, error) {

	m.Lock()
	defer m.Unlock()

	if !m.shouldClear {
		return nil, matching.ErrNoMarketPossible
	}

	if len(m.bids) == 0 {
		return nil, fmt.Errorf("no market to clear")
	}

	// The mock will just match each bid to one ask.
	if len(m.bids) != len(m.asks) {
		return nil, fmt.Errorf("only supports same number of bids/asks")
	}

	matches := make([]matching.MatchedOrder, 0, len(m.asks))
	for idx, ask := range m.asks {
		bid := m.bids[idx]

		// Matched volume will be the minimum of the two orders.
		vol := bid.UnitsUnfulfilled
		if ask.UnitsUnfulfilled < vol {
			vol = ask.UnitsUnfulfilled
		}

		match := matching.MatchedOrder{
			Asker: matching.Trader{
				AccountKey: ask.AcctKey,
				AccountOutPoint: wire.OutPoint{
					Index: uint32(idx*2 + 1),
				},
				BatchKey: toRawKey(pubKey),
				NextBatchKey: toRawKey(
					poolscript.IncrementKey(pubKey),
				),
				AccountBalance: btcutil.SatoshiPerBitcoin,
			},
			Bidder: matching.Trader{
				AccountKey: bid.AcctKey,
				AccountOutPoint: wire.OutPoint{
					Index: uint32(idx*2 + 2),
				},
				BatchKey: toRawKey(pubKey),
				NextBatchKey: toRawKey(
					poolscript.IncrementKey(pubKey),
				),
				AccountBalance: btcutil.SatoshiPerBitcoin,
			},
			Details: matching.OrderPair{
				Bid: bid,
				Ask: ask,
				Quote: matching.PriceQuote{
					UnitsMatched:     vol,
					TotalSatsCleared: vol.ToSatoshis(),
				},
			},
		}

		matches = append(matches, match)
	}

	var priceClearer matching.LastAcceptedBid
	matchSet := &matching.MatchSet{
		MatchedOrders: matches,
	}
	clearingPrice, err := priceClearer.ExtractClearingPrice(matchSet)
	if err != nil {
		return nil, fmt.Errorf("unable to extract clearing price: %v",
			err)
	}

	return &matching.OrderBatch{
		Orders: matches,
		FeeReport: matching.NewTradingFeeReport(
			matches, defaultFeeSchedule, clearingPrice,
		),
		ClearingPrice: clearingPrice,
	}, nil
}

func (m *mockCallMarket) RemoveMatches(matches ...matching.MatchedOrder) error {
	m.Lock()
	defer m.Unlock()

	for _, match := range matches {
		vol := match.Details.Quote.UnitsMatched

		bidNonce := match.Details.Bid.Nonce()
		bid, ok := m.getBid(bidNonce)
		if !ok {
			return fmt.Errorf("bid not found")
		}

		bid.UnitsUnfulfilled -= vol
		if bid.UnitsUnfulfilled == 0 {
			m.removeBid(bidNonce)
		}

		askNonce := match.Details.Ask.Nonce()
		ask, ok := m.getAsk(askNonce)
		if !ok {
			return fmt.Errorf("ask not found")
		}

		ask.UnitsUnfulfilled -= vol
		if ask.UnitsUnfulfilled == 0 {
			m.removeAsk(askNonce)
		}
	}

	return nil
}

func (m *mockCallMarket) ConsiderBids(bids ...*order.Bid) error {
	m.Lock()
	defer m.Unlock()

	m.bids = append(m.bids, bids...)

	return nil
}

func (m *mockCallMarket) ForgetBids(nonces ...orderT.Nonce) error {
	m.Lock()
	defer m.Unlock()

	for _, nonce := range nonces {
		m.removeBid(nonce)
	}

	return nil
}

func (m *mockCallMarket) getBid(nonce orderT.Nonce) (*order.Bid, bool) {
	for _, bid := range m.bids {
		if bid.Nonce() == nonce {
			return bid, true
		}
	}

	return nil, false
}

func (m *mockCallMarket) removeBid(nonce orderT.Nonce) {
	for i := 0; i < len(m.bids); i++ {
		if m.bids[i].Nonce() == nonce {
			m.bids = append(m.bids[:i], m.bids[i+1:]...)
			i--
		}
	}
}

func (m *mockCallMarket) ConsiderAsks(asks ...*order.Ask) error {
	m.Lock()
	defer m.Unlock()

	m.asks = append(m.asks, asks...)

	return nil
}

func (m *mockCallMarket) ForgetAsks(nonces ...orderT.Nonce) error {
	m.Lock()
	defer m.Unlock()

	for _, nonce := range nonces {
		m.removeAsk(nonce)
	}

	return nil
}

func (m *mockCallMarket) getAsk(nonce orderT.Nonce) (*order.Ask, bool) {
	for _, ask := range m.asks {
		if ask.Nonce() == nonce {
			return ask, true
		}
	}

	return nil, false
}

func (m *mockCallMarket) removeAsk(nonce orderT.Nonce) {
	for i := 0; i < len(m.asks); i++ {
		if m.asks[i].Nonce() == nonce {
			m.asks = append(m.asks[:i], m.asks[i+1:]...)
			i--
		}
	}
}

var _ matching.BatchAuctioneer = (*mockCallMarket)(nil)

type mockBatchExecutor struct {
	submittedBatch *batchtx.ExecutionContext
	resChan        chan *venue.ExecutionResult

	sync.Mutex
}

func newMockBatchExecutor() *mockBatchExecutor {
	return &mockBatchExecutor{
		resChan: make(chan *venue.ExecutionResult, 1),
	}
}

func (m *mockBatchExecutor) Submit(exeCtx *batchtx.ExecutionContext) (
	chan *venue.ExecutionResult, error) {

	m.Lock()
	defer m.Unlock()

	m.submittedBatch = exeCtx

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

func newAuctioneerTestHarness(t *testing.T,
	bufferStateTransitions bool) *auctioneerTestHarness {

	mockDB := newMockAuctioneerState(pubKey, bufferStateTransitions)
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
		TraderRejected:    matching.NewNodeConflictPredicate(),
		FundingConflicts:  matching.NewNodeConflictPredicate(),
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

	// We must first "quit" the underlying mock DB to allow any DB writes
	// by the auctioneer to return.
	close(a.db.quit)

	if err := a.auctioneer.Stop(); err != nil {
		a.t.Fatalf("unable to stop auctioneer: %v", err)
	}
}

func (a *auctioneerTestHarness) AssertStateTransitions(
	states ...AuctionState) AuctionState {

	a.t.Helper()

	// TODO(roasbeef): assert starting state?
	var nextState AuctionState
	for _, state := range states {
		select {
		case nextState = <-a.db.stateTransitions:
		case <-time.After(5 * time.Second):
			a.t.Fatalf("no state transition happened")
		}

		if nextState.String() != state.String() {
			a.t.Fatalf("expected transitiion to state=%v, "+
				"instead went to state=%v", state, nextState)
		}
	}

	return nextState
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

	a.db.state = DefaultState{}
	a.db.stateTransitions = make(chan AuctionState, 100)
	a.db.quit = make(chan struct{})

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

func genAskOrder(fixedRate, duration uint32) (*order.Ask, error) { // nolint:dupl
	var nonce orderT.Nonce
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("unable to read nonce: %v", err)
	}

	kit := orderT.NewKit(nonce)
	kit.FixedRate = fixedRate
	kit.UnitsUnfulfilled = orderT.SupplyUnit(fixedRate * duration)
	kit.LeaseDuration = duration

	var acctPrivKey [32]byte
	if _, err := rand.Read(acctPrivKey[:]); err != nil {
		return nil, fmt.Errorf("could not create private key: %v", err)
	}
	_, acctPubKey := btcec.PrivKeyFromBytes(btcec.S256(), acctPrivKey[:])
	kit.AcctKey = toRawKey(acctPubKey)

	return &order.Ask{
		Ask: orderT.Ask{
			Kit: *kit,
		},
		Kit: order.Kit{
			MultiSigKey: kit.AcctKey,
		},
	}, nil
}

func genBidOrder(fixedRate, duration uint32) (*order.Bid, error) { // nolint:dupl
	var nonce orderT.Nonce
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("unable to read nonce: %v", err)
	}

	kit := orderT.NewKit(nonce)
	kit.FixedRate = fixedRate
	kit.UnitsUnfulfilled = orderT.SupplyUnit(fixedRate * duration)
	kit.LeaseDuration = duration

	var acctPrivKey [32]byte
	if _, err := rand.Read(acctPrivKey[:]); err != nil {
		return nil, fmt.Errorf("could not create private key: %v", err)
	}
	_, acctPubKey := btcec.PrivKeyFromBytes(btcec.S256(), acctPrivKey[:])
	kit.AcctKey = toRawKey(acctPubKey)

	return &order.Bid{
		Bid: orderT.Bid{
			Kit: *kit,
		},
		Kit: order.Kit{
			MultiSigKey: kit.AcctKey,
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
			if _, ok := a.callMarket.getBid(nonce); ok {
				continue
			}
			if _, ok := a.callMarket.getAsk(nonce); ok {
				continue
			}
			return fmt.Errorf("nonce %x not found",
				nonce[:])
		}

		return nil
	}, time.Second*5)
	if err != nil {
		a.t.Fatal(err)
	}
}

func (a *auctioneerTestHarness) AssertOrdersNotPresent(nonces ...orderT.Nonce) {
	a.t.Helper()
	err := wait.NoError(func() error {
		a.callMarket.Lock()
		defer a.callMarket.Unlock()

		for _, nonce := range nonces {
			if _, ok := a.callMarket.getBid(nonce); ok {
				return fmt.Errorf("nonce %x found in call "+
					"market", nonce[:])
			}
			if _, ok := a.callMarket.getAsk(nonce); ok {
				return fmt.Errorf("nonce %x found in call "+
					"market", nonce[:])
			}
		}

		return nil
	}, time.Second*5)
	if err != nil {
		a.t.Fatal(err)
	}
}

func (a *auctioneerTestHarness) AssertSingleOrder(nonce orderT.Nonce,
	f func(order.ServerOrder) error) {

	a.t.Helper()

	a.callMarket.Lock()
	defer a.callMarket.Unlock()

	if bid, ok := a.callMarket.getBid(nonce); ok {
		err := f(bid)
		if err != nil {
			a.t.Fatal(err)
		}
		return
	}
	if ask, ok := a.callMarket.getAsk(nonce); ok {
		err := f(ask)
		if err != nil {
			a.t.Fatal(err)
		}
		return
	}

	a.t.Fatalf("nonce %x not found", nonce[:])
}

func (a *auctioneerTestHarness) AssertNoOrdersPreesnt() {
	a.t.Helper()

	err := wait.NoError(func() error {
		a.callMarket.Lock()
		defer a.callMarket.Unlock()

		if len(a.callMarket.bids) > 0 || len(a.callMarket.asks) > 0 {
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
	duration uint32) order.ServerOrder {

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

	return order
}

func (a *auctioneerTestHarness) AssertLifetimesEnforced() {
	a.t.Helper()

	a.channelEnforcer.Lock()
	defer a.channelEnforcer.Unlock()
	require.NotEmpty(a.t, a.channelEnforcer.lifetimePkgs)
}

func (a *auctioneerTestHarness) MarkBatchUnconfirmed(bid orderT.BatchID,
	snapshot *subastadb.BatchSnapshot) {

	a.db.Lock()
	defer a.db.Unlock()

	a.db.batchStates[bid] = false
	a.db.snapshots[bid] = snapshot
}

func (a *auctioneerTestHarness) AssertBatchConfirmed(batchKey *btcec.PublicKey) {
	a.t.Helper()

	err := wait.NoError(func() error {
		a.db.Lock()
		defer a.db.Unlock()

		bid := orderT.NewBatchID(batchKey)
		confirmed, ok := a.db.batchStates[bid]
		if !ok {
			return fmt.Errorf("batch %x not found: ", bid[:])
		}

		if !confirmed {
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

func (a *auctioneerTestHarness) AssertSubmittedBatch(numOrders int) {
	a.t.Helper()

	err := wait.NoError(func() error {
		a.executor.Lock()
		defer a.executor.Unlock()

		n := len(a.executor.submittedBatch.OrderBatch.Orders)
		if numOrders != n {
			return fmt.Errorf("submitted batch didn't have "+
				"the expected(%d) number of orders: %d",
				numOrders, n)
		}

		return nil
	}, time.Second*5)
	if err != nil {
		a.t.Fatal(err)
	}
}

func (a *auctioneerTestHarness) AssertSubmittedBatchFeeRate(
	feeRate chainfee.SatPerKWeight) {

	a.t.Helper()

	err := wait.NoError(func() error {
		a.executor.Lock()
		defer a.executor.Unlock()

		f := a.executor.submittedBatch.BatchFeeRate
		if f != feeRate {
			return fmt.Errorf("submitted batch didn't have "+
				"the expected(%v) targeted fee rate: %v",
				feeRate, f)
		}

		return nil
	}, time.Second*5)
	if err != nil {
		a.t.Fatal(err)
	}

}

func (a *auctioneerTestHarness) AssertFinalizedBatch(numOrders int) {
	a.t.Helper()

	n := len(a.auctioneer.finalizedBatch.Batch.Orders)
	if numOrders != n {
		a.t.Fatalf("finalized batch didn't have the expected(%d) "+
			"number of orders: %d", numOrders, n)
	}
}

func (a *auctioneerTestHarness) ReportExecutionSuccess() {
	a.executor.Lock()
	exeCtx := a.executor.submittedBatch
	a.executor.Unlock()

	snapshot := &subastadb.BatchSnapshot{
		BatchTx:    exeCtx.ExeTx,
		BatchTxFee: exeCtx.FeeInfoEstimate.Fee,
		OrderBatch: exeCtx.OrderBatch,
	}

	// On reported execution success, the batch will be found as
	// unconfirmed in the DB.
	a.MarkBatchUnconfirmed(exeCtx.BatchID, snapshot)

	a.db.Lock()
	a.db.batchKey = poolscript.IncrementKey(a.db.batchKey)
	a.db.Unlock()

	a.executor.resChan <- &venue.ExecutionResult{
		Batch:   exeCtx.OrderBatch,
		BatchTx: exeCtx.ExeTx,
		FeeInfo: exeCtx.FeeInfoEstimate,
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
	testHarness := newAuctioneerTestHarness(t, true)
	err := testHarness.db.UpdateAuctioneerAccount(
		context.Background(), &account.Auctioneer{
			AuctioneerKey: &keychain.KeyDescriptor{
				PubKey: pubKey,
			},
			BatchKey: toRawKey(pubKey),
		},
	)
	if err != nil {
		t.Fatalf("unable to add acct: %v", err)
	}

	testHarness.StartAuctioneer()
	defer testHarness.StopAuctioneer()

	// Upon startup, it should realize the we already have an auctioneer
	// account on disk, and transition to the OrderSubmitState phase.
	testHarness.AssertStateTransitions(OrderSubmitState{})
}

// TestAuctioneerStateMachineMasterAcctInit tests that the auctioneer state
// machine is able to execute all the step required to create an auctioneer
// account within the chain. We also ensure it's able to resume the process at
// any of the marked states.
func TestAuctioneerStateMachineMasterAcctInit(t *testing.T) {
	t.Parallel()

	// First, we'll start up the auctioneer as normal.
	testHarness := newAuctioneerTestHarness(t, true)
	testHarness.StartAuctioneer()
	defer testHarness.StopAuctioneer()

	// As we don't have an account on disk, we expect the state machine to
	// transition to the NoMasterAcctState. It should end in this state, as
	// we have no coins in the main wallet.
	testHarness.AssertStateTransitions(NoMasterAcctState{})

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
	testHarness.AssertStateTransitions(MasterAcctPending{})

	broadcastTx := testHarness.AssertTxBroadcast()

	// We simulate a restart now by closing down the old auctioneer, then
	// starting up a new one.
	testHarness.RestartAuctioneer()

	// At this point, it should transition all the way to MasterAcctPending
	// as we broadcasted the transaction before we went down.
	testHarness.AssertStateTransitions(MasterAcctPending{})

	// Now we'll dispatch a confirmation to simulate the transaction being
	// confirmed.
	testHarness.SendConf(broadcastTx)

	// With the confirmation notification dispatched, we should now go to
	// the MasterAcctConfirmed state, then to the OrderSubmitState{} where we
	// terminate.
	testHarness.AssertStateTransitions(
		MasterAcctConfirmed{}, OrderSubmitState{},
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
	testHarness := newAuctioneerTestHarness(t, true)
	testHarness.StartAuctioneer()
	defer testHarness.StopAuctioneer()

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
	testHarness := newAuctioneerTestHarness(t, true)

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
			).Nonce(),
		)
	}

	// We'll now start up the auctioneer itself.
	testHarness.StartAuctioneer()
	defer testHarness.StopAuctioneer()

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
	testHarness := newAuctioneerTestHarness(t, true)

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
		bid := orderT.NewBatchID(currentBatchKey)
		testHarness.MarkBatchUnconfirmed(bid, &subastadb.BatchSnapshot{
			BatchTx: batchTx,
		})
		batchKeys = append(batchKeys, currentBatchKey)

		// Now that the new batch has been marked unconfirmed, we
		// increment the current batch key by one. The new batch key
		// will be the current batch key from the PoV of the
		// auctioneer.
		currentBatchKey = poolscript.IncrementKey(currentBatchKey)
		testHarness.db.batchKey = currentBatchKey
	}

	// Now that the batch is marked unconfirmed, we'll start up the
	// auctioneer. It should recognize this batch is still unconfirmed, and
	// publish the unconfirmed batch transactions again.
	testHarness.StartAuctioneer()
	defer testHarness.StopAuctioneer()

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
	testHarness := newAuctioneerTestHarness(t, true)

	// Before we start things, we'll insert a master account so we can skip
	// creating the master account and go straight to the order matching
	// phase.
	err := testHarness.db.UpdateAuctioneerAccount(
		context.Background(), &account.Auctioneer{
			AuctioneerKey: &keychain.KeyDescriptor{
				PubKey: pubKey,
			},
			BatchKey: toRawKey(pubKey),
		},
	)
	if err != nil {
		t.Fatalf("unable to add acct: %v", err)
	}

	testHarness.StartAuctioneer()
	defer testHarness.StopAuctioneer()

	// We should now transition to the order submit state.
	testHarness.AssertStateTransitions(OrderSubmitState{})

	// We haven't yet added any orders to the market, so we'll queue up a
	// signal that no market can cleared in this instance.
	testHarness.QueueNoMarketClear()

	// Next, we'll force a batch tick so the main state machine wakes up.
	testHarness.ForceBatchTick()

	// We should now go to FeeEstimationState, the MatchMakingState, then
	// back to the OrderSubmitState as there's no market to be cleared.
	testHarness.AssertStateTransitions(
		FeeEstimationState{}, MatchMakingState{}, OrderSubmitState{},
	)

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
	testHarness := newAuctioneerTestHarness(t, true)
	err := testHarness.db.UpdateAuctioneerAccount(
		context.Background(), &account.Auctioneer{
			AuctioneerKey: &keychain.KeyDescriptor{
				PubKey: pubKey,
			},
			BatchKey: toRawKey(pubKey),
		},
	)
	if err != nil {
		t.Fatalf("unable to add acct: %v", err)
	}

	testHarness.StartAuctioneer()
	defer testHarness.StopAuctioneer()

	// We should now transition to the order submit state.
	testHarness.AssertStateTransitions(OrderSubmitState{})

	const numOrders = 14
	nonces := make([]orderT.Nonce, numOrders)
	for i := 0; i < numOrders; i++ {
		fixedRate := uint32(10)
		duration := uint32(10)

		// To ensure partial matches have their remaining volume found
		// in the order book after successful batch execution, we'll
		// let the last bid order have a smaller volume by setting a
		// shorter duration (since the generated orders have units
		// fixedRate*duration).
		if i == numOrders-1 {
			duration = 5
		}

		var o order.ServerOrder
		if i%2 == 0 {
			o = testHarness.AddDiskOrder(true, fixedRate, duration)
			err := testHarness.callMarket.ConsiderAsks(o.(*order.Ask))
			if err != nil {
				t.Fatalf("unable to consider ask: %v", err)
			}
		} else {
			o = testHarness.AddDiskOrder(false, fixedRate, duration)
			err := testHarness.callMarket.ConsiderBids(o.(*order.Bid))
			if err != nil {
				t.Fatalf("unable to consider ask: %v", err)
			}
		}

		nonces[i] = o.Nonce()
	}

	// We'll now enter the main loop to execute a batch, but before that
	// we'll ensure all calls to the call market return a proper batch.
	testHarness.QueueMarketClear()

	// Now that the call market is set up, we'll trigger a batch force tick
	// to kick off this cycle. We should go from the FeeEstimationState, to
	// the MatchMakingState, via FeeCheckState, to the BatchExecutionState.
	testHarness.ForceBatchTick()
	testHarness.AssertStateTransitions(
		FeeEstimationState{}, MatchMakingState{}, FeeCheckState{},
		BatchExecutionState{},
	)

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

	// The matchmaking should have resulted in 7 matches.
	testHarness.AssertSubmittedBatch(7)

	// In this scenario, we'll return an error that a sub-set of the
	// traders are missing.
	testHarness.ReportExecutionFailure(
		&venue.ErrMissingTraders{
			OrderNonces: map[orderT.Nonce]struct{}{
				nonces[0]: {},
				nonces[1]: {},
			},
		},
	)

	// We should now transition back to fee estimation, match making, and
	// fee check state, then finally execution to give things another go.
	testHarness.AssertStateTransitions(
		FeeEstimationState{}, MatchMakingState{}, FeeCheckState{},
		BatchExecutionState{},
	)

	// Since two orders failed on the previous execution, there should be 6
	// matches left.
	testHarness.AssertSubmittedBatch(6)

	// The set of orders referenced above should now have been removed from
	// the call market
	testHarness.AssertOrdersRemoved(nonces[:2])

	// This time, we'll report a failure that one of the traders gave us an
	// invalid witness.
	testHarness.ReportExecutionFailure(&venue.ErrInvalidWitness{
		OrderNonces: nonces[2:4],
	})

	// Once again, the set of orders should be removed, and we should step
	// again until we retry execution.
	testHarness.AssertStateTransitions(
		FeeEstimationState{}, MatchMakingState{}, FeeCheckState{},
		BatchExecutionState{},
	)
	testHarness.AssertOrdersRemoved(nonces[2:4])

	// Now only eight orders are left, resulting in 5 matches.
	testHarness.AssertSubmittedBatch(5)

	// We'll now simulate one of the traders failing to send a message in
	// time.
	testHarness.ReportExecutionFailure(&venue.ErrMsgTimeout{
		OrderNonces: nonces[4:6],
	})
	testHarness.AssertStateTransitions(
		FeeEstimationState{}, MatchMakingState{}, FeeCheckState{},
		BatchExecutionState{},
	)
	testHarness.AssertOrdersRemoved(nonces[4:6])

	// Now only six orders are left, resulting in 4 matches.
	testHarness.AssertSubmittedBatch(4)

	// We'll now simulate one of the traders failing to include required
	// channel information.
	testHarness.ReportExecutionFailure(&venue.ErrMissingChannelInfo{
		Trader:       matching.AccountID{1},
		ChannelPoint: wire.OutPoint{Index: 1},
		OrderNonces:  nonces[6:8],
	})
	testHarness.AssertStateTransitions(
		FeeEstimationState{}, MatchMakingState{}, FeeCheckState{},
		BatchExecutionState{},
	)
	testHarness.AssertOrdersRemoved(nonces[6:8])
	testHarness.AssertSubmittedBatch(3)

	// We'll now simulate one of the traders providing non-matching channel
	// information. Both traders should be banned and their orders removed.
	bannedTrader1 := matching.AccountID(toRawKey(randomPubKey(t)))
	bannedTrader2 := matching.AccountID(toRawKey(randomPubKey(t)))
	testHarness.ReportExecutionFailure(&venue.ErrNonMatchingChannelInfo{
		ChannelPoint: wire.OutPoint{Index: 1},
		Trader1:      bannedTrader1,
		Trader2:      bannedTrader2,
		OrderNonces:  nonces[8:10],
	})

	testHarness.AssertStateTransitions(
		FeeEstimationState{}, MatchMakingState{}, FeeCheckState{},
		BatchExecutionState{},
	)
	testHarness.AssertBannedTrader(bannedTrader1)
	testHarness.AssertBannedTrader(bannedTrader2)
	testHarness.AssertOrdersRemoved(nonces[8:10])
	testHarness.AssertSubmittedBatch(2)

	// In this last scenario, we'll return an error that a sub-set of the
	// traders rejected the orders.
	testHarness.executor.Lock()
	rejectedPair := testHarness.executor.submittedBatch.OrderBatch.Orders[0]
	testHarness.executor.Unlock()

	rejectNonce1 := rejectedPair.Details.Ask.Nonce()
	rejectNonce2 := rejectedPair.Details.Bid.Nonce()
	require.Equal(t, nonces[10], rejectNonce1)
	require.Equal(t, nonces[11], rejectNonce2)
	testHarness.ReportExecutionFailure(&venue.ErrReject{
		RejectingTraders: map[matching.AccountID]*venue.OrderRejectMap{
			rejectedPair.Bidder.AccountKey: {
				FullReject: &venue.Reject{
					Type:   venue.FullRejectUnknown,
					Reason: "mismatch",
				},
				OwnOrders: []orderT.Nonce{rejectNonce1},
			},
			rejectedPair.Asker.AccountKey: {
				FullReject: &venue.Reject{
					Type:   venue.FullRejectUnknown,
					Reason: "mismatch",
				},
				OwnOrders: []orderT.Nonce{rejectNonce2},
			},
		},
	})

	// We should now transition back to the fee estimation state, match
	// making state, fee check state, then finally execution to give things
	// another go.
	testHarness.AssertStateTransitions(
		FeeEstimationState{}, MatchMakingState{}, FeeCheckState{},
		BatchExecutionState{},
	)
	testHarness.AssertOrdersRemoved(nonces[10:12])

	// Only one possible match is left.
	testHarness.AssertSubmittedBatch(1)

	// At long last, we're now ready to trigger a successful batch
	// execution.
	startingBatchKey := testHarness.db.batchKey
	testHarness.ReportExecutionSuccess()

	// Now that the batch was successful, we should transition to the
	// BatchCommitState.
	testHarness.AssertStateTransitions(BatchCommitState{})

	// Make sure the finalize batch is the same one as the one that was
	// submitted last.
	testHarness.AssertFinalizedBatch(1)

	// In this state, we expect that the batch transaction was properly
	// broadcast and the channel lifetimes are being enforced.
	broadcastTx := testHarness.AssertTxBroadcast()
	testHarness.AssertLifetimesEnforced()

	// Now we trigger a confirmation, the batch should be marked as being
	// confirmed on disk.
	testHarness.SendConf(broadcastTx)
	testHarness.AssertBatchConfirmed(startingBatchKey)

	// Finally, we should go back to the order submit state, where we'll
	// terminate the state machine, and await another batch tick.
	testHarness.AssertStateTransitions(OrderSubmitState{})

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

	// We should go to the fee estimation state, the match making state,
	// then back to the order submit state as we can't make a market with
	// things as is, then make no further state transitions.
	testHarness.AssertStateTransitions(
		FeeEstimationState{}, MatchMakingState{}, OrderSubmitState{},
	)

	// Also all the orders that we removed earlier, should now also be once
	// again part of the call market. Note that also the last ask should
	// still be present, as it was only partially matched.
	testHarness.AssertOrdersPresent(nonces[:numOrders-1]...)

	// The last bid should not be found as it was fully matched.
	testHarness.AssertOrdersNotPresent(nonces[numOrders-1])

	// Make sure the ask order that matched with the smaller bid is still
	// in the call market, and has had 50 of its 100 units matched.
	testHarness.AssertSingleOrder(
		nonces[numOrders-2], func(o order.ServerOrder) error {
			exp := orderT.SupplyUnit(50)
			if o.Details().UnitsUnfulfilled != exp {
				return fmt.Errorf("expected %v rem units, had "+
					"%v", exp, o.Details().UnitsUnfulfilled)
			}

			return nil
		},
	)

	testHarness.AssertNoStateTransitions()
}

// TestAuctioneerAllowAccountUpdate tests whether the auctioneer allows account
// updates throughout the batch lifecycle.
func TestAuctioneerAllowAccountUpdate(t *testing.T) {
	t.Parallel()

	// First, we'll start up the auctioneer as normal.
	testHarness := newAuctioneerTestHarness(t, false)

	// Before we start things, we'll insert a master account so we can skip
	// creating the master account and go straight to the order matching
	// phase.
	err := testHarness.db.UpdateAuctioneerAccount(
		context.Background(), &account.Auctioneer{
			AuctioneerKey: &keychain.KeyDescriptor{
				PubKey: pubKey,
			},
			BatchKey: toRawKey(pubKey),
		},
	)
	require.NoErrorf(t, err, "unable to update auctioneer account: %v", err)

	go testHarness.StartAuctioneer() // nolint:staticcheck
	defer testHarness.StopAuctioneer()

	// We should now transition to the order submit state.
	testHarness.AssertStateTransitions(OrderSubmitState{})

	// We'll submit two orders (an ask and bid pair) to clear a market and
	// enter the batch execution phase.
	const fixedRate = 10
	const duration = 10
	ask := testHarness.AddDiskOrder(true, fixedRate, duration)
	err = testHarness.callMarket.ConsiderAsks(ask.(*order.Ask))
	require.NoErrorf(t, err, "unable to consider ask: %v", err)
	bid := testHarness.AddDiskOrder(false, fixedRate, duration)
	err = testHarness.callMarket.ConsiderBids(bid.(*order.Bid))
	require.NoErrorf(t, err, "unable to consider bid: %v", err)

	testHarness.QueueMarketClear()

	// Now that the call market is set up, we'll trigger a batch force tick
	// to kick off this cycle. We should immediately go to the
	// FeeEstimationState.
	testHarness.ForceBatchTick()
	testHarness.AssertStateTransitions(FeeEstimationState{})

	fakeAcct := matching.AccountID(toRawKey(randomPubKey(t)))
	askAcct := matching.AccountID(ask.Details().AcctKey)
	bidAcct := matching.AccountID(bid.Details().AcctKey)
	checkNoAcctUpdate := func() {
		require.False(t, testHarness.auctioneer.AllowAccountUpdate(fakeAcct))
		require.False(t, testHarness.auctioneer.AllowAccountUpdate(askAcct))
		require.False(t, testHarness.auctioneer.AllowAccountUpdate(bidAcct))
	}

	// At this point, no account updates should be allowed for any account.
	checkNoAcctUpdate()

	// Neither should any update be allowed in MatchMaking or FeeCheck
	// states.
	testHarness.AssertStateTransitions(MatchMakingState{})
	checkNoAcctUpdate()

	testHarness.AssertStateTransitions(FeeCheckState{})
	checkNoAcctUpdate()

	// There should be no further state transitions at this point.
	testHarness.AssertStateTransitions(BatchExecutionState{})

	// Now, we can perform our final assertions. The fake account should be
	// allowed an update as it's not part of the batch, but the ask and bid
	// accounts shouldn't.
	require.True(t, testHarness.auctioneer.AllowAccountUpdate(fakeAcct))
	require.False(t, testHarness.auctioneer.AllowAccountUpdate(askAcct))
	require.False(t, testHarness.auctioneer.AllowAccountUpdate(bidAcct))
}

// TestAuctioneerRequestBatchFeeBump tests that we can request the fee rate to
// be used for the next batch, and that the batch will be submittd with this
// fee rate. It also checks that if a fee bump is requested, but no market can
// be made, an empty batch is created to bump the fee of the unconfirmed
// batches.
func TestAuctioneerRequestBatchFeeBump(t *testing.T) {
	t.Parallel()

	// First, we'll start up the auctioneer as normal.
	testHarness := newAuctioneerTestHarness(t, false)

	// Before we start things, we'll insert a master account so we can skip
	// creating the master account and go straight to the order matching
	// phase.
	err := testHarness.db.UpdateAuctioneerAccount(
		context.Background(), &account.Auctioneer{
			AuctioneerKey: &keychain.KeyDescriptor{
				PubKey: pubKey,
			},
			BatchKey: toRawKey(pubKey),
			Balance:  10_000_000,
		},
	)
	require.NoErrorf(t, err, "unable to update auctioneer account: %v", err)

	go testHarness.StartAuctioneer() // nolint:staticcheck
	defer testHarness.StopAuctioneer()

	// We should now transition to the order submit state.
	testHarness.AssertStateTransitions(OrderSubmitState{})

	// Helper method to clear market and check the following state
	// transitions.
	clearMarket := func(emptyBatch, expectExecution bool,
		targetEffRate chainfee.SatPerKWeight) {

		t.Helper()

		// If we are clearing an empty batch, the number of matches
		// will be 0 obviously. We'll queue a no market clear.
		numExpectedMatches := 0
		testHarness.QueueNoMarketClear()

		// If this is not an empty batch, we'll submit two orders (an
		// ask and bid pair) and queue a market clear.
		if !emptyBatch {
			const fixedRate = 10
			const duration = 10
			ask := testHarness.AddDiskOrder(true, fixedRate, duration)
			err = testHarness.callMarket.ConsiderAsks(ask.(*order.Ask))
			require.NoErrorf(t, err, "unable to consider ask: %v", err)
			bid := testHarness.AddDiskOrder(false, fixedRate, duration)
			err = testHarness.callMarket.ConsiderBids(bid.(*order.Bid))
			require.NoErrorf(t, err, "unable to consider bid: %v", err)

			numExpectedMatches = 1
			testHarness.QueueMarketClear()
		}

		// Now that the call market is set up, we'll trigger a batch
		// force tick to kick off this cycle. We should immediately go
		// to the FeeEstimationState.
		testHarness.ForceBatchTick()
		testHarness.AssertStateTransitions(FeeEstimationState{})

		// If we didn't expect the batch to be executed, the state
		// machine should go to MatchMaking->FeeCheck and then back to
		// OrderSubmit.
		if !expectExecution {
			testHarness.AssertStateTransitions(
				MatchMakingState{}, FeeCheckState{},
				OrderSubmitState{},
			)
			return
		}

		// Otherwise we should cycle through the states MatchMaking ->
		// FeeCheck until the target fee rate for the match is met.
		timeout := time.After(10 * time.Second)
		for {
			state := testHarness.AssertStateTransitions(
				MatchMakingState{}, FeeCheckState{},
			)

			feeCheck := state.(FeeCheckState)
			effFeeRates := feebump.CalcEffectiveFeeRates(
				feeCheck.pendingBatches,
			)

			// When the batches meet the target fee rate we expect
			// execution to start.
			effRate := effFeeRates[len(effFeeRates)-1]
			if effRate >= targetEffRate {
				break
			}

			select {
			case <-timeout:
				t.Fatalf("effective fee rate %v never met, "+
					"last rate %v", targetEffRate, effRate)
			default:
			}
		}

		testHarness.AssertStateTransitions(BatchExecutionState{})
		testHarness.AssertSubmittedBatch(numExpectedMatches)

		// Resport execution success, which should eventually take us
		// back to OrderSubmitState.
		testHarness.ReportExecutionSuccess()
		testHarness.AssertStateTransitions(
			BatchCommitState{}, OrderSubmitState{},
		)
	}

	// We'll request the next batch to have a custom fee rate.
	customFeeRate := chainfee.SatPerKWeight(12345)
	err = testHarness.auctioneer.RequestBatchFeeBump(sweep.FeePreference{
		FeeRate: customFeeRate,
	})
	require.NoError(t, err, "unable to request fee bump")

	// Clear the market and expect the submitted batch to have our custom
	// fee rate.
	clearMarket(false, true, customFeeRate)

	// The submitted batch should have the exact fee rate we requested
	// earlier.
	testHarness.AssertSubmittedBatchFeeRate(customFeeRate)

	// Confirm this batch, so it won't impact the next fee estimate.
	broadcastTx := testHarness.AssertTxBroadcast()
	testHarness.SendConf(broadcastTx)

	// Clear a new market, and make sure this batch won't have our custom
	// fee rate, as it should only apply for a single batch.
	clearMarket(false, true, chainfee.FeePerKwFloor)
	testHarness.AssertSubmittedBatchFeeRate(chainfee.FeePerKwFloor)

	// At this point a single batch should be unconfirmed.
	ub, err := testHarness.auctioneer.UnconfirmedBatches(
		context.Background(),
	)
	require.NoError(t, err, "unable to get unconfirmed batches")
	if len(ub) != 1 {
		t.Fatalf("expected single unconfirmed batch, found %d", len(ub))
	}

	// We request a fee bump and clear the market again.
	err = testHarness.auctioneer.RequestBatchFeeBump(sweep.FeePreference{
		FeeRate: customFeeRate,
	})
	require.NoError(t, err, "unable to request fee bump")
	clearMarket(false, true, customFeeRate)

	// Since the last submitted batch is bumping the fee of the other
	// unconfirmed batche, it will have a batch fee rate that is higher
	// than its effective fee rate.
	// TODO(halseth): do the calculation instead of hardcoding this?
	testHarness.AssertSubmittedBatchFeeRate(20595)

	// Now we request a fee bump even though there is no market to clear.
	// This should result in an empty batch.
	err = testHarness.auctioneer.RequestBatchFeeBump(sweep.FeePreference{
		FeeRate: 4 * customFeeRate,
	})
	require.NoError(t, err, "unable to request fee bump")
	clearMarket(true, true, 4*customFeeRate)

	// Finally we try to bump the fee again, but the fee we request is less
	// than the current effective feerate of the unconfirmed batches. Since
	// there is no market to be made, no batch should be created.
	err = testHarness.auctioneer.RequestBatchFeeBump(sweep.FeePreference{
		FeeRate: customFeeRate,
	})
	require.NoError(t, err, "unable to request fee bump")
	clearMarket(true, false, customFeeRate)
}
