// +build itest
// remove this build tag as soon as lnd#3382 is merged

package itest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/integration/rpctest"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcutil"
	"github.com/lightningnetwork/lnd/lntest"
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

	if err := miner.SetUp(true, 50); err != nil {
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
	defer lndHarness.TearDownAll()

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
	numBlocks := harnessNetParams.MinerConfirmationWindow * 2
	if _, err := miner.Node.Generate(numBlocks); err != nil {
		ht.Fatalf("unable to generate blocks: %v", err)
	}

	// With the btcd harness created, we can now complete the
	// initialization of the network. args - list of lnd arguments,
	// example: "--debuglevel=debug"
	if err = lndHarness.SetUp(nil); err != nil {
		ht.Fatalf("unable to set up test lightning network: %v", err)
	}

	t.Logf("Running %v integration tests", len(testCases))
	for _, testCase := range testCases {
		logLine := fmt.Sprintf("STARTING ============ %v ============\n",
			testCase.name)

		// The auction server and client are both freshly created and
		// later discarded for each test run to assure no state is taken
		// over between runs.
		traderHarness, auctioneerHarness := setupHarnesses(
			t, lndHarness,
		)
		err = lndHarness.EnsureConnected(
			context.Background(), lndHarness.Alice, lndHarness.Bob,
		)
		if err != nil {
			t.Fatalf("unable to connect alice to bob: %v", err)
		}

		if err := lndHarness.Alice.AddToLog(logLine); err != nil {
			t.Fatalf("unable to add to log: %v", err)
		}
		if err := lndHarness.Bob.AddToLog(logLine); err != nil {
			t.Fatalf("unable to add to log: %v", err)
		}

		success := t.Run(testCase.name, func(t1 *testing.T) {
			ht := newHarnessTest(
				t1, lndHarness, auctioneerHarness,
				traderHarness,
			)
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

// setupHarnesses creates new server and client harnesses that are connected
// to each other through an in-memory gRPC connection.
func setupHarnesses(t *testing.T, lndHarness *lntest.NetworkHarness) (
	*traderHarness, *auctioneerHarness) {

	// Create the two harnesses but don't start them yet, they need to be
	// connected through the bufconn first.
	auctioneerHarness, err := newAuctioneerHarness(auctioneerConfig{
		BackendCfg: lndHarness.BackendCfg,
		NetParams:  harnessNetParams,
		LndNode:    lndHarness.Alice,
	})
	if err != nil {
		t.Fatalf("could not create auction server: %v", err)
	}
	traderHarness, err := newTraderHarness(traderConfig{
		BackendCfg: lndHarness.BackendCfg,
		NetParams:  harnessNetParams,
		LndNode:    lndHarness.Bob,
	})
	if err != nil {
		t.Fatalf("could not create trader server: %v", err)
	}

	// Connect them together through the in-memory connection and then start
	// them.
	err = connectServerClient(auctioneerHarness, traderHarness, false)
	if err != nil {
		t.Fatalf("could not connect client to server: %v", err)
	}
	err = traderHarness.start()
	if err != nil {
		t.Fatalf("could not start trader server: %v", err)
	}
	return traderHarness, auctioneerHarness
}
