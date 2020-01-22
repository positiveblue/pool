package itest

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/wire"
	"github.com/go-errors/errors"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lntest/wait"
)

var (
	harnessNetParams = &chaincfg.RegressionNetParams
)

const (
	minerMempoolTimeout = lntest.MinerMempoolTimeout
	defaultWaitTimeout  = 15 * time.Second
)

// testCase is a struct that holds a single test case.
type testCase struct {
	name string
	test func(t *harnessTest)
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
	txid *chainhash.Hash) {
	for _, tx := range block.Transactions {
		sha := tx.TxHash()
		if bytes.Equal(txid[:], sha[:]) {
			return
		}
	}

	t.Fatalf("tx was not included in block")
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

// openAccountAndAssert creates a new trader account, mines its funding TX and
// waits for it to be confirmed.
func openAccountAndAssert(ctx context.Context, t *harnessTest,
	net *lntest.NetworkHarness, trader *traderHarness,
	req *clmrpc.InitAccountRequest) *clmrpc.Account {

	acct, err := trader.InitAccount(ctx, req)
	if err != nil {
		t.Fatalf("could not create account: %v", err)
	}

	// At this point the account should be funded but the status should be
	// pending until the TX is confirmed.
	if acct.State != clmrpc.AccountState_PENDING_OPEN {
		t.Fatalf("unexpected account state. got %d, expected %d",
			acct.State, clmrpc.AccountState_PENDING_OPEN)
	}

	// Mine the account funding TX and make sure the account outpoint was
	// actually included in the block.
	block := mineBlocks(t, net, 6, 1)[0]
	txHash, err := chainhash.NewHash(acct.Outpoint.Txid)
	if err != nil {
		t.Fatalf("could not create chain hash from outpoint: %v", err)
	}
	assertTxInBlock(t, block, txHash)

	var account *clmrpc.Account
	err = wait.NoError(func() error {
		list, err := trader.ListAccounts(
			ctx, &clmrpc.ListAccountsRequest{},
		)
		if err != nil {
			return fmt.Errorf("could not list accounts: %v", err)
		}
		for _, a := range list.Accounts {
			if bytes.Equal(a.TraderKey, acct.TraderKey) {
				if a.State != clmrpc.AccountState_OPEN {
					return fmt.Errorf("unexpected account "+
						"state. got %d, expected %d",
						a.State,
						clmrpc.AccountState_OPEN)
				}
				account = a
				return nil
			}
		}
		return fmt.Errorf("account not found in list after account " +
			"creation")
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf("error waiting for account to be confirmed: %v", err)
	}
	return account
}
