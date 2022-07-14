//go:build itest
// +build itest

// remove this build tag as soon as lnd#3382 is merged

package itest

import (
	"fmt"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/lightningnetwork/lnd/signal"
	"github.com/stretchr/testify/require"
)

// TestAuctioneerServer performs a series of integration tests amongst a
// programmatically driven set of participants, namely an auction server and an
// auction client, both connected to lnd nodes.
func TestAuctioneerServer(t *testing.T) {
	// If no tests are registered, then we can exit early.
	if len(testCases) == 0 {
		t.Skip("integration tests not selected with flag 'itest'")
	}

	ht := newHarnessTest(t, nil, nil, nil)
	ht.setupLogging()

	// Create an instance of the btcd's rpctest.Harness that will act as
	// the miner for all tests. This will be used to fund the wallets of
	// the nodes within the test network and to drive blockchain related
	// events within the network. Revert the default setting of accepting
	// non-standard transactions on simnet to reject them. Transactions on
	// the lightning network should always be standard to get better
	// guarantees of getting included in to blocks.
	//
	// We will also connect it to our chain backend.
	miner, err := lntest.NewMiner()
	if err != nil {
		ht.Fatalf("unable to create mining node: %v", err)
	}
	defer func() {
		require.NoError(t, miner.Stop())
	}()

	// Start a chain backend.
	chainBackend, cleanUp, err := lntest.NewBackend(
		miner.P2PAddress(), harnessNetParams,
	)
	if err != nil {
		ht.Fatalf("unable to start backend: %v", err)
	}
	defer cleanUp()

	// As we mine blocks below to trigger segwit and CSV activation, we
	// don't need to mine a test chain here.
	if err := miner.SetUp(false, 0); err != nil {
		ht.Fatalf("unable to set up mining node: %v", err)
	}
	if err := miner.Client.NotifyNewTransactions(false); err != nil {
		ht.Fatalf("unable to request transaction notifications: %v", err)
	}
	if err := chainBackend.ConnectMiner(); err != nil {
		ht.Fatalf("unable to connect backend to miner: %v", err)
	}

	// Now we can set up our test harness (LND instance), with the chain
	// backend we just created.
	lndHarness, err := lntest.NewNetworkHarness(
		miner, chainBackend, "./lnd-itest", lntest.BackendBbolt,
	)
	if err != nil {
		ht.Fatalf("unable to create lightning network harness: %v", err)
	}
	defer func() {
		// There is a timing issue in here somewhere. If we shut down
		// lnd immediately after stopping the trader server, sometimes
		// we get a race in the TX notifier chan closes. The wait seems
		// to fix it for now...
		time.Sleep(100 * time.Millisecond)
		_ = lndHarness.TearDown()
		lndHarness.Stop()
	}()

	// Spawn a new goroutine to watch for any fatal errors that any of the
	// running lnd processes encounter. If an error occurs, then the test
	// case should naturally as a result and we log the server error here to
	// help debug.
	go func() {
		for {
			select {
			case err, more := <-lndHarness.ProcessErrors():
				if !more {
					return
				}
				ht.Logf("lnd finished with error (stderr):\n%v",
					err)
			}
		}
	}()

	// Next mine enough blocks in order for segwit and the CSV package
	// soft-fork to activate on SimNet.
	numBlocks := harnessNetParams.MinerConfirmationWindow * 4
	if _, err := miner.Client.Generate(numBlocks); err != nil {
		ht.Fatalf("unable to generate blocks: %v", err)
	}

	// With the btcd harness created, we can now complete the
	// initialization of the network.
	err = lndHarness.SetUp(ht.t, "subasta-itest", lndDefaultArgs)
	if err != nil {
		ht.Fatalf("unable to set up test lightning network: %v", err)
	}

	// Before we continue on below, we'll wait here until the specified
	// number of blocks has been mined, to ensure we have complete control
	// over the extension of the chain. 10 extra block are mined as the
	// SetUp method above mines 10 blocks to confirm the coins it sends to
	// the first nodes in the harness.
	targetHeight := int32(numBlocks) + 10
	err = wait.NoError(func() error {
		_, blockHeight, err := miner.Client.GetBestBlock()
		if err != nil {
			return fmt.Errorf("unable to get best block: %v", err)
		}

		if blockHeight < targetHeight {
			return fmt.Errorf("want height %v, got %v",
				blockHeight, targetHeight)
		}

		return nil
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf("test chian never created: %v", err)
	}

	t.Logf("Running %v integration tests", len(testCases))
	for _, testCase := range testCases {
		logLine := fmt.Sprintf("STARTING ============ %v ============\n",
			testCase.name)

		success := t.Run(testCase.name, func(t1 *testing.T) {
			// The auction server and client are both freshly
			// created and later discarded for each test run to
			// assure no state is taken over between runs.
			traderHarness, auctioneerHarness := setupHarnesses(
				t1, lndHarness, signal.Interceptor{},
			)
			lndHarness.EnsureConnected(
				t1, lndHarness.Alice, lndHarness.Bob,
			)

			lndHarness.Alice.AddToLogf(logLine)
			lndHarness.Bob.AddToLogf(logLine)

			ht := newHarnessTest(
				t1, lndHarness, auctioneerHarness,
				traderHarness,
			)

			// The auctioneer will create its master account on
			// startup. For most tests, if run on their own, we want
			// the auctioneer to be ready before starting the test.
			// Tests that want to explicitly test the master account
			// creation can set this flag to true.
			if !testCase.skipMasterAcctInit {
				_ = mineBlocks(ht, lndHarness, 1, 1)
			}

			// Now we have everything to run the test case.
			ht.RunTestCase(testCase)

			// Shut down both client and server to remove all state.
			err := ht.shutdown(t)
			if err != nil {
				t1.Fatalf("error shutting down harness: %v", err)
			}
		})

		// Stop at the first failure. Mimic behavior of original test
		// framework.
		if !success {
			return
		}
	}

	// TODO(positiveblue): leader election needs multiple harnesses running
	// at once so it does not fit the current TestAuctioneerServer to be
	// added as a TestCase. Rethink tests to add this one as a testCase?
	t.Run("leader election", func(t1 *testing.T) {
		if err := leaderElectionTestCase(t1, lndHarness); err != nil {
			ht.Fatalf("%v", err)
		}
	})

}

// leaderElectionTestCase checks the leader election logic by starting
// two subasta instances. It checks that the frist one is up and running
// and the second one is waiting to be selected before starting the server.
func leaderElectionTestCase(t *testing.T,
	lndHarness *lntest.NetworkHarness) error {

	t.Log("Running leader election integration tests")

	primaryAddr, primaryHarness, err := newAuctioneerHarnessWithReporter(
		"primary", auctioneerConfig{
			BackendCfg:  lndHarness.BackendCfg,
			NetParams:   harnessNetParams,
			LndNode:     lndHarness.Alice,
			Interceptor: signal.Interceptor{},
		},
	)
	require.NoError(t, err)

	if err := primaryHarness.start(t); err != nil {
		t.Fatalf("unable to start primary harness: %v", err)
	}

	err = wait.NoError(func() error {
		return checkReady(primaryAddr, true)
	}, defaultWaitTimeout)
	require.NoError(t, err)

	secondaryAddr, secondaryHarness, err := newAuctioneerHarnessWithReporter(
		"secondary", auctioneerConfig{
			BackendCfg:  lndHarness.BackendCfg,
			NetParams:   harnessNetParams,
			LndNode:     lndHarness.Alice,
			Interceptor: signal.Interceptor{},
		},
	)
	require.NoError(t, err)

	secondaryHarness.initSQLDatabaseServer(t)

	// secondaryHarness.start() tries to recreate the etcd database and it
	// fails so we need to set the same database as the primary harness
	// here.
	secondaryHarness.etcd = primaryHarness.etcd

	// This server will hang, so we run it in a goroutine.
	go secondaryHarness.runServer()

	// Give some time to the secondary sever to start.
	err = wait.NoError(func() error {
		return checkReady(secondaryAddr, false)
	}, defaultWaitTimeout)
	require.NoError(t, err)

	// Let's kill the first server and see how the secondaryHarness takes
	// over.
	primaryHarness.halt()
	err = wait.NoError(func() error {
		return checkReady(secondaryAddr, true)
	}, defaultWaitTimeout)
	require.NoError(t, err)

	// TODO (positiveblue): fix primary and secondary harness leaks. It is
	// not a problem because this is the last test, but the harness needs
	// a refactor so we can start and stop multiple instances without any
	// problem.

	return nil
}
