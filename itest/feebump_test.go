package itest

import (
	"context"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/subasta"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/stretchr/testify/require"
)

// testManualFeeBump ensures we can bump the fee rate of pending batches by
// requesting a higher fee rate for the following batch.
func testManualFeeBump(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader.
	lndArgs := []string{"--maxpendingchannels=2"}
	charlie, err := t.lndHarness.NewNode("charlie", lndArgs)
	if err != nil {
		t.Fatalf("unable to set up charlie: %v", err)
	}
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	if err != nil {
		t.Fatalf("unable to send coins to carol: %v", err)
	}

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders.
	account1 := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	account2 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// Now we check that manual fee bumping works as expected, first by
	// requiring a fee for the next batch that is very low.
	ctxb := context.Background()
	var customFeeRate int64 = 500
	_, err = t.auctioneer.AuctionAdminClient.BumpBatchFeeRate(
		ctxb, &adminrpc.BumpBatchFeeRateRequest{
			FeeRateSatPerKw: uint32(customFeeRate),
		},
	)
	if err != nil {
		t.Fatalf("unable to request fee bump: %v", err)
	}

	// Make the batch tick, but since there are no orders no batch should
	// actually be created.
	_, err = t.auctioneer.AuctionAdminClient.BatchTick(
		ctxb, &adminrpc.EmptyRequest{},
	)
	require.NoError(t.t, err)

	// Ensure no transaction was created.
	empty, err := isMempoolEmpty(
		t.lndHarness.Miner.Node, 1*time.Second,
	)
	require.NoError(t.t, err)
	require.True(t.t, empty, "mempool not empty")

	// We should go back to the order submit state.
	assertAuctionState(t, subasta.OrderSubmitState{})

	// Now that the accounts are confirmed, submit an ask order from our
	// default trader, selling 15 units (1.5M sats) of liquidity.
	const orderFixedRate = 100
	askAmt := btcutil.Amount(1_500_000)
	_, err = submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate, askAmt,
		defaultOrderDuration, uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit ask order: %v", err)
	}

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity. So let's submit an order for that.
	bidAmt := btcutil.Amount(800_000)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate, bidAmt,
		defaultOrderDuration, uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit bid order: %v", err)
	}

	// Let's kick the auctioneer once again to try and create a batch.
	_, batchTXIDs := executeBatch(t, 1)
	batchTXID := batchTXIDs[0]

	// Check that the final fee rate of the transaction is what we
	// requested earlier. Since the worst case tx weight is used when
	// setting the fee, the actual fee rate might be a litle bit higher, so
	// we allow a few sat/kw difference.
	feeRate, err := getTxFeeRate(t, batchTXID)
	require.NoError(t.t, err)

	// We allow the fee rate to be up to 101% of our custom fee rate.
	if feeRate < customFeeRate || feeRate > int64(float64(customFeeRate)*1.01) {
		t.Fatalf("batch had fee rate %v, after bump to %v",
			feeRate, customFeeRate)
	}

	// We execute another fee bump request and batch tick. Since there are
	// no orders left to be matches, this should trigger the creation of an
	// empty batch on the next tick.
	customFeeRate = 20000
	_, err = t.auctioneer.AuctionAdminClient.BumpBatchFeeRate(
		ctxb, &adminrpc.BumpBatchFeeRateRequest{
			FeeRateSatPerKw: uint32(customFeeRate),
		},
	)
	if err != nil {
		t.Fatalf("unable to request fee bump: %v", err)
	}

	// Make the batch tick.
	_, err = t.auctioneer.AuctionAdminClient.BatchTick(
		ctxb, &adminrpc.EmptyRequest{},
	)
	require.NoError(t.t, err)

	// An empty batch should be created, since we want to use it to bump
	// the previous, non-empty batch.
	txids, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 2, minerMempoolTimeout,
	)
	require.NoError(t.t, err)

	// Get the effective fee rate of the tx package.
	feeRate, err = getTxFeeRate(t, txids...)
	require.NoError(t.t, err)

	// We allow the fee rate to be up to 101% of our custom fee rate.
	if feeRate < customFeeRate || feeRate > int64(float64(customFeeRate)*1.01) {
		t.Fatalf("batch had fee rate %v, after bump to %v",
			feeRate, customFeeRate)
	}

	// Bump again, with a lower fee rate, ensuring this don't trigger a new
	// empty batch.
	customFeeRate = 10000
	_, err = t.auctioneer.AuctionAdminClient.BumpBatchFeeRate(
		ctxb, &adminrpc.BumpBatchFeeRateRequest{
			FeeRateSatPerKw: uint32(customFeeRate),
		},
	)
	if err != nil {
		t.Fatalf("unable to request fee bump: %v", err)
	}

	// Make the batch tick.
	_, err = t.auctioneer.AuctionAdminClient.BatchTick(
		ctxb, &adminrpc.EmptyRequest{},
	)
	require.NoError(t.t, err)

	// Still only two transactions should be found in the mempool.
	_, err = waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 2, minerMempoolTimeout,
	)
	require.NoError(t.t, err)

	// As a final check before cleanup, ensure the channel is still
	// pending.
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bidAmt, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt, false, t.trader.cfg.LndNode.PubKey,
	)

	// We'll also make sure that the accounts are in StatePendingBatch.
	assertAuctioneerAccountState(
		t, account1.TraderKey, account.StatePendingBatch,
	)
	assertAuctioneerAccountState(
		t, account2.TraderKey, account.StatePendingBatch,
	)

	// We'll now mine a block to confirme the transactions.
	blocks := mineBlocks(t, t.lndHarness, 1, 2)

	// The block above should contain both batch transactions found in the
	// mempool above.
	for _, batchTXID := range txids {
		assertTxInBlock(t, blocks[0], batchTXID)
	}

	// We'll now mine another 3 blocks to ensure the channel itself is
	// fully confirmed.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// Now that the channels are confirmed, they should both be active, and
	// we should be able to make a payment between this new channel
	// established.
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt), *batchTXID,
		charlie.PubKey, defaultOrderDuration,
	)

	assertActiveChannel(
		t, charlie, int64(bidAmt), *batchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration,
	)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
}

// getTxFeeRate finds the effective fee rate of the package txids.
func getTxFeeRate(t *harnessTest, txids ...*chainhash.Hash) (int64, error) {
	var (
		fee    int64
		weight int64
	)
	for _, txid := range txids {
		tx, err := t.lndHarness.Miner.Node.GetRawTransaction(txid)
		if err != nil {
			return 0, err
		}

		// Get the input value for each input and add it to the total
		// fee.
		for _, in := range tx.MsgTx().TxIn {
			prev := in.PreviousOutPoint

			parent, err := t.lndHarness.Miner.Node.GetRawTransaction(
				&prev.Hash,
			)
			if err != nil {
				return 0, err
			}
			fee += parent.MsgTx().TxOut[prev.Index].Value
		}

		// Subtract each output from the total fee.
		for _, out := range tx.MsgTx().TxOut {
			fee -= out.Value
		}

		weight += blockchain.GetTransactionWeight(tx)
	}

	return 1000 * fee / weight, nil
}
