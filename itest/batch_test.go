package itest

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/account"
	auctioneerAccount "github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
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
	defer shutdownAndAssert(t, charlie, secondTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	if err != nil {
		t.Fatalf("unable to send coins to carol: %v", err)
	}

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders. To test the message multi-plexing between token IDs
	// and accounts, we add a secondary account to the second trader.
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
	account3 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// We'll add a third trader that we will shut down after placing an
	// order, to ensure batch execution still can proceed.
	dave, err := t.lndHarness.NewNode("dave", lndArgs)
	if err != nil {
		t.Fatalf("unable to set up charlie: %v", err)
	}
	thirdTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
	)
	defer shutdownAndAssert(t, dave, thirdTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, dave)
	if err != nil {
		t.Fatalf("unable to send coins to carol: %v", err)
	}

	account4 := openAccountAndAssert(
		t, thirdTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
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

	// Make the third account submit a bid that will get matched, but shut
	// down the trader immediately after.
	_, err = submitBidOrder(
		thirdTrader, account4.TraderKey, orderFixedRate, bidAmt2,
		dayInBlocks, uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit bid order: %v", err)
	}

	if err := thirdTrader.stop(false); err != nil {
		t.Fatalf("unable to stop trader %v", err)
	}

	// To ensure the venue is aware of account deposits/withdrawals, we'll
	// process a deposit for the account behind the ask.
	depositResp, err := t.trader.DepositAccount(ctx, &poolrpc.DepositAccountRequest{
		TraderKey:       account1.TraderKey,
		AmountSat:       100_000,
		FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
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

	// Since the ask account is pending an update, a batch should not be
	// cleared, so a batch transaction should not be broadcast. Kick the
	// auctioneer and wait for it to return back to the order submit state.
	// No tx should be in the mempool as no market should be possible.
	_, _ = executeBatch(t, 0)

	// Proceed to fully confirm the account deposit.
	_ = mineBlocks(t, t.lndHarness, 5, 0)
	assertAuctioneerAccountState(
		t, depositResp.Account.TraderKey, account.StateOpen,
	)

	// Let's kick the auctioneer once again to try and create a batch.
	_, batchTXIDs := executeBatch(t, 1)
	firstBatchTXID := batchTXIDs[0]

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

	// We'll also make sure that the account now is in the special state
	// where it is allowed to participate in the next batch without on-chain
	// confirmation.
	assertAuctioneerAccountState(
		t, account1.TraderKey, account.StatePendingBatch,
	)

	// We'll now mine a block to confirm the channel. We should find the
	// channel in the listchannels output for both nodes, and the
	// thaw_height should be set accordingly.
	blocks := mineBlocks(t, t.lndHarness, 1, 1)

	// The block above should contain the batch transaction found in the
	// mempool above.
	assertTxInBlock(t, blocks[0], firstBatchTXID)

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
	if !bytes.Equal(acctOutPoint.Txid, firstBatchTXID[:]) {
		t.Fatalf("master account mismatch: expected %v, got %x",
			firstBatchTXID, acctOutPoint.Txid)
	}

	// We'll now mine another 3 blocks to ensure the channel itself is
	// fully confirmed.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// Now that the channels are confirmed, they should both be active, and
	// we should be able to make a payment between this new channel
	// established.
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt), *firstBatchTXID,
		charlie.PubKey, dayInBlocks,
	)
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt2), *firstBatchTXID,
		charlie.PubKey, dayInBlocks,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, dayInBlocks,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt2), *firstBatchTXID,
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
	assertTraderAssets(t, t.trader, 2, []*chainhash.Hash{firstBatchTXID})
	assertTraderAssets(t, secondTrader, 2, []*chainhash.Hash{firstBatchTXID})

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
	// purchased. Let's kick the auctioneer now to try and create a batch.
	_, batchTXIDs = executeBatch(t, 1)
	secondBatchTXID := batchTXIDs[0]

	// Both parties should once again have a pending channel.
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bidAmt3, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt3, false, t.trader.cfg.LndNode.PubKey,
	)
	blocks = mineBlocks(t, t.lndHarness, 1, 1)
	assertTxInBlock(t, blocks[0], secondBatchTXID)

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
	secondBatchKey := poolscript.IncrementKey(firstBatchKey)
	secondBatchID := secondBatchKey.SerializeCompressed()
	assertBatchSnapshot(
		t, secondBatchID, secondTrader, []uint64{uint64(bidAmt3)},
		orderFixedRate,
	)
	batchTXIDs = []*chainhash.Hash{firstBatchTXID, secondBatchTXID}
	assertTraderAssets(t, t.trader, 3, batchTXIDs)
	assertTraderAssets(t, secondTrader, 3, batchTXIDs)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
}

// closeAllChannals closes and asserts all channels to node are closed.
func closeAllChannels(ctx context.Context, t *harnessTest,
	node *lntest.HarnessNode) {

	chanReq := &lnrpc.ListChannelsRequest{}
	openChans, err := node.ListChannels(context.Background(), chanReq)
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
			ctx, node, chanPoint, false,
		)
		if err != nil {
			t.Fatalf("unable to close channel: %v", err)
		}

		assertChannelClosed(
			ctx, t, t.lndHarness, node, chanPoint, closeUpdates,
			false,
		)
	}
}

// testUnconfirmedBatchChain tests that the server supports publishing batches
// even though previous batches remain unconfirmed.
func testUnconfirmedBatchChain(t *harnessTest) {
	ctx := context.Background()

	const unconfirmedBatches = 6

	// We need a third lnd node, Charlie that is used for the second
	// trader. We'll make sure to support all the pending channels that
	// will be opened towards him.
	lndArgs := []string{
		fmt.Sprintf("--maxpendingchannels=%d", unconfirmedBatches),
	}
	charlie, err := t.lndHarness.NewNode("charlie", lndArgs)
	if err != nil {
		t.Fatalf("unable to set up charlie: %v", err)
	}
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)

	// We fund both traders so they have enough funds to open multiple
	// accounts.
	walletAmt := btcutil.Amount(
		(unconfirmedBatches + 1) * defaultAccountValue,
	)
	err = t.lndHarness.SendCoins(ctx, walletAmt, charlie)
	if err != nil {
		t.Fatalf("unable to send coins to carol: %v", err)
	}
	err = t.lndHarness.SendCoins(ctx, walletAmt, t.trader.cfg.LndNode)
	if err != nil {
		t.Fatalf("unable to send coins to carol: %v", err)
	}

	type accountPair struct {
		account1 *poolrpc.Account
		account2 *poolrpc.Account
	}

	var accounts []accountPair
	var batchTXIDs []*chainhash.Hash

	// We start by opening a number of account pairs, that will each
	// be involved in one match in each batch.
	for i := 0; i < unconfirmedBatches; i++ {
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

		accounts = append(accounts, accountPair{account1, account2})
	}

	const orderFixedRate = 100
	const baseOrderAmt = btcutil.Amount(300_000)

	// We execute a list of batches, each lingering unconfirmed in the
	// mempool.
	for i := 0; i < unconfirmedBatches; i++ {
		account1 := accounts[i].account1
		account2 := accounts[i].account2

		// We'll make sure to have each batch open a channel of a
		// different size, to easily distinguish them after opening.
		chanAmt := btcutil.Amount(i+1) * baseOrderAmt

		_, err := submitAskOrder(
			t.trader, account1.TraderKey, orderFixedRate, chanAmt,
			2*dayInBlocks, uint32(order.CurrentVersion),
		)
		if err != nil {
			t.Fatalf("could not submit ask order: %v", err)
		}

		_, err = submitBidOrder(
			secondTrader, account2.TraderKey, orderFixedRate, chanAmt,
			dayInBlocks, uint32(order.CurrentVersion),
		)
		if err != nil {
			t.Fatalf("could not submit bid order: %v", err)
		}

		// Let's kick the auctioneer to try and create a batch.
		_, err = t.auctioneer.AuctionAdminClient.BatchTick(
			ctx, &adminrpc.EmptyRequest{},
		)
		if err != nil {
			t.Fatalf("could not trigger batch tick: %v", err)
		}

		// At this point, all batches created up to this point should
		// be found in the mempool.
		txids, err := waitForNTxsInMempool(
			t.lndHarness.Miner.Node, i+1, minerMempoolTimeout,
		)
		if err != nil {
			t.Fatalf("txid not found in mempool: %v", err)
		}

		if len(txids) != i+1 {
			t.Fatalf("expected %d transactions, instead have: %v",
				i+1, spew.Sdump(txids))
		}

		// At this point, the lnd nodes backed by each trader should
		// have a channel included in the just executed batch.
		assertPendingChannel(
			t, t.trader.cfg.LndNode, chanAmt, true, charlie.PubKey,
		)
		assertPendingChannel(
			t, charlie, chanAmt, false, t.trader.cfg.LndNode.PubKey,
		)

		// Get the latest batch ID for later.
		ctxb := context.Background()
		masterAcct, err := t.auctioneer.AuctionAdminClient.MasterAccount(
			ctxb, &adminrpc.EmptyRequest{},
		)
		if err != nil {
			t.Fatalf("unable to read master acct: %v", err)
		}
		txid, _ := chainhash.NewHash(masterAcct.Outpoint.Txid)
		batchTXIDs = append(batchTXIDs, txid)
	}

	// We'll now mine a block to confirm all the unconfirmed batches.
	blocks := mineBlocks(t, t.lndHarness, 1, unconfirmedBatches)

	// The block above should contain all the retrieved batch IDs.
	for _, batchTXID := range batchTXIDs {
		assertTxInBlock(t, blocks[0], batchTXID)
	}

	// We'll now mine another 3 blocks to ensure the channels are fully
	// operational.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// Now that the channels are confirmed, they should both be active.
	for i, batchTXID := range batchTXIDs {
		chanAmt := btcutil.Amount(i+1) * baseOrderAmt
		assertActiveChannel(
			t, t.trader.cfg.LndNode, int64(chanAmt), *batchTXID,
			charlie.PubKey, dayInBlocks,
		)

		assertActiveChannel(
			t, charlie, int64(chanAmt), *batchTXID,
			t.trader.cfg.LndNode.PubKey, dayInBlocks,
		)
	}

	// Now that the batches have been fully executed, we'll ensure that all
	// the expected state has been updated from the client's PoV.
	//
	// Charlie, the trader that just bought a channel should have no
	// present orders.
	assertNoOrders(t, secondTrader)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
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
		&poolrpc.BatchSnapshotRequest{
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

// assertTraderAssets ensures that the given trader has the expected number of
// assets with any of the given transaction hashes.
func assertTraderAssets(t *harnessTest, trader *traderHarness, numAssets int,
	txids []*chainhash.Hash) {

	resp, err := trader.Leases(context.Background(), &poolrpc.LeasesRequest{})
	require.NoErrorf(t.t, err, "unable to retrieve assets")
	require.Equalf(t.t, numAssets, len(resp.Leases), "num assets mismatch")

	txidSet := make(map[chainhash.Hash]struct{}, len(resp.Leases))
	for _, txid := range txids {
		txidSet[*txid] = struct{}{}
	}

	for _, asset := range resp.Leases {
		assetTxid, err := chainhash.NewHash(asset.ChannelPoint.Txid)
		require.NoError(t.t, err)
		require.Contains(t.t, txidSet, *assetTxid)
	}
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
	_, batchTXIDs := executeBatch(t, 1)
	batchTXID := batchTXIDs[0]

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
	_, err = t.trader.DepositAccount(ctx, &poolrpc.DepositAccountRequest{
		TraderKey:       account1.TraderKey,
		AmountSat:       1000,
		FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
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

// testBatchExecutionDustOutputs checks that an account is considered closed if
// the remaining balance is below the dust limit.
func testBatchExecutionDustOutputs(t *harnessTest) {
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

	// Create an account for the maker with plenty of sats.
	account1 := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// We'll open a minimum sized account we'll use for bidding.
	accountAmt := btcutil.Amount(100_000)
	account2 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: uint64(accountAmt),
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// We'll create an order that matches with a ridiculously high
	// premium, in order to make what's left on the bidders account into
	// dust.
	//
	// 635_000 per billion of 100_000 sats for 1440 blocks is 91_440 sats,
	// so the trader will only have 8560 sats left to pay for chain fees
	// and execution fees.
	//
	// The execution fee is 101 sats, chain fee 8162 sats (at the static
	// fee rate of 12500 s/kw), so what is left will be dust (< 678 sats).
	const orderSize = 100_000
	const matchRate = 635_000
	const durationBlocks = 1440

	// Submit an ask an bid which will match exactly.
	_, err = submitAskOrder(
		t.trader, account1.TraderKey, matchRate, orderSize,
		durationBlocks, uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit ask order: %v", err)
	}

	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, matchRate, orderSize,
		durationBlocks, uint32(order.CurrentVersion),
	)
	if err != nil {
		t.Fatalf("could not submit bid order: %v", err)
	}

	// Let's kick the auctioneer now to try and create a batch.
	_, batchTXIDs := executeBatch(t, 1)
	batchTXID := batchTXIDs[0]
	assertPendingChannel(
		t, t.trader.cfg.LndNode, orderSize, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, orderSize, false, t.trader.cfg.LndNode.PubKey,
	)
	blocks := mineBlocks(t, t.lndHarness, 1, 1)
	batchTx := assertTxInBlock(t, blocks[0], batchTXID)

	// The batch tx should have 3 inputs: the master account and the two
	// traders.
	if len(batchTx.TxIn) != 3 {
		t.Fatalf("expected 3 inputs, found %d", len(batchTx.TxIn))
	}

	// There should be 3 outputs: master account, the new channel, and the
	// first trader. The second trader's output is dust and does not
	// materialize.
	if len(batchTx.TxOut) != 3 {
		t.Fatalf("expected 3 outputs, found %d", len(batchTx.TxOut))
	}

	// We'll conclude by mining enough blocks to have the channels be
	// confirmed.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// At this point, both traders should have no outstanding orders as
	// they've all be bundled up into a batch.
	assertNoOrders(t, secondTrader)
	assertNoOrders(t, t.trader)

	// Now the first account should still have money left and be open,
	// while the second account only have dust left and should be closed.
	assertTraderAccountState(
		t.t, t.trader, account1.TraderKey, poolrpc.AccountState_OPEN,
	)
	assertTraderAccountState(
		t.t, secondTrader, account2.TraderKey, poolrpc.AccountState_CLOSED,
	)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
}

// testConsecutiveBatches tests that accounts can participate in consecutive
// batches if their account state doesn't include any pending state from a
// withdrawal or deposit.
func testConsecutiveBatches(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader.
	lndArgs := []string{"--maxpendingchannels=2"}
	charlie, err := t.lndHarness.NewNode("charlie", lndArgs)
	require.NoError(t.t, err)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	require.NoError(t.t, err)

	// Create an account for the maker with plenty of sats.
	askAccount := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// We'll also create accounts for the bidder as well.
	bidAccount := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// We'll create an ask order that is large enough to be matched three
	// times. Charlie will try to buy that three times with different bid
	// sizes (so we can distinguish between the channels) but only succeed
	// the first two times.
	const askSize = 600_000
	const bid1Size = 100_000
	const bid2Size = 200_000
	const bid3Size = 300_000
	const askRate = 20
	const durationBlocks = 144

	// Submit an ask an bid that matches one third of the ask.
	_, err = submitAskOrder(
		t.trader, askAccount.TraderKey, askRate, askSize,
		durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, bidAccount.TraderKey, askRate, bid1Size,
		durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)

	// Execute the batch and make sure there's a channel between Bob and
	// Charlie now.
	_, _ = executeBatch(t, 1)
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bid1Size, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bid1Size, false, t.trader.cfg.LndNode.PubKey,
	)

	// Now the ask still has 5 units left. Without confirming the batch,
	// let's create another bid order and then execute a batch, adding to
	// the unconfirmed chain of batches.
	_, err = submitBidOrder(
		secondTrader, bidAccount.TraderKey, askRate, bid2Size,
		durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)

	// We'll now restart Charlie's trader daemon.
	err = secondTrader.stop(false)
	require.NoError(t.t, err)

	// Before restarting, we'll attempt a batch while Charlie is offline.
	// Since the batch depends on their order, no batch should occur.
	_, _ = executeBatch(t, 1)

	// Restart Charlie to ensure they can participate in the next batch.
	err = secondTrader.start()
	require.NoError(t.t, err)

	// Execute the batch and make sure there's a second channel between Bob
	// and Charlie now.
	_, _ = executeBatch(t, 2)
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bid2Size, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bid2Size, false, t.trader.cfg.LndNode.PubKey,
	)

	// Let's now confirm the previous two batches to clear the mempool and
	// reset the account states to open (we don't care about the balance).
	_ = mineBlocks(t, t.lndHarness, 6, 2)
	assertTraderAccountState(
		t.t, secondTrader, bidAccount.TraderKey,
		poolrpc.AccountState_OPEN,
	)
	assertAuctioneerAccountState(
		t, bidAccount.TraderKey, auctioneerAccount.StateOpen,
	)

	// Let's add a pending withdrawal to the account then try making a batch
	// again. This time no batch should be possible.
	_, _ = withdrawAccountAndAssertMempool(
		t, secondTrader, bidAccount.TraderKey, -1, bidAccount.Value/2,
		validTestAddr,
	)
	_, err = submitBidOrder(
		secondTrader, bidAccount.TraderKey, askRate, bid3Size,
		durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)

	// Execute another batch now and wait until it's completed. There should
	// not be a new batch transaction in the mempool (only the withdrawal
	// TX) as we don't expect a batch to have been successful.
	_, _ = executeBatch(t, 1)

	// Let's now mine the withdrawal to clean up the mempool.
	_ = mineBlocks(t, t.lndHarness, 3, 1)
}

// testTraderPartialRejectNewNodesOnly tests that the server supports traders
// rejecting parts of a batch. If some orders within a batch are rejected by a
// trader (for example because they have the --newnodesonly flag set), a new
// match making process is started. We also test that trader accounts can take
// part in subsequent batches and don't need 3 confirmations first.
func testTraderPartialRejectNewNodesOnly(t *harnessTest) {
	ctx := context.Background()

	// We need a third and fourth lnd node, Charlie and Dave that are used
	// for the second and third trader. Charlie is very picky and only wants
	// channels from new nodes.
	charlie, err := t.lndHarness.NewNode("charlie", nil)
	require.NoError(t.t, err)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
		newNodesOnlyOpt(),
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	require.NoError(t.t, err)

	dave, err := t.lndHarness.NewNode("dave", nil)
	require.NoError(t.t, err)
	thirdTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
	)
	defer shutdownAndAssert(t, dave, thirdTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, dave)
	require.NoError(t.t, err)

	// Create an account for the maker with plenty of sats.
	askAccount := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// We'll also create accounts for both bidders as well.
	bidAccountCharlie := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	bidAccountDave := openAccountAndAssert(
		t, thirdTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// We'll create an ask order that is large enough to be matched twice.
	// First, Charlie will buy half of that ask in batch 1.
	const askSize = 400_000
	const askRate = 2000
	const durationBlocks = 1440

	// Submit an ask an bid that matches half of the ask.
	_, err = submitAskOrder(
		t.trader, askAccount.TraderKey, askRate, askSize,
		durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, bidAccountCharlie.TraderKey, askRate, askSize/2,
		durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)

	// Execute the batch and make sure there's a channel between Bob and
	// Charlie now.
	_, _ = executeBatch(t, 1)
	assertPendingChannel(
		t, t.trader.cfg.LndNode, askSize/2, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, askSize/2, false, t.trader.cfg.LndNode.PubKey,
	)

	// Now the ask still has 2 units left. We'll now create competing bid
	// orders for those 2 units. Charlie is willing to pay double the rate
	// than Dave so he should be matched first. But because Charlie already
	// has a channel, he will reject the batch. Matchmaking will be retried
	// and the second time Dave will get the remaining 2 units.
	_, err = submitBidOrder(
		secondTrader, bidAccountCharlie.TraderKey, askRate*2,
		askSize/2, durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		thirdTrader, bidAccountDave.TraderKey, askRate,
		askSize/2, durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)

	// Execute the batch and make sure there's a channel between Bob and
	// Dave now.
	_, _ = executeBatch(t, 2)
	assertPendingChannel(
		t, t.trader.cfg.LndNode, askSize/2, true, dave.PubKey,
	)
	assertPendingChannel(
		t, dave, askSize/2, false, t.trader.cfg.LndNode.PubKey,
	)

	// Let's now mine the batch to clean up the mempool and make sure Bob's
	// and Dave's accounts are confirmed again.
	_ = mineBlocks(t, t.lndHarness, 3, 2)
	assertTraderAccountState(
		t.t, t.trader, askAccount.TraderKey, poolrpc.AccountState_OPEN,
	)
	assertTraderAccountState(
		t.t, thirdTrader, bidAccountDave.TraderKey,
		poolrpc.AccountState_OPEN,
	)
}

// testTraderPartialRejectFundingFailure tests that the server supports traders
// rejecting parts of a batch because of a funding failure.
func testTraderPartialRejectFundingFailure(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie, that is used for the second
	// trader. Charlie can only have one pending incoming channel (default
	// setting of lnd).
	charlie, err := t.lndHarness.NewNode("charlie", nil)
	require.NoError(t.t, err)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	require.NoError(t.t, err)

	// And as an "innocent bystander" we also create Dave who will buy a
	// channel during the second round so we can make sure being matched
	// multiple times in the same batch (because another node rejected and
	// the matchmaking had to be restarted) can still succeed.
	dave, err := t.lndHarness.NewNode("dave", nil)
	require.NoError(t.t, err)
	thirdTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
	)
	defer shutdownAndAssert(t, dave, thirdTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, dave)
	require.NoError(t.t, err)

	// Create an account for the maker and taker with plenty of sats.
	askAccount := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	bidAccountCharlie := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	bidAccountDave := openAccountAndAssert(
		t, thirdTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// We'll create an ask order that is large enough to be matched multiple
	// times. First, Charlie will buy half of that ask in batch 1.
	const askSize = 600_000
	const bidSize1 = 300_000
	const bidSize2 = 200_000
	const askRate = 2000
	const durationBlocks = 1440

	// Submit an ask order that is large enough to be matched multiple
	// times.
	_, err = submitAskOrder(
		t.trader, askAccount.TraderKey, askRate, askSize,
		durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)

	// We manually open a channel between Bob and Charlie now to saturate
	// the maxpendingchannels setting.
	err = t.lndHarness.EnsureConnected(ctx, t.trader.cfg.LndNode, charlie)
	require.NoError(t.t, err)
	_, err = t.lndHarness.OpenPendingChannel(
		ctx, t.trader.cfg.LndNode, charlie, 1_000_000, 0,
	)
	require.NoError(t.t, err)

	// Let's now create a bid that will be matched against the ask but
	// should result in Charlie rejecting it because of too many pending
	// channels. That is, the asker, Bob, will open the channel and Charlie
	// will reject the channel funding. That leads to both trader daemons
	// rejecting the batch because of a failed funding attempt.
	_, err = submitBidOrder(
		secondTrader, bidAccountCharlie.TraderKey, askRate, bidSize1,
		durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)

	// Dave also participates. He should be matched twice with a pending
	// channel already initiated during the first try. We need to make sure
	// the same matched order can still result in a channel the second time
	// around.
	_, err = submitBidOrder(
		thirdTrader, bidAccountDave.TraderKey, askRate, bidSize2,
		durationBlocks, uint32(order.CurrentVersion),
	)
	require.NoError(t.t, err)

	// Try to execute the batch now. It should fail in the first round
	// because both Bob and Charlie reject the orders. A conflict should
	// then be recorded in the conflict tracker and they shouldn't be
	// matched again. But the channel between Bob and Dave should still be
	// created.
	_, _ = executeBatch(t, 2)
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bidSize2, true, dave.PubKey,
	)
	assertPendingChannel(
		t, dave, bidSize2, false, t.trader.cfg.LndNode.PubKey,
	)

	// Because the first round never happened, Dave did create a channel
	// that will never confirm because it was replaced with a channel of the
	// second matched batch. That first version should have been cleaned up
	// by the trader daemon by abandoning it in lnd. We check that the
	// abandon mechanism worked by making sure there's only one pending
	// channel present now.
	assertNumPendingChannels(t, dave, 1)

	// Make sure we have a conflict reported from both sides.
	conflicts, err := t.auctioneer.FundingConflicts(
		ctx, &adminrpc.EmptyRequest{},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, 2, len(conflicts.Conflicts))

	askerNodeHex := hex.EncodeToString(t.trader.cfg.LndNode.PubKey[:])
	bidderNodeHex := hex.EncodeToString(charlie.PubKey[:])
	require.Equal(t.t, 1, len(conflicts.Conflicts[askerNodeHex].Conflicts))
	require.Equal(t.t, 1, len(conflicts.Conflicts[bidderNodeHex].Conflicts))

	askerConflict := conflicts.Conflicts[askerNodeHex].Conflicts[0]
	require.Equal(t.t, bidderNodeHex, askerConflict.Subject)
	require.Contains(t.t, askerConflict.Reason, "received funding error")
	require.Contains(t.t, askerConflict.Reason, "pending channels exceed")

	bidderConflict := conflicts.Conflicts[bidderNodeHex].Conflicts[0]
	require.Equal(t.t, askerNodeHex, bidderConflict.Subject)
	require.Contains(t.t, bidderConflict.Reason, "timed out waiting")

	// Let's now confirm the channel and try creating a batch again. It
	// should still fail because the conflicts are kept in memory over the
	// lifetime of the auctioneer.
	mineBlocks(t, t.lndHarness, 1, 2)
	_, _ = executeBatch(t, 0)

	// If we now clear the conflicts, we should be able to create the
	// batch and channel.
	_, err = t.auctioneer.ClearConflicts(ctx, &adminrpc.EmptyRequest{})
	require.NoError(t.t, err)
	_, _ = executeBatch(t, 1)
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bidSize1, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidSize1, false, t.trader.cfg.LndNode.PubKey,
	)
	mineBlocks(t, t.lndHarness, 1, 1)
}
