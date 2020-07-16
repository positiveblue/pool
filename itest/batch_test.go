package itest

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/llm/clmrpc"
	"github.com/lightninglabs/llm/clmscript"
	"github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightningnetwork/lnd/lnrpc"
)

// testBatchExecution is an end-to-end test of the entire system. In this test,
// we'll create two new traders and accounts for each trader. The traders will
// then submit orders we know will match, causing us to trigger a manual batch
// tick. From there the batch should proceed all the way to broadcasting the
// batch execution transaction. From there, all channels created should be
// operational and useable.
func testBatchExecution(t *harnessTest) {
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
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	if err != nil {
		t.Fatalf("unable to send coins to carol: %v", err)
	}

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders. To test the message multi-plexing between token IDs
	// and accounts, we add a secondary account to the second trader.
	account1 := openAccountAndAssert(
		t, t.trader, &clmrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	account2 := openAccountAndAssert(
		t, secondTrader, &clmrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	account3 := openAccountAndAssert(
		t, secondTrader, &clmrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// Now that the accounts are confirmed, submit an ask order from our
	// default trader, selling 15 units (1.5M sats) of liquidity.
	const orderFixedRate = 100
	askAmt := btcutil.Amount(1_500_000)
	ask1Nonce, err := submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate, askAmt,
		2*dayInBlocks, uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit ask order: %v", err)
	}

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity. So let's submit an order for that.
	bidAmt := btcutil.Amount(800_000)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate, bidAmt,
		dayInBlocks, uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit bid order: %v", err)
	}

	// From the secondary account of the second trader, we also create an
	// order to buy some units. The order should also make it into the same
	// batch and the second trader should sign a message for both orders at
	// the same time.
	bidAmt2 := btcutil.Amount(400_000)
	_, err = submitBidOrder(
		secondTrader, account3.TraderKey, orderFixedRate, bidAmt2,
		dayInBlocks, uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit bid order: %v", err)
	}

	// To ensure the venue is aware of account deposits/withdrawals, we'll
	// process a deposit for the account behind the ask.
	depositResp, err := t.trader.DepositAccount(ctx, &clmrpc.DepositAccountRequest{
		TraderKey:   account1.TraderKey,
		AmountSat:   100_000,
		SatPerVbyte: 1,
	})
	if err != nil {
		t.Fatalf("could not deposit into account: %v", err)
	}

	// We should expect to see the transaction causing the deposit.
	depositTxid, _ := chainhash.NewHash(depositResp.Account.Outpoint.Txid)
	txids, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.Fatalf("deposit transaction not found in mempool: %v", err)
	}
	if !txids[0].IsEqual(depositTxid) {
		t.Fatalf("found mempool transaction %v instead of %v",
			txids[0], depositTxid)
	}

	// Let's go ahead and confirm it. The account should remain in
	// PendingUpdate as it hasn't met all of the required confirmations.
	block := mineBlocks(t, t.lndHarness, 1, 1)[0]
	_ = assertTxInBlock(t, block, depositTxid)
	assertAuctioneerAccountState(
		t, depositResp.Account.TraderKey, account.StatePendingUpdate,
	)

	// Let's kick the auctioneer now to try and create a batch.
	_, err = t.auctioneer.AuctionAdminClient.BatchTick(
		ctx, &adminrpc.EmptyRequest{},
	)
	if err != nil {
		t.Fatalf("could not trigger batch tick: %v", err)
	}

	// Since the ask account is pending an update, a batch should not be
	// cleared, so a batch transaction should not be broadcast.
	//
	// TODO: Determine whether a batch has been made without waiting for the
	// mempool timeout? Waiting is not ideal here as it slows down the test.
	isMempoolEmpty, err := isMempoolEmpty(
		t.lndHarness.Miner.Node, minerMempoolTimeout/2,
	)
	if err != nil {
		t.Fatalf("unable to determine if mempool is empty: %v", err)
	}
	if !isMempoolEmpty {
		t.Fatalf("found unexpected non-empty mempool")
	}

	// Proceed to fully confirm the account deposit.
	_ = mineBlocks(t, t.lndHarness, 5, 0)
	assertAuctioneerAccountState(
		t, depositResp.Account.TraderKey, account.StateOpen,
	)

	// Let's kick the auctioneer once again to try and create a batch.
	_, err = t.auctioneer.AuctionAdminClient.BatchTick(
		ctx, &adminrpc.EmptyRequest{},
	)
	if err != nil {
		t.Fatalf("could not trigger batch tick: %v", err)
	}

	// At this point, the batch should now attempt to be cleared, and find
	// that we're able to make a market. Eventually the batch execution
	// transaction should be broadcast to the mempool.
	txids, err = waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.Fatalf("txid not found in mempool: %v", err)
	}

	if len(txids) != 1 {
		t.Fatalf("expected a single transaction, instead have: %v",
			spew.Sdump(txids))
	}
	batchTXID := txids[0]

	// At this point, the lnd nodes backed by each trader should have a
	// single pending channel, which matches the amount of the order
	// executed above.
	//
	// In our case, Bob is the maker so he should be marked as the
	// initiator of the channel.
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bidAmt, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt, false, t.trader.cfg.LndNode.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt2, false, t.trader.cfg.LndNode.PubKey,
	)

	// We'll now mine a block to confirm the channel. We should find the
	// channel in the listchannels output for both nodes, and the
	// thaw_height should be set accordingly.
	blocks := mineBlocks(t, t.lndHarness, 1, 1)

	// The block above should contain the batch transaction found in the
	// mempool above.
	assertTxInBlock(t, blocks[0], batchTXID)

	// The master account from the server's PoV should have the same txid
	// hash as this mined block.
	ctxb := context.Background()
	masterAcct, err := t.auctioneer.AuctionAdminClient.MasterAccount(
		ctxb, &adminrpc.EmptyRequest{},
	)
	if err != nil {
		t.Fatalf("unable to read master acct: %v", err)
	}
	acctOutPoint := masterAcct.Outpoint
	if !bytes.Equal(acctOutPoint.Txid, batchTXID[:]) {
		t.Fatalf("master account mismatch: expected %v, got %x",
			batchTXID, acctOutPoint.Txid)
	}

	// We'll now mine another 3 blocks to ensure the channel itself is
	// fully confirmed.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// Now that the channels are confirmed, they should both be active, and
	// we should be able to make a payment between this new channel
	// established.
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt), *batchTXID,
		charlie.PubKey, dayInBlocks,
	)
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt2), *batchTXID,
		charlie.PubKey, dayInBlocks,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt), *batchTXID,
		t.trader.cfg.LndNode.PubKey, dayInBlocks,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt2), *batchTXID,
		t.trader.cfg.LndNode.PubKey, dayInBlocks,
	)

	// To make sure the channels works as expected, we'll send a payment
	// from Bob (the maker) to Charlie (the taker).
	payAmt := btcutil.Amount(100)
	invoice := &lnrpc.Invoice{
		Memo:  "testing",
		Value: int64(payAmt),
	}
	ctxt, cancel := context.WithTimeout(ctxb, defaultWaitTimeout)
	defer cancel()
	resp, err := charlie.AddInvoice(ctxt, invoice)
	if err != nil {
		t.Fatalf("unable to add invoice: %v", err)
	}

	ctxt, cancel = context.WithTimeout(ctxb, defaultWaitTimeout)
	defer cancel()
	err = completePaymentRequests(
		ctxt, t.trader.cfg.LndNode, []string{resp.PaymentRequest}, true,
	)
	if err != nil {
		t.Fatalf("unable to send payments: %v", err)
	}

	// Now that the batch has been fully executed, we'll ensure that all
	// the expected state has been updated from the client's PoV.
	//
	// Charlie, the trader that just bought a channel should have no
	// present orders.
	assertNoOrders(t, secondTrader)

	// The party with the sell orders open should still have a single open
	// ask order with 300k unfilled (3 units).
	assertAskOrderState(t, t.trader, 3, ask1Nonce)

	// We should now be able to find this snapshot. As this is the only
	// batch created atm, we pass a nil batch ID so it'll look up the prior
	// batch.
	firstBatchID := assertBatchSnapshot(
		t, nil, secondTrader,
		[]uint64{uint64(bidAmt), uint64(bidAmt2)}, orderFixedRate,
	)

	// We'll now do an additional round to ensure that we're able to
	// fulfill back to back batches. In this round, Charlie will submit
	// another order for 3 units, which should be matched with Bob's
	// remaining Ask order that should now have zero units remaining.
	bidAmt3 := btcutil.Amount(300_000)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, 100, bidAmt3, dayInBlocks,
		uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit ask order: %v", err)
	}

	// We'll now tick off another batch, which should trigger a clearing of
	// the market, to produce another channel which Charlie has just
	// purchased.  Let's kick the auctioneer now to try and create a batch.
	_, err = t.auctioneer.AuctionAdminClient.BatchTick(
		ctx, &adminrpc.EmptyRequest{},
	)
	if err != nil {
		t.Fatalf("could not trigger batch tick: %v", err)
	}

	// We should find another transaction in the mempool, and that both
	// parties once again have a pending channel.
	txids, err = waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.Fatalf("txid not found in mempool: %v", err)
	}

	if len(txids) != 1 {
		t.Fatalf("expected a single transaction, instead have: %v",
			spew.Sdump(txids))
	}
	batchTXID = txids[0]
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bidAmt3, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt3, false, t.trader.cfg.LndNode.PubKey,
	)
	blocks = mineBlocks(t, t.lndHarness, 1, 1)
	assertTxInBlock(t, blocks[0], batchTXID)

	// We'll conclude by mining enough blocks to have the channels be
	// confirmed.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// At this point, both traders should have no outstanding orders as
	// they've all be bundled up into a batch.
	assertNoOrders(t, secondTrader)
	assertNoOrders(t, t.trader)

	// We should also be able to find this batch as well, with only 2
	// orders being included in the batch. This time, we'll query with the
	// exact batch ID we expect.
	firstBatchKey, err := btcec.ParsePubKey(firstBatchID[:], btcec.S256())
	if err != nil {
		t.Fatalf("unable to decode first batch key: %v", err)
	}
	secondBatchKey := clmscript.IncrementKey(firstBatchKey)
	secondBatchID := secondBatchKey.SerializeCompressed()
	assertBatchSnapshot(
		t, secondBatchID, secondTrader, []uint64{uint64(bidAmt3)},
		orderFixedRate,
	)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	chanReq := &lnrpc.ListChannelsRequest{}
	openChans, err := charlie.ListChannels(context.Background(), chanReq)
	if err != nil {
		t.Fatalf("unable to list charlie's channels: %v", err)
	}
	for _, openChan := range openChans.Channels {
		chanPointStr := openChan.ChannelPoint
		chanPointParts := strings.Split(chanPointStr, ":")
		txid, err := chainhash.NewHashFromStr(chanPointParts[0])
		if err != nil {
			t.Fatalf("unable txid to convert to hash: %v", err)
		}
		index, err := strconv.Atoi(chanPointParts[1])
		if err != nil {
			t.Fatalf("unable to convert string to int: %v", err)
		}

		chanPoint := &lnrpc.ChannelPoint{
			FundingTxid: &lnrpc.ChannelPoint_FundingTxidBytes{
				FundingTxidBytes: txid[:],
			},
			OutputIndex: uint32(index),
		}
		closeUpdates, _, err := t.lndHarness.CloseChannel(
			ctx, charlie, chanPoint, false,
		)
		if err != nil {
			t.Fatalf("unable to close channel: %v", err)
		}

		assertChannelClosed(
			ctx, t, t.lndHarness, charlie, chanPoint, closeUpdates,
			false,
		)
	}

}

// assertBatchSnapshot asserts that a batch identified by the passed batchID is
// found and includes the specified orders, cleared at the target clearing
// rate. This method also returns ID of the queried batch.
//
// TODO(roasbeef): update to assert order nonce and other info once the admin
// RPC stuff is in
func assertBatchSnapshot(t *harnessTest, batchID []byte, trader *traderHarness,
	expectedOrderAmts []uint64,
	clearingPrice order.FixedRatePremium) order.BatchID {

	ctxb := context.Background()
	batchSnapshot, err := trader.BatchSnapshot(
		ctxb,
		&clmrpc.BatchSnapshotRequest{
			BatchId: batchID,
		},
	)
	if err != nil {
		t.Fatalf("unable to obtain batch snapshot: %v", err)
	}

	// The final clearing price should match the expected fixed rate passed
	// in.
	if batchSnapshot.ClearingPriceRate != uint32(clearingPrice) {
		t.Fatalf("wrong clearing price: expected %v, got %v",
			clearingPrice, batchSnapshot.ClearingPriceRate)
	}

	// Next we'll compile a map of the included ask and bid orders so we
	// can assert the existence of the orders we created above.
	matchedOrderAmts := make(map[uint64]struct{})
	for _, order := range batchSnapshot.MatchedOrders {
		matchedOrderAmts[order.TotalSatsCleared] = struct{}{}
	}

	// Next we'll assert that all the expected orders have been found in
	// this batch.
	for _, orderAmt := range expectedOrderAmts {
		if _, ok := matchedOrderAmts[orderAmt]; !ok {
			t.Fatalf("order amt %v not found in batch", orderAmt)
		}
	}

	var serverbatchID order.BatchID
	copy(serverbatchID[:], batchSnapshot.BatchId)

	return serverbatchID
}

// testServiceLevelEnforcement ensures that the auctioneer correctly enforces
// a channel's lifetime by punishing the offending trader (the maker).
func testServiceLevelEnforcement(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader.
	charlie, err := t.lndHarness.NewNode("charlie", nil)
	if err != nil {
		t.Fatalf("unable to set up charlie: %v", err)
	}
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	if err != nil {
		t.Fatalf("unable to send coins to carol: %v", err)
	}

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders.
	account1 := openAccountAndAssert(
		t, t.trader, &clmrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	account2 := openAccountAndAssert(
		t, secondTrader, &clmrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// Now that the accounts are confirmed, submit an ask order from our
	// default trader, selling 15 units (1.5M sats) of liquidity.
	askAmt := btcutil.Amount(1_500_000)
	_, err = submitAskOrder(
		t.trader, account1.TraderKey, 100, askAmt, 2*dayInBlocks,
		uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit ask order: %v", err)
	}

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity. So let's submit an order for that.
	bidAmt := btcutil.Amount(800_000)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, 100, bidAmt, dayInBlocks,
		uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit bid order: %v", err)
	}

	// Let's kick the auctioneer now to try and create a batch.
	_, err = t.auctioneer.AuctionAdminClient.BatchTick(
		ctx, &adminrpc.EmptyRequest{},
	)
	if err != nil {
		t.Fatalf("could not trigger batch tick: %v", err)
	}

	// At this point, the batch should now attempt to be cleared, and find
	// that we're able to make a market. Eventually the batch execution
	// transaction should be broadcast to the mempool.
	txids, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.Fatalf("txid not found in mempool: %v", err)
	}

	if len(txids) != 1 {
		t.Fatalf("expected a single transaction, instead have: %v",
			spew.Sdump(txids))
	}
	batchTXID := txids[0]

	// At this point, the lnd nodes backed by each trader should have a
	// single pending channel, which matches the amount of the order
	// executed above.
	//
	// In our case, Bob is the maker so he should be marked as the
	// initiator of the channel.
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bidAmt, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt, false, t.trader.cfg.LndNode.PubKey,
	)

	// We'll now mine a block to confirm the channel. We should find the
	// channel in the listchannels output for both nodes, and the
	// thaw_height should be set accordingly.
	blocks := mineBlocks(t, t.lndHarness, 1, 1)

	// The block above should contain the batch transaction found in the
	// mempool above.
	assertTxInBlock(t, blocks[0], batchTXID)

	// We'll now mine another 3 blocks to ensure the channel itself is
	// fully confirmed.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// Now that the channels are confirmed, they should both be active, and
	// we should be able to make a payment between this new channel
	// established.
	chanPoint := assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt), *batchTXID,
		charlie.PubKey, dayInBlocks,
	)
	_ = assertActiveChannel(
		t, charlie, int64(bidAmt), *batchTXID,
		t.trader.cfg.LndNode.PubKey, dayInBlocks,
	)

	// Proceed to force close the channel from the initiator. They should be
	// banned accordingly.
	ctxt, cancel := context.WithTimeout(ctx, defaultWaitTimeout)
	defer cancel()
	closeUpdates, _, err := t.lndHarness.CloseChannel(
		ctxt, t.trader.cfg.LndNode, chanPoint, true,
	)
	if err != nil {
		t.Fatalf("unable to force close channel: %v", err)
	}
	assertChannelClosed(
		ctx, t, t.lndHarness, t.trader.cfg.LndNode, chanPoint,
		closeUpdates, true,
	)

	// The trader responsible should no longer be able to modify their
	// account or submit orders.
	_, err = submitAskOrder(
		t.trader, account1.TraderKey, 100, 100_000, 2*dayInBlocks,
		uint32(order.CurrentVersion),
	)
	if err == nil || !strings.Contains(err.Error(), "banned") {
		t.Fatalf("expected order submission to fail due to account ban")
	}
	_, err = t.trader.DepositAccount(ctx, &clmrpc.DepositAccountRequest{
		TraderKey:   account1.TraderKey,
		AmountSat:   1000,
		SatPerVbyte: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "banned") {
		t.Fatalf("expected account deposit to fail due to account ban")
	}

	// The offended trader should still be able to however.
	_, err = submitAskOrder(
		secondTrader, account2.TraderKey, 100, 100_000, 2*dayInBlocks,
		uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("expected order submission to succeed: %v", err)
	}
}
