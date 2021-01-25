package itest

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	"github.com/go-errors/errors"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/pool/auctioneerrpc"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/subasta"
	auctioneerAccount "github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightningnetwork/lnd"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
)

var (
	harnessNetParams = &chaincfg.RegressionNetParams

	// lastPort is the last port determined to be free for use by a new
	// node. It should be used atomically.
	lastPort uint32 = defaultNodePort
)

const (
	minerMempoolTimeout = lntest.MinerMempoolTimeout
	defaultWaitTimeout  = lntest.DefaultTimeout

	// defaultNodePort is the start of the range for listening ports of
	// harness nodes. Ports are monotonically increasing starting from this
	// number and are determined by the results of nextAvailablePort(). The
	// start port should be distinct from lntest's one to not get a conflict
	// with the lnd nodes that are also started.
	defaultNodePort = 19655

	// defaultTimeout is a timeout that will be used for various wait
	// scenarios where no custom timeout value is defined.
	defaultTimeout = time.Second * 5

	defaultOrderDuration uint32 = 2016
)

// testCase is a struct that holds a single test case.
type testCase struct {
	name               string
	test               func(t *harnessTest)
	skipMasterAcctInit bool // nolint:structcheck
}

// harnessTest wraps a regular testing.T providing enhanced error detection
// and propagation. All error will be augmented with a full stack-trace in
// order to aid in debugging. Additionally, any panics caused by active
// test cases will also be handled and represented as fatals.
type harnessTest struct {
	t *testing.T

	// testCase is populated during test execution and represents the
	// current test case.
	testCase *testCase

	// lndHarness is a reference to the current network harness. Will be
	// nil if not yet set up.
	lndHarness *lntest.NetworkHarness

	auctioneer *auctioneerHarness

	trader *traderHarness
}

// newHarnessTest creates a new instance of a harnessTest from a regular
// testing.T instance.
func newHarnessTest(t *testing.T, net *lntest.NetworkHarness,
	auctioneer *auctioneerHarness, trader *traderHarness) *harnessTest {

	return &harnessTest{t, nil, net, auctioneer, trader}
}

// Skipf calls the underlying testing.T's Skip method, causing the current test
// to be skipped.
func (h *harnessTest) Skipf(format string, args ...interface{}) {
	h.t.Skipf(format, args...)
}

// Fatalf causes the current active test case to fail with a fatal error. All
// integration tests should mark test failures solely with this method due to
// the error stack traces it produces.
func (h *harnessTest) Fatalf(format string, a ...interface{}) {
	if h.lndHarness != nil {
		h.lndHarness.SaveProfilesPages()
	}

	stacktrace := errors.Wrap(fmt.Sprintf(format, a...), 1).ErrorStack()

	if h.testCase != nil {
		h.t.Fatalf("Failed: (%v): exited with error: \n"+
			"%v", h.testCase.name, stacktrace)
	} else {
		h.t.Fatalf("Error outside of test: %v", stacktrace)
	}
}

// RunTestCase executes a harness test case. Any errors or panics will be
// represented as fatal.
func (h *harnessTest) RunTestCase(testCase *testCase) {
	h.testCase = testCase
	defer func() {
		h.testCase = nil
	}()

	defer func() {
		if err := recover(); err != nil {
			description := errors.Wrap(err, 2).ErrorStack()
			h.t.Fatalf("Failed: (%v) panicked with: \n%v",
				h.testCase.name, description)
		}
	}()

	testCase.test(h)
}

func (h *harnessTest) Logf(format string, args ...interface{}) {
	h.t.Logf(format, args...)
}

func (h *harnessTest) Log(args ...interface{}) {
	h.t.Log(args...)
}

// shutdown stops both the auction and trader server.
func (h *harnessTest) shutdown() error {
	// Allow both server and client to stop but only return the first error
	// that occurs.
	err := h.trader.stop(true)
	err2 := h.auctioneer.stop()
	if err != nil {
		return err
	}
	return err2
}

// restartServer stops the auctioneer server and then starts it again, forcing
// all connected traders to reconnect.
func (h *harnessTest) restartServer() {
	err := h.auctioneer.halt()
	if err != nil {
		h.t.Fatalf("could not halt auctioneer server: %v", err)
	}

	// Wait a few milliseconds to make sure a client reconnect backoff is
	// triggered.
	time.Sleep(300 * time.Millisecond)

	err = prepareServerConnection(h.auctioneer, true)
	if err != nil {
		h.t.Fatalf("could not recreate server connection: %v", err)
	}
}

// prepareServerConnection creates a new connection in the auctioneer server
// that clients can connect to. This should only be called once after any
// (re)start of the auctioneer.
func prepareServerConnection(ah *auctioneerHarness, isRestart bool) error {
	if isRestart {
		return ah.runServer()
	}
	return ah.start()
}

// nextAvailablePort returns the first port that is available for listening by
// a new node. It panics if no port is found and the maximum available TCP port
// is reached.
func nextAvailablePort() int {
	port := atomic.AddUint32(&lastPort, 1)
	for port < 65535 {
		// If there are no errors while attempting to listen on this
		// port, close the socket and return it as available. While it
		// could be the case that some other process picks up this port
		// between the time the socket is closed and it's reopened in
		// the harness node, in practice in CI servers this seems much
		// less likely than simply some other process already being
		// bound at the start of the tests.
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		l, err := net.Listen("tcp4", addr)
		if err == nil {
			err := l.Close()
			if err == nil {
				return int(port)
			}
		}
		port = atomic.AddUint32(&lastPort, 1)
	}

	// No ports available? Must be a mistake.
	panic("no ports available for listening")
}

// setupHarnesses creates new server and client harnesses that are connected
// to each other through an in-memory gRPC connection.
func setupHarnesses(t *testing.T, lndHarness *lntest.NetworkHarness) (
	*traderHarness, *auctioneerHarness) {

	// Create the two harnesses but don't start them yet, they need to be
	// connected first.
	auctioneerHarness, err := newAuctioneerHarness(auctioneerConfig{
		BackendCfg: lndHarness.BackendCfg,
		NetParams:  harnessNetParams,
		LndNode:    lndHarness.Alice,
	})
	if err != nil {
		t.Fatalf("could not create auction server: %v", err)
	}

	// Create a new internal connection in the auctioneer server.
	err = prepareServerConnection(auctioneerHarness, false)
	if err != nil {
		t.Fatalf("could not create auctioneer connection: %v", err)
	}

	// Create a trader that uses Bob and connect it to the auction server.
	traderHarness := setupTraderHarness(
		t, lndHarness.BackendCfg, lndHarness.Bob, auctioneerHarness,
	)
	return traderHarness, auctioneerHarness
}

// setupTraderHarness creates a new trader that connects to the given lnd node
// and to the given auction server.
func setupTraderHarness(t *testing.T, backend lntest.BackendConfig,
	node *lntest.HarnessNode, auctioneer *auctioneerHarness,
	opts ...traderCfgOpt) *traderHarness {

	traderHarness, err := newTraderHarness(traderConfig{
		AuctionServer: auctioneer.serverCfg.RPCListen,
		ServerTLSPath: auctioneer.serverCfg.TLSCertPath,
		BackendCfg:    backend,
		NetParams:     harnessNetParams,
		LndNode:       node,
	}, opts)
	if err != nil {
		t.Fatalf("could not create trader server: %v", err)
	}

	// Start the trader harness now.
	err = traderHarness.start()
	if err != nil {
		t.Fatalf("could not start trader server: %v", err)
	}
	return traderHarness
}

// isMempoolEmpty checks whether the mempool remains empty for the given
// timeout.
func isMempoolEmpty(miner *rpcclient.Client, timeout time.Duration) (bool, error) {
	breakTimeout := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var err error
	var mempool []*chainhash.Hash
	for {
		select {
		case <-breakTimeout:
			return true, nil

		case <-ticker.C:
			mempool, err = miner.GetRawMempool()
			if err != nil {
				return false, err
			}
			if len(mempool) > 0 {
				return false, nil
			}
		}
	}
}

// waitForNTxsInMempool polls until finding the desired number of transactions
// in the provided miner's mempool. An error is returned if this number is not
// met after the given timeout.
func waitForNTxsInMempool(miner *rpcclient.Client, n int,
	timeout time.Duration) ([]*chainhash.Hash, error) {

	breakTimeout := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var err error
	var mempool []*chainhash.Hash
	for {
		select {
		case <-breakTimeout:
			return nil, fmt.Errorf("wanted %v, found %v txs "+
				"in mempool: %v", n, len(mempool), mempool)
		case <-ticker.C:
			mempool, err = miner.GetRawMempool()
			if err != nil {
				return nil, err
			}

			if len(mempool) == n {
				return mempool, nil
			}
		}
	}
}

// assertTxInBlock checks that a given transaction can be found in the block's
// transaction list.
func assertTxInBlock(t *harnessTest, block *wire.MsgBlock,
	txid *chainhash.Hash) *wire.MsgTx {

	for _, tx := range block.Transactions {
		sha := tx.TxHash()
		if bytes.Equal(txid[:], sha[:]) {
			return tx
		}
	}

	t.Fatalf("tx was not included in block")
	return nil
}

// mineBlocks mine 'num' of blocks and check that blocks are present in
// node blockchain. numTxs should be set to the number of transactions
// (excluding the coinbase) we expect to be included in the first mined block.
func mineBlocks(t *harnessTest, net *lntest.NetworkHarness,
	num uint32, numTxs int) []*wire.MsgBlock {

	// If we expect transactions to be included in the blocks we'll mine,
	// we wait here until they are seen in the miner's mempool.
	var txids []*chainhash.Hash
	var err error
	if numTxs > 0 {
		txids, err = waitForNTxsInMempool(
			net.Miner.Node, numTxs, minerMempoolTimeout,
		)
		if err != nil {
			t.Fatalf("unable to find txns in mempool: %v", err)
		}
	}

	blocks := make([]*wire.MsgBlock, num)

	blockHashes, err := net.Miner.Node.Generate(num)
	if err != nil {
		t.Fatalf("unable to generate blocks: %v", err)
	}

	for i, blockHash := range blockHashes {
		block, err := net.Miner.Node.GetBlock(blockHash)
		if err != nil {
			t.Fatalf("unable to get block: %v", err)
		}

		blocks[i] = block
	}

	// Finally, assert that all the transactions were included in the first
	// block.
	for _, txid := range txids {
		assertTxInBlock(t, blocks[0], txid)
	}

	return blocks
}

// assertTraderAccount asserts that the account with the corresponding trader
// key is found in the given state.
func assertTraderAccount(t *harnessTest, trader *traderHarness,
	traderKey []byte, value btcutil.Amount, expiry uint32,
	state poolrpc.AccountState) {

	ctx := context.Background()
	err := wait.NoError(func() error {
		list, err := trader.ListAccounts(
			ctx, &poolrpc.ListAccountsRequest{},
		)
		if err != nil {
			return fmt.Errorf("unable to retrieve accounts: %v", err)
		}

		for _, a := range list.Accounts {
			if !bytes.Equal(a.TraderKey, traderKey) {
				continue
			}
			if btcutil.Amount(a.Value) != value {
				return fmt.Errorf("expected account value %v, "+
					"got %v", value, btcutil.Amount(a.Value))
			}
			if a.ExpirationHeight != expiry {
				return fmt.Errorf("expected account expiry %v, "+
					"got %v", expiry, a.ExpirationHeight)
			}
			if a.State != state {
				return fmt.Errorf("expected account state %v, "+
					"got %v", state, a.State)
			}

			return nil
		}

		return errors.New("account not found")
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf(err.Error())
	}
}

// assertTraderAccountState asserts that the account with the corresponding
// trader key is found in the given state from the PoV of the trader.
func assertTraderAccountState(t *testing.T, trader *traderHarness,
	traderKey []byte, state poolrpc.AccountState) {

	t.Helper()

	ctx := context.Background()
	err := wait.NoError(func() error {
		list, err := trader.ListAccounts(
			ctx, &poolrpc.ListAccountsRequest{},
		)
		if err != nil {
			return fmt.Errorf("unable to retrieve accounts: %v", err)
		}

		for _, a := range list.Accounts {
			if !bytes.Equal(a.TraderKey, traderKey) {
				continue
			}
			if a.State != state {
				return fmt.Errorf("expected account state %v, "+
					"got %v", state, a.State)
			}

			return nil
		}

		return errors.New("account not found")
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf(err.Error())
	}
}

// assertAuctioneerAccount asserts that the account with the corresponding
// trader key is found in the given state from the PoV of the auctioneer.
func assertAuctioneerAccount(t *harnessTest, rawTraderKey []byte,
	value btcutil.Amount, state auctioneerAccount.State) {

	traderKey, err := btcec.ParsePubKey(rawTraderKey, btcec.S256())
	if err != nil {
		t.Fatalf(err.Error())
	}

	ctx := context.Background()
	err = wait.NoError(func() error {
		account, err := t.auctioneer.store.Account(ctx, traderKey, true)
		if err != nil {
			return fmt.Errorf("unable to retrieve account: %v", err)
		}

		if account.Value != value {
			return fmt.Errorf("expected account value %v, got %v",
				value, account.Value)
		}
		if account.State != state {
			return fmt.Errorf("expected account state %v, got %v",
				state, account.State)
		}

		return nil
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf(err.Error())
	}
}

// assertAuctioneerAccountState asserts that the account with the corresponding
// trader key is found in any of the given states from the PoV of the auctioneer.
func assertAuctioneerAccountState(t *harnessTest, rawTraderKey []byte,
	states ...auctioneerAccount.State) {

	traderKey, err := btcec.ParsePubKey(rawTraderKey, btcec.S256())
	if err != nil {
		t.Fatalf(err.Error())
	}

	ctx := context.Background()
	err = wait.NoError(func() error {
		account, err := t.auctioneer.store.Account(ctx, traderKey, true)
		if err != nil {
			return fmt.Errorf("unable to retrieve account: %v", err)
		}

		foundState := false
		for _, state := range states {
			if account.State == state {
				foundState = true
			}
		}
		if !foundState {
			return fmt.Errorf("expected account state %v, got %v",
				states, account.State)
		}

		return nil
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf(err.Error())
	}
}

// assertAuctionState asserts that the auctioneer is in the given state.
func assertAuctionState(t *harnessTest, state subasta.AuctionState) {
	ctx := context.Background()
	err := wait.NoError(func() error {
		status, err := t.auctioneer.AuctionStatus(
			ctx, &adminrpc.EmptyRequest{},
		)
		if err != nil {
			return fmt.Errorf("unable to retrieve status: %v", err)
		}

		if status.AuctionState != state.String() {
			return fmt.Errorf("expected auction state %v, got %v",
				state, status.AuctionState)
		}

		return nil
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf(err.Error())
	}
}

// openAccountAndAssert creates a new trader account, mines its funding TX and
// waits for it to be confirmed.
func openAccountAndAssert(t *harnessTest, trader *traderHarness,
	req *poolrpc.InitAccountRequest) *poolrpc.Account {

	// Add the default conf target of the CLI to the request if it wasn't
	// set. This removes the need for every test to specify the value
	// explicitly.
	if req.Fees == nil {
		req.Fees = &poolrpc.InitAccountRequest_ConfTarget{ConfTarget: 6}
	}

	acct, err := trader.InitAccount(context.Background(), req)
	if err != nil {
		t.Fatalf("could not create account: %v", err)
	}

	// At this point the account should be funded but the status should be
	// pending until the TX is confirmed.
	if acct.State != poolrpc.AccountState_PENDING_OPEN {
		t.Fatalf("unexpected account state. got %d, expected %d",
			acct.State, poolrpc.AccountState_PENDING_OPEN)
	}
	assertAuctioneerAccountState(
		t, acct.TraderKey, auctioneerAccount.StatePendingOpen,
	)

	// Mine the account funding TX and make sure the account outpoint was
	// actually included in the block.
	block := mineBlocks(t, t.lndHarness, 6, 1)[0]
	txHash, err := chainhash.NewHash(acct.Outpoint.Txid)
	if err != nil {
		t.Fatalf("could not create chain hash from outpoint: %v", err)
	}
	_ = assertTxInBlock(t, block, txHash)

	assertTraderAccountState(
		t.t, trader, acct.TraderKey, poolrpc.AccountState_OPEN,
	)
	assertAuctioneerAccountState(
		t, acct.TraderKey, auctioneerAccount.StateOpen,
	)

	return acct
}

// closeAccountAndAssert closes an existing trader account by broadcasting a
// a spending transaction of the account. This spending transaction may take
// either the expiration or multi-sig path, depending on whether the account has
// already expired. Once the spending transaction confirms, we assert that the
// account is marked as closed.
func closeAccountAndAssert(t *harnessTest, trader *traderHarness,
	req *poolrpc.CloseAccountRequest) *wire.MsgTx {

	t.t.Helper()

	// If a destination for the funds was not provided in the request, we'll
	// use the default.
	if req.FundsDestination == nil {
		req.FundsDestination = &poolrpc.CloseAccountRequest_OutputWithFee{
			OutputWithFee: &poolrpc.OutputWithFee{
				Fees: &poolrpc.OutputWithFee_FeeRateSatPerKw{
					FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
				},
			},
		}
	}

	// Send the close account request and wait for the closing transaction
	// to be broadcast. The account should also be found in a
	// StatePendingClosed state.
	resp, err := trader.CloseAccount(context.Background(), req)
	if err != nil {
		t.Fatalf("could not close account %x: %v", req.TraderKey, err)
	}

	_, err = waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.t.Fatal(err)
	}

	assertTraderAccountState(
		t.t, trader, req.TraderKey, poolrpc.AccountState_PENDING_CLOSED,
	)
	assertAuctioneerAccountState(
		t, req.TraderKey, auctioneerAccount.StateOpen,
		auctioneerAccount.StateExpired,
	)

	// Mine the closing transaction and make sure it was included in a
	// block.
	block := mineBlocks(t, t.lndHarness, 1, 1)[0]
	closeTxHash, err := chainhash.NewHash(resp.CloseTxid)
	if err != nil {
		t.Fatalf("invalid close transaction hash: %v", err)
	}
	closeTx := assertTxInBlock(t, block, closeTxHash)

	// The account should now be found in a StateClosed state.
	assertTraderAccountState(
		t.t, trader, req.TraderKey, poolrpc.AccountState_CLOSED,
	)
	assertAuctioneerAccountState(
		t, req.TraderKey, auctioneerAccount.StateClosed,
	)

	return closeTx
}

// assertTraderSubscribed makes sure the trader with the given token is
// connected to the auction server and has an active account subscription.
func assertTraderSubscribed(t *harnessTest, token lsat.TokenID,
	acct *poolrpc.Account) {

	// Make sure the trader stream was registered.
	err := wait.NoError(func() error {
		ctx := context.Background()
		client := t.auctioneer.AuctionAdminClient
		resp, err := client.ConnectedTraders(
			ctx, &adminrpc.EmptyRequest{},
		)
		if err != nil {
			return fmt.Errorf("error getting connected traders: %v",
				err)
		}
		traderStreams := resp.Streams
		if len(traderStreams) != 1 {
			return fmt.Errorf("unexpected number of trader "+
				"streams, got %d expected %d",
				len(traderStreams), 1)
		}
		stream, ok := traderStreams[token.String()]
		if !ok {
			return fmt.Errorf("trader stream for token %v not "+
				"found", token)
		}

		// Loop through all subscribed account keys to see if the one we
		// are looking for is included.
		for _, subscribedKey := range stream.RawKeyBytes {
			if bytes.Equal(subscribedKey, acct.TraderKey) {
				return nil
			}
		}

		return fmt.Errorf("account %x not subscribed", acct.TraderKey)
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf("trader stream was not registered before timeout: %v",
			err)
	}
}

// assertOrderEvents makes sure the order with the given nonce has the correct
// approximate creation timestamp and number of other events given.
func assertOrderEvents(t *harnessTest, trader *traderHarness, nonce []byte,
	creationTs time.Time, numUpdated, numMatched int,
	rejectReasons ...poolrpc.MatchRejectReason) {

	t.t.Helper()

	ctx := context.Background()
	orderList, err := trader.ListOrders(ctx, &poolrpc.ListOrdersRequest{
		Verbose: true,
	})
	require.NoError(t.t, err)

	var rpcOrder *poolrpc.Order
	for _, ask := range orderList.Asks {
		if bytes.Equal(ask.Details.OrderNonce, nonce) {
			rpcOrder = ask.Details
			break
		}
	}
	for _, bid := range orderList.Bids {
		if bytes.Equal(bid.Details.OrderNonce, nonce) {
			rpcOrder = bid.Details
			break
		}
	}
	require.NotNil(t.t, rpcOrder)

	// Make sure the creation timestamp is within 100 milliseconds of the
	// time that was passed in.
	expectedTs := uint64(creationTs.UnixNano())
	maxDelta := float64(100 * time.Millisecond)
	require.InDelta(t.t, expectedTs, rpcOrder.CreationTimestampNs, maxDelta)

	// Assert we have the right number of events in verbose mode.
	actualCreated, actualUpdated, actualMatched := 0, 0, 0
	for _, rpcEvent := range rpcOrder.Events {
		switch {
		case rpcEvent.EventStr == "OrderCreated":
			actualCreated++

		case rpcEvent.GetStateChange() != nil:
			actualUpdated++

		case rpcEvent.GetMatched() != nil:
			actualMatched++

			m := rpcEvent.GetMatched()
			if len(rejectReasons) > 0 &&
				m.RejectReason != poolrpc.MatchRejectReason_NONE {

				require.Contains(
					t.t, rejectReasons, m.RejectReason,
				)
			}
		}
	}
	require.Equal(t.t, 1, actualCreated)
	require.Equal(t.t, numUpdated, actualUpdated)
	require.Equal(t.t, numMatched, actualMatched)
}

// traderOutputScript creates a P2WPKH output script that pays to the trader's
// lnd wallet.
func traderOutputScript(t *harnessTest, traderNode *lntest.HarnessNode) []byte {
	ctx := context.Background()
	resp, err := traderNode.NewAddress(ctx, &lnrpc.NewAddressRequest{
		Type: lnrpc.AddressType_WITNESS_PUBKEY_HASH,
	})
	if err != nil {
		t.Fatalf("could not create new address: %v", err)
	}
	addr, err := btcutil.DecodeAddress(
		resp.Address, &chaincfg.RegressionNetParams,
	)
	if err != nil {
		t.Fatalf("could not decode address: %v", err)
	}
	addrScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatalf("could not create pay to address script: %v", err)
	}
	return addrScript
}

func assertPendingChannel(t *harnessTest, node *lntest.HarnessNode,
	chanAmt btcutil.Amount, initiator bool, chanPeer [33]byte) {

	req := &lnrpc.PendingChannelsRequest{}
	err := wait.NoError(func() error {
		resp, err := node.PendingChannels(context.Background(), req)
		if err != nil {
			return err
		}

		if len(resp.PendingOpenChannels) == 0 {
			return fmt.Errorf("no pending channels")
		}

		var pendingChan *lnrpc.PendingChannelsResponse_PendingOpenChannel
		for _, c := range resp.PendingOpenChannels {
			if c.Channel.Capacity != int64(chanAmt) {
				continue
			}

			chanPeerStr := hex.EncodeToString(chanPeer[:])
			if c.Channel.RemoteNodePub != chanPeerStr {
				continue
			}

			pendingChan = c
			break
		}

		if pendingChan == nil {
			return fmt.Errorf("channel with capacity %v and peer "+
				"%x not found in pending channels", chanAmt,
				chanPeer)
		}

		channel := pendingChan.Channel
		switch {
		case channel.Initiator == lnrpc.Initiator_INITIATOR_LOCAL &&
			!initiator:

			return fmt.Errorf("intiator mismatch: expected %v, "+
				"got %v", initiator, channel.Initiator)

		case channel.Initiator == lnrpc.Initiator_INITIATOR_REMOTE &&
			initiator:

			return fmt.Errorf("intiator mismatch: expected %v, "+
				"got %v", initiator, channel.Initiator)
		}

		return nil
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf("pending channel assertion failed: %v", err)
	}
}

func assertNumPendingChannels(t *harnessTest, node *lntest.HarnessNode,
	numChans int) {

	req := &lnrpc.PendingChannelsRequest{}
	err := wait.NoError(func() error {
		resp, err := node.PendingChannels(context.Background(), req)
		require.NoError(t.t, err)

		if len(resp.PendingOpenChannels) != numChans {
			return fmt.Errorf("num channel mismatch: expected %v, "+
				"got %v", numChans,
				len(resp.PendingOpenChannels))
		}

		return nil
	}, defaultWaitTimeout)
	require.NoError(t.t, err)
}

// completePaymentRequests sends payments from a lightning node to complete all
// payment requests. If the awaitResponse parameter is true, this function
// does not return until all payments successfully complete without errors.
func completePaymentRequests(ctx context.Context, client lnrpc.LightningClient,
	paymentRequests []string, awaitResponse bool) error {

	// We start by getting the current state of the client's channels. This
	// is needed to ensure the payments actually have been committed before
	// we return.
	ctxt, cancel := context.WithTimeout(ctx, defaultWaitTimeout)
	defer cancel()
	req := &lnrpc.ListChannelsRequest{}
	listResp, err := client.ListChannels(ctxt, req)
	if err != nil {
		return err
	}

	ctxc, cancel := context.WithCancel(ctx)
	defer cancel()

	payStream, err := client.SendPayment(ctxc)
	if err != nil {
		return err
	}

	for _, payReq := range paymentRequests {
		sendReq := &lnrpc.SendRequest{
			PaymentRequest: payReq,
		}
		err := payStream.Send(sendReq)
		if err != nil {
			return err
		}
	}

	if awaitResponse {
		for range paymentRequests {
			resp, err := payStream.Recv()
			if err != nil {
				return err
			}
			if resp.PaymentError != "" {
				return fmt.Errorf("received payment error: %v",
					resp.PaymentError)
			}
		}

		return nil
	}

	// We are not waiting for feedback in the form of a response, but we
	// should still wait long enough for the server to receive and handle
	// the send before cancelling the request. We wait for the number of
	// updates to one of our channels has increased before we return.
	err = wait.Predicate(func() bool {
		ctxt, cancel = context.WithTimeout(ctx, defaultWaitTimeout)
		defer cancel()

		newListResp, err := client.ListChannels(ctxt, req)
		if err != nil {
			return false
		}

		for _, c1 := range listResp.Channels {
			for _, c2 := range newListResp.Channels {
				if c1.ChannelPoint != c2.ChannelPoint {
					continue
				}

				// If this channel has an increased numbr of
				// updates, we assume the payments are
				// committed, and we can return.
				if c2.NumUpdates > c1.NumUpdates {
					return true
				}
			}
		}

		return false
	}, time.Second*15)
	if err != nil {
		return err
	}

	return nil
}

func assertActiveChannel(t *harnessTest, node *lntest.HarnessNode,
	chanAmt int64, fundingTXID chainhash.Hash, chanPeer [33]byte,
	chanDuration uint32) *lnrpc.ChannelPoint { // nolint:unparam

	var chanPointStr string
	req := &lnrpc.ListChannelsRequest{}
	err := wait.NoError(func() error {
		resp, err := node.ListChannels(context.Background(), req)
		if err != nil {
			return err
		}

		if len(resp.Channels) == 0 {
			return fmt.Errorf("no pending channels")
		}

		var pendingChan *lnrpc.Channel
		for _, c := range resp.Channels {
			if c.Capacity != chanAmt {
				continue
			}

			chanPeerStr := hex.EncodeToString(chanPeer[:])
			if c.RemotePubkey != chanPeerStr {
				continue
			}

			pendingChan = c
			break
		}

		if pendingChan == nil {
			return fmt.Errorf("channel with capacity %v and peer "+
				"%x not found in pending channels", chanAmt,
				chanPeer)
		}

		if !pendingChan.Active {
			return fmt.Errorf("channel not active")
		}

		if !strings.Contains(pendingChan.ChannelPoint, fundingTXID.String()) {
			return fmt.Errorf("wrong output: %v, should have "+
				"hash %v", pendingChan.ChannelPoint, fundingTXID.String())
		}

		if pendingChan.ThawHeight != chanDuration {
			return fmt.Errorf("wrong thaw height: expected %v, "+
				"got %v", chanDuration, pendingChan.ThawHeight)
		}

		chanPointStr = pendingChan.ChannelPoint
		return nil
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf("active channel assertion failed: %v", err)
	}

	chanPointParts := strings.Split(chanPointStr, ":")
	txid, err := chainhash.NewHashFromStr(chanPointParts[0])
	if err != nil {
		t.Fatalf("unable txid to convert to hash: %v", err)
	}
	index, err := strconv.Atoi(chanPointParts[1])
	if err != nil {
		t.Fatalf("unable to convert string to int: %v", err)
	}
	return &lnrpc.ChannelPoint{
		FundingTxid: &lnrpc.ChannelPoint_FundingTxidBytes{
			FundingTxidBytes: txid[:],
		},
		OutputIndex: uint32(index),
	}
}

type bidModifier func(bid *poolrpc.SubmitOrderRequest_Bid)

func submitBidOrder(trader *traderHarness, subKey []byte,
	rate uint32, amt btcutil.Amount,
	modifiers ...bidModifier) (orderT.Nonce, error) {

	rpcBid := &poolrpc.SubmitOrderRequest_Bid{
		Bid: &poolrpc.Bid{
			Details: &poolrpc.Order{
				TraderKey:               subKey,
				RateFixed:               rate,
				Amt:                     uint64(amt),
				MinUnitsMatch:           1,
				MaxBatchFeeRateSatPerKw: uint64(12500),
			},
			LeaseDurationBlocks: defaultOrderDuration,
			Version: uint32(
				orderT.VersionLeaseDurationBuckets,
			),
			MinNodeTier: auctioneerrpc.NodeTier_TIER_0,
		},
	}
	for _, modifier := range modifiers {
		modifier(rpcBid)
	}

	var nonce orderT.Nonce

	ctx := context.Background()
	resp, err := trader.SubmitOrder(ctx, &poolrpc.SubmitOrderRequest{
		Details: rpcBid,
	})
	if err != nil {
		return nonce, err
	}

	if resp.GetInvalidOrder() != nil {
		return nonce, fmt.Errorf("invalid order: %v",
			resp.GetInvalidOrder().FailString)
	}

	copy(nonce[:], resp.GetAcceptedOrderNonce())

	return nonce, nil
}

type askModifier func(ask *poolrpc.SubmitOrderRequest_Ask)

func submitAskOrder(trader *traderHarness, subKey []byte,
	rate uint32, amt btcutil.Amount,
	modifiers ...askModifier) (orderT.Nonce, error) {

	rpcAsk := &poolrpc.SubmitOrderRequest_Ask{
		Ask: &poolrpc.Ask{
			Details: &poolrpc.Order{
				TraderKey:               subKey,
				RateFixed:               rate,
				Amt:                     uint64(amt),
				MinUnitsMatch:           1,
				MaxBatchFeeRateSatPerKw: uint64(12500),
			},
			LeaseDurationBlocks: defaultOrderDuration,
			Version: uint32(
				orderT.VersionLeaseDurationBuckets,
			),
		},
	}
	for _, modifier := range modifiers {
		modifier(rpcAsk)
	}

	var nonce orderT.Nonce

	ctx := context.Background()
	resp, err := trader.SubmitOrder(ctx, &poolrpc.SubmitOrderRequest{
		Details: rpcAsk,
	})
	if err != nil {
		return nonce, err
	}

	if resp.GetInvalidOrder() != nil {
		return nonce, fmt.Errorf("invalid order: %v",
			resp.GetInvalidOrder().FailString)
	}

	copy(nonce[:], resp.GetAcceptedOrderNonce())

	return nonce, nil
}

func assertNoOrders(t *harnessTest, trader *traderHarness) {
	err := wait.NoError(func() error {
		req := &poolrpc.ListOrdersRequest{}
		resp, err := trader.ListOrders(context.Background(), req)
		if err != nil {
			return err
		}

		for _, ask := range resp.Asks {
			if ask.Details.State != auctioneerrpc.OrderState_ORDER_EXECUTED {

				return fmt.Errorf("order in state: %v",
					ask.Details.State)
			}
		}

		for _, bid := range resp.Bids {
			if bid.Details.State != auctioneerrpc.OrderState_ORDER_EXECUTED {

				return fmt.Errorf("order in state: %v",
					bid.Details.State)
			}
		}

		return nil
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf("no order assertion failed: %v", err)
	}
}

func assertAskOrderState(t *harnessTest, trader *traderHarness,
	unfilledUnits uint32, orderNonce orderT.Nonce) {

	// TODO(roasbeef): add LookupORder method for client RPC
	err := wait.NoError(func() error {
		req := &poolrpc.ListOrdersRequest{}
		resp, err := trader.ListOrders(context.Background(), req)
		if err != nil {
			return err
		}

		var orderFound bool
		for _, order := range resp.Asks {
			if !bytes.Equal(order.Details.OrderNonce,
				orderNonce[:]) {
				continue
			}

			if order.Details.UnitsUnfulfilled != unfilledUnits {
				return fmt.Errorf("order has %v units "+
					"unfilled, expected %v",
					order.Details.UnitsUnfulfilled,
					unfilledUnits)
			}

			orderFound = true
		}

		if !orderFound {
			return fmt.Errorf("order not found")
		}

		return nil
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf("order state doesn't match: %v", err)
	}
}

// assertChannelClosed asserts that the channel is properly cleaned up after
// initiating a cooperative or local close.
func assertChannelClosed(ctx context.Context, t *harnessTest,
	net *lntest.NetworkHarness, node *lntest.HarnessNode,
	fundingChanPoint *lnrpc.ChannelPoint,
	closeUpdates lnrpc.Lightning_CloseChannelClient,
	force bool) *chainhash.Hash {

	txid, err := lnd.GetChanPointFundingTxid(fundingChanPoint)
	if err != nil {
		t.Fatalf("unable to get txid: %v", err)
	}
	chanPointStr := fmt.Sprintf("%v:%v", txid, fundingChanPoint.OutputIndex)

	// At this point, the channel should now be marked as being in the
	// state of "waiting close".
	pendingChansRequest := &lnrpc.PendingChannelsRequest{}
	err = wait.NoError(func() error {
		pendingChanResp, err := node.PendingChannels(ctx, pendingChansRequest)
		if err != nil {
			return fmt.Errorf("unable to query for pending channels: %v", err)
		}
		var found bool
		for _, pendingClose := range pendingChanResp.WaitingCloseChannels {
			if pendingClose.Channel.ChannelPoint == chanPointStr {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no chan found")
		}

		return nil
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf("channel not marked as waiting close: %v", err)
	}

	// We'll now, generate a single block, wait for the final close status
	// update, then ensure that the closing transaction was included in the
	// block.
	block := mineBlocks(t, net, 1, 1)[0]

	closingTxid, err := net.WaitForChannelClose(ctx, closeUpdates)
	if err != nil {
		t.Fatalf("error while waiting for channel close: %v", err)
	}

	assertTxInBlock(t, block, closingTxid)

	// Finally, the transaction should no longer be in the waiting close
	// state as we've just mined a block that should include the closing
	// transaction.
	var csvDelay uint32
	err = wait.Predicate(func() bool {
		pendingChansRequest := &lnrpc.PendingChannelsRequest{}
		pendingChanResp, err := node.PendingChannels(
			ctx, pendingChansRequest,
		)
		if err != nil {
			return false
		}

		if force {
			// If the channel was force closed, we'll need to mine
			// some additional blocks to trigger the delayed
			// commitment sweep.
			for _, pendingClose := range pendingChanResp.PendingForceClosingChannels {
				if pendingClose.Channel.ChannelPoint == chanPointStr {
					csvDelay = uint32(pendingClose.BlocksTilMaturity)
					break
				}
			}

			// Wait for the proper CSV delay to be reported.
			if csvDelay == 0 {
				return false
			}
		} else {
			// Otherwise, the channel was closed cooperatively, so
			// we'll wait until the backing lnd node picks up its
			// confirmation.
			for _, pendingClose := range pendingChanResp.WaitingCloseChannels {
				if pendingClose.Channel.ChannelPoint == chanPointStr {
					return false
				}
			}
		}

		return true
	}, time.Second*15)
	if err != nil {
		t.Fatalf("closing transaction not marked as fully closed")
	}

	if !force {
		return closingTxid
	}

	// If the channel was force closed, we'll need to mine some additional
	// blocks to trigger the delayed commitment sweep.
	_ = mineBlocks(t, net, csvDelay, 0)
	_ = mineBlocks(t, net, 1, 1)

	return closingTxid
}

func executeBatch(t *harnessTest, expectedMempoolTxns int) ([]*wire.MsgTx,
	[]*chainhash.Hash) {

	ctx := context.Background()

	// Let's kick the auctioneer now to try and create a batch.
	_, err := t.auctioneer.AuctionAdminClient.BatchTick(
		ctx, &adminrpc.EmptyRequest{},
	)
	require.NoError(t.t, err)

	// Before we check anything else, let's first wait for the auctioneer
	// to do its job and then return back to its "waiting" state where new
	// orders are accepted.
	assertAuctionState(t, subasta.OrderSubmitState{})

	// At this point, the batch should now attempt to be cleared, and find
	// that we're able to make a market. Eventually the batch execution
	// transaction should be broadcast to the mempool.
	txids, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Node, expectedMempoolTxns,
		minerMempoolTimeout,
	)
	require.NoError(t.t, err)

	if len(txids) != expectedMempoolTxns {
		t.Fatalf("expected %d transaction(s), instead have: %v",
			expectedMempoolTxns, spew.Sdump(txids))
	}

	msgTxs := make([]*wire.MsgTx, len(txids))
	for idx, txid := range txids {
		tx, err := t.lndHarness.Miner.Node.GetRawTransaction(txid)
		require.NoError(t.t, err)
		msgTxs[idx] = tx.MsgTx()

	}
	return msgTxs, txids
}

func expectNoPossibleMarket(t *harnessTest) {
	ctx := context.Background()

	// First, grab the current master account to obtain a snapshot of the
	// current state.
	masterAcct, err := t.auctioneer.MasterAccount(
		ctx, &adminrpc.EmptyRequest{},
	)
	if err != nil {
		t.Fatalf("unable to fetch master acct")
	}

	// Let's kick the auctioneer now to try and create a batch.
	_, err = t.auctioneer.AuctionAdminClient.BatchTick(
		ctx, &adminrpc.EmptyRequest{},
	)
	require.NoError(t.t, err)

	// Before we check anything else, let's first wait for the auctioneer
	// to do its job and then return back to its "waiting" state where new
	// orders are accepted.
	assertAuctionState(t, subasta.OrderSubmitState{})

	// At this point, there should be no new batch, we'll assert this by
	// ensuring the master account still has the same batch key.
	masterAcct2, err := t.auctioneer.MasterAccount(
		ctx, &adminrpc.EmptyRequest{},
	)
	if err != nil {
		t.Fatalf("unable to fetch master acct")
	}

	if !bytes.Equal(masterAcct.BatchKey, masterAcct2.BatchKey) {
		t.Fatalf("a batch occurred but shouldn't have")
	}
}

func withdrawAccountAndAssertMempool(t *harnessTest, trader *traderHarness,
	accountKey []byte, startValue int64, withdrawValue uint64,
	address string) (*chainhash.Hash, btcutil.Amount) {

	t.t.Helper()

	withdrawReq := &poolrpc.WithdrawAccountRequest{
		TraderKey: accountKey,
		Outputs: []*poolrpc.Output{{
			ValueSat: withdrawValue,
			Address:  address,
		}},
		FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
	}
	withdrawResp, err := trader.WithdrawAccount(
		context.Background(), withdrawReq,
	)
	require.NoError(t.t, err)

	// We should expect to see the transaction causing the withdrawal.
	withdrawTxid, _ := chainhash.NewHash(withdrawResp.Account.Outpoint.Txid)
	txids, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, minerMempoolTimeout,
	)
	require.NoError(t.t, err)
	require.Equal(t.t, withdrawTxid, txids[0])

	// Assert that the account state is reflected correctly for both the
	// trader and auctioneer while the withdrawal hasn't confirmed.
	// If the caller doesn't care about the value, we only assert the state.
	if startValue == -1 {
		assertTraderAccountState(
			t.t, trader, withdrawResp.Account.TraderKey,
			poolrpc.AccountState_PENDING_UPDATE,
		)
		assertAuctioneerAccountState(
			t, withdrawResp.Account.TraderKey,
			auctioneerAccount.StatePendingUpdate,
		)

		return withdrawTxid, -1
	}

	// The caller cares about the account value.
	const withdrawalFee = 184
	valueAfterWithdrawal := btcutil.Amount(startValue) -
		btcutil.Amount(withdrawValue) - withdrawalFee
	assertTraderAccount(
		t, trader, withdrawResp.Account.TraderKey, valueAfterWithdrawal,
		withdrawResp.Account.ExpirationHeight,
		poolrpc.AccountState_PENDING_UPDATE,
	)
	assertAuctioneerAccount(
		t, withdrawResp.Account.TraderKey, valueAfterWithdrawal,
		auctioneerAccount.StatePendingUpdate,
	)

	return withdrawTxid, valueAfterWithdrawal
}

// shutdownAndAssert shuts down the given node and asserts that no errors
// occur.
func shutdownAndAssert(t *harnessTest, node *lntest.HarnessNode,
	trader *traderHarness) {

	if trader != nil {
		if err := trader.stop(true); err != nil {
			t.Fatalf("unable to shutdown trader: %v", err)
		}
	}

	if err := t.lndHarness.ShutdownNode(node); err != nil {
		t.Fatalf("unable to shutdown %v: %v", node.Name(), err)
	}
}

// assertServerLogContains makes sure the current auction server log file
// contains the given string.
func assertServerLogContains(t *harnessTest, logStatement string,
	args ...interface{}) {

	content, err := t.auctioneer.getLogFileContent()
	require.NoError(t.t, err)
	require.Contains(t.t, content, fmt.Sprintf(logStatement, args...))
}
