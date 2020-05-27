// +build itest
// remove this build tag as soon as lnd#3382 is merged

package itest

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/integration/rpctest"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcutil"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lntest/wait"
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

	// Declare the network harness here to gain access to its
	// 'OnTxAccepted' call back.
	var lndHarness *lntest.NetworkHarness

	// Create an instance of the btcd's rpctest.Harness that will act as
	// the miner for all tests. This will be used to fund the wallets of
	// the nodes within the test network and to drive blockchain related
	// events within the network. Revert the default setting of accepting
	// non-standard transactions on simnet to reject them. Transactions on
	// the lightning network should always be standard to get better
	// guarantees of getting included in to blocks.
	//
	// We will also connect it to our chain backend.
	minerLogDir := "./.minerlogs"
	args := []string{
		"--rejectnonstd",
		"--txindex",
		"--debuglevel=debug",
		"--logdir=" + minerLogDir,
		"--trickleinterval=100ms",
	}
	handlers := &rpcclient.NotificationHandlers{
		OnTxAccepted: func(hash *chainhash.Hash, amt btcutil.Amount) {
			lndHarness.OnTxAccepted(hash)
		},
	}

	miner, err := rpctest.New(harnessNetParams, handlers, args)
	if err != nil {
		ht.Fatalf("unable to create mining node: %v", err)
	}
	defer func() {
		err := miner.TearDown()
		if err != nil {
			fmt.Printf("error tearing down miner: %v\n", err)
		}

		// After shutting down the miner, we'll make a copy of the log
		// file before deleting the temporary log dir.
		logFile := fmt.Sprintf(
			"%s/%s/btcd.log", minerLogDir, harnessNetParams.Name,
		)
		err = lntest.CopyFile("./output_btcd_miner.log", logFile)
		if err != nil {
			fmt.Printf("unable to copy file: %v\n", err)
		}
		if err = os.RemoveAll(minerLogDir); err != nil {
			fmt.Printf("Cannot remove dir %s: %v\n",
				minerLogDir, err)
		}
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
	if err := miner.Node.NotifyNewTransactions(false); err != nil {
		ht.Fatalf("unable to request transaction notifications: %v", err)
	}

	// Now we can set up our test harness (LND instance), with the chain
	// backend we just created.
	lndHarness, err = lntest.NewNetworkHarness(
		miner, chainBackend, "./lnd-itest",
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
		_ = lndHarness.TearDownAll()
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
	if _, err := miner.Node.Generate(numBlocks); err != nil {
		ht.Fatalf("unable to generate blocks: %v", err)
	}

	// With the btcd harness created, we can now complete the
	// initialization of the network. args - list of lnd arguments,
	// example: "--debuglevel=debug"
	if err = lndHarness.SetUp(nil); err != nil {
		ht.Fatalf("unable to set up test lightning network: %v", err)
	}

	// Before we continue on below, we'll wait here until the specified
	// number of blocks has been mined, to ensure we have complete control
	// over the extension of the chain. 10 extra block are mined as the
	// SetUp method above mines 10 blocks to confirm the coins it sends to
	// the first nodes in the harness.
	targetHeight := int32(numBlocks) + 10
	err = wait.NoError(func() error {
		_, blockHeight, err := miner.Node.GetBestBlock()
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
	masterAccountConfirmed := false
	for _, testCase := range testCases {
		logLine := fmt.Sprintf("STARTING ============ %v ============\n",
			testCase.name)

		success := t.Run(testCase.name, func(t1 *testing.T) {
			// The auction server and client are both freshly
			// created and later discarded for each test run to
			// assure no state is taken over between runs.
			traderHarness, auctioneerHarness := setupHarnesses(
				t, lndHarness,
			)
			err = lndHarness.EnsureConnected(
				context.Background(), lndHarness.Alice,
				lndHarness.Bob,
			)
			if err != nil {
				t.Fatalf("unable to connect alice to bob: %v",
					err)
			}

			if err := lndHarness.Alice.AddToLog(logLine); err != nil {
				t.Fatalf("unable to add to log: %v", err)
			}
			if err := lndHarness.Bob.AddToLog(logLine); err != nil {
				t.Fatalf("unable to add to log: %v", err)
			}

			ht := newHarnessTest(
				t1, lndHarness, auctioneerHarness,
				traderHarness,
			)

			// The auctioneer will create its master account on
			// startup. For most tests, if run on their own, we want
			// the auctioneer to be ready before starting the test.
			// Tests that want to explicitly test the master account
			// creation can set this flag to true.
			if !testCase.skipMasterAcctInit && !masterAccountConfirmed {
				_ = mineBlocks(ht, lndHarness, 1, 1)
				masterAccountConfirmed = true
			}
			if testCase.skipMasterAcctInit {
				masterAccountConfirmed = true
			}

			// Now we have everything to run the test case.
			ht.RunTestCase(testCase)

			// Shut down both client and server to remove all state.
			err := ht.shutdown()
			if err != nil {
				t.Fatalf("error shutting down harness: %v", err)
			}
		})

		// Stop at the first failure. Mimic behavior of original test
		// framework.
		if !success {
			break
		}
	}
}
