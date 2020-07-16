package itest

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightningnetwork/lnd/lntest/wait"
)

// testMasterAcctInit tests that after we start up the auctioneer, if an account
// doesn't already exist, then it creates one and waits for it to be mined in a
// block, until it confirms.
func testMasterAcctInit(t *harnessTest) {
	// Right off the bat, a transaction should enter the mempool as we go
	// to init the system.
	txid, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, time.Second*10,
	)
	if err != nil {
		t.Fatalf("transactions not found in the mempool: %v", err)
	}

	// We'll now restart the auctioneer as it should still be able to
	// process the confirmation even after a restart.
	t.restartServer()

	// Next, we mine a block, which should confirm the genesis transaction
	// created above.
	blocks := mineBlocks(t, t.lndHarness, 1, 1)

	// We should find the transaction in the block, and if we do it should
	// be of the correct value.
	var genesisTx *wire.MsgTx
	for _, tx := range blocks[0].Transactions {
		txHash := tx.TxHash()
		if txHash.IsEqual(txid[0]) {
			genesisTx = tx
			break
		}
	}

	if genesisTx == nil {
		t.Fatalf("genesis tx not mined in block")
	}

	// We should now be able to examine the database and find a populated
	// master account which matches the genesis tx found above.
	ctxb := context.Background()
	client := t.auctioneer.AuctionAdminClient
	var masterAcct *adminrpc.MasterAccountResponse
	err = wait.NoError(func() error {
		masterAcct, err = client.MasterAccount(
			ctxb, &adminrpc.EmptyRequest{},
		)
		if err != nil { // nolint:gosimple
			return err
		}

		if masterAcct.Pending {
			return errors.New("master account is still pending")
		}

		return nil
	}, defaultWaitTimeout)
	if err != nil {
		t.Fatalf("unable to fetch master acct: %v", err)
	}

	// The stored outpoint should match the genesis transaction, and the
	// output should match as well.
	acctOutPoint := masterAcct.Outpoint
	genTxHash := genesisTx.TxHash()
	if !bytes.Equal(acctOutPoint.Txid, genTxHash[:]) {
		t.Fatalf("gen txid mismatch: expected %v, got %x",
			&genTxHash, acctOutPoint.Txid)
	}
}
