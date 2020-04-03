package agora

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
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

type mockAuctioneerState struct {
	sync.RWMutex

	state AuctionState

	stateTransitions chan AuctionState

	acct *account.Auctioneer
}

func newMockAuctioneerState() *mockAuctioneerState {
	return &mockAuctioneerState{
		stateTransitions: make(chan AuctionState, 100),
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
		return nil, agoradb.ErrNoAuctioneerAccount
	}

	return m.acct, nil
}

func (m *mockAuctioneerState) BatchKey(context.Context) (*btcec.PublicKey, error) {
	m.RLock()
	defer m.RUnlock()

	return pubKey, nil
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

var _ AuctioneerDatabase = (*mockAuctioneerState)(nil)

type mockWallet struct {
	sync.RWMutex

	balance btcutil.Amount

	lastTx *wire.MsgTx
}

func (m *mockWallet) SendOutputs(cctx context.Context, outputs []*wire.TxOut,
	_ chainfee.SatPerKWeight) (*wire.MsgTx, error) {

	m.Lock()
	defer m.Unlock()

	m.lastTx = wire.NewMsgTx(2)
	m.lastTx.TxIn = []*wire.TxIn{
		{},
	}
	m.lastTx.TxOut = outputs

	return m.lastTx, nil
}

func (m *mockWallet) ConfirmedWalletBalance(context.Context) (btcutil.Amount, error) {
	m.RLock()
	defer m.RUnlock()

	return m.balance, nil
}

func (m *mockWallet) ListTransactions(context.Context) ([]*wire.MsgTx, error) {
	m.RLock()
	defer m.RUnlock()

	if m.lastTx == nil {
		return nil, nil
	}

	return []*wire.MsgTx{m.lastTx}, nil
}

func (m *mockWallet) DeriveKey(context.Context, *keychain.KeyLocator) (
	*keychain.KeyDescriptor, error) {

	return &keychain.KeyDescriptor{
		PubKey: pubKey,
	}, nil
}

var _ Wallet = (*mockWallet)(nil)

type auctioneerTestHarness struct {
	t *testing.T

	db *mockAuctioneerState

	notifier *account.MockChainNotifier

	wallet *mockWallet

	auctioneer *Auctioneer
}

func newAuctioneerTestHarness(t *testing.T) *auctioneerTestHarness {
	mockDB := newMockAuctioneerState()
	wallet := &mockWallet{}
	notifier := account.NewMockChainNotifier()

	auctioneer := NewAuctioneer(AuctioneerConfig{
		DB:                mockDB,
		Wallet:            wallet,
		ChainNotifier:     notifier,
		StartingAcctValue: 1_000_000,
	})

	return &auctioneerTestHarness{
		db:         mockDB,
		notifier:   notifier,
		auctioneer: auctioneer,
		wallet:     wallet,
		t:          t,
	}
}

func (a *auctioneerTestHarness) StartAuctioneer() {
	a.t.Helper()

	if err := a.auctioneer.Start(); err != nil {
		a.t.Fatalf("unable to start aucitoneer: %v", err)
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
		nextState := <-a.db.stateTransitions

		if nextState != state {
			a.t.Fatalf("expected transitiion to state=%v, "+
				"instead went to state=%v", state, nextState)
		}
	}
}

func (a *auctioneerTestHarness) AssertNoStateTransitions() {
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
	a.wallet.RLock()
	defer a.wallet.RUnlock()

	if a.wallet.lastTx == nil {
		a.t.Fatalf("no transaction broadcast")
	}

	return a.wallet.lastTx
}

func (a *auctioneerTestHarness) RestartAuctioneer() {
	a.StopAuctioneer()

	a.db.state = DefaultState
	a.db.stateTransitions = make(chan AuctionState, 100)

	a.auctioneer = NewAuctioneer(AuctioneerConfig{
		DB:                a.db,
		Wallet:            a.wallet,
		ChainNotifier:     a.notifier,
		StartingAcctValue: 1_000_000,
	})

	a.StartAuctioneer()
}

func (a *auctioneerTestHarness) SendConf(tx *wire.MsgTx) {
	a.notifier.ConfChan <- &chainntnfs.TxConfirmation{
		Tx: tx,
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
	testHarness.AssertStateTransitions(
		NoMasterAcctState, MasterAcctPending,
	)

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
