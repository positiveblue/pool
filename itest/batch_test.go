package itest

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/davecgh/go-spew/spew"
	accountT "github.com/lightninglabs/pool/account"
	"github.com/lightninglabs/pool/auctioneerrpc"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/account"
	auctioneerAccount "github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
)

var (
	scriptEnforcedType = auctioneerrpc.OrderChannelType_ORDER_CHANNEL_TYPE_SCRIPT_ENFORCED

	announceNoPreference = auctioneerrpc.ChannelAnnouncementConstraints_ANNOUNCEMENT_NO_PREFERENCE
	onlyUnannounced      = auctioneerrpc.ChannelAnnouncementConstraints_ONLY_UNANNOUNCED

	zcNoPreference        = auctioneerrpc.ChannelConfirmationConstraints_CONFIRMATION_NO_PREFERENCE
	onlyConfirmedChannels = auctioneerrpc.ChannelConfirmationConstraints_ONLY_CONFIRMED
	onlyZeroConfChannels  = auctioneerrpc.ChannelConfirmationConstraints_ONLY_ZEROCONF
)

// testBatchExecutionV0 is an end-to-end test of the entire system. In this test
// we'll create two new traders and accounts for each trader. The traders will
// then submit orders we know will match, causing us to trigger a manual batch
// tick. From there the batch should proceed all the way to broadcasting the
// batch execution transaction. From there, all channels created should be
// operational and usable. We test batch execution with the two trader accounts
// using v0 scripts. This test needs to be individual test cases (and not just
// subtests) so we can start with a fresh auctioneer master account.
func testBatchExecutionV0(t *harnessTest) {
	ctx := context.Background()
	runBatchExecutionTest(ctx, t, accountT.VersionInitialNoVersion, false)
}

// testBatchExecutionV1 is an end-to-end test of the entire system. In this test
// we'll create two new traders and accounts for each trader. The traders will
// then submit orders we know will match, causing us to trigger a manual batch
// tick. From there the batch should proceed all the way to broadcasting the
// batch execution transaction. From there, all channels created should be
// operational and usable. We test batch execution with all trader accounts
// using v1 scripts. This test needs to be individual test cases (and not just
// subtests) so we can start with a fresh auctioneer master account.
func testBatchExecutionV1(t *harnessTest) {
	ctx := context.Background()
	runBatchExecutionTest(ctx, t, accountT.VersionTaprootEnabled, false)
}

// testBatchExecutionV0ToV1 is an end-to-end test of the entire system. In this
// test we'll create two new traders and accounts for each trader. The traders
// will then submit orders we know will match, causing us to trigger a manual
// batch tick. From there the batch should proceed all the way to broadcasting
// the batch execution transaction. From there, all channels created should be
// operational and usable. We test batch execution with the two trader accounts
// using v0 scripts at the beginning and then upgrading to v1 during a batch.
// This test needs to be individual test cases (and not just subtests) so we can
// start with a fresh auctioneer master account.
func testBatchExecutionV0ToV1(t *harnessTest) {
	ctx := context.Background()
	runBatchExecutionTest(ctx, t, accountT.VersionInitialNoVersion, true)
}

// runBatchExecutionTest ensures that we can renew an account of the given
// version in its confirmed state, and after it has expired.
func runBatchExecutionTest(ctx context.Context, t *harnessTest,
	initialVersion accountT.Version, allowAccountUpgrade bool) {

	var (
		traderOpt         []traderCfgOpt
		versionAfterBatch = initialVersion
	)

	isV0 := initialVersion == accountT.VersionInitialNoVersion

	// We can only upgrade accounts from V0.
	require.False(t.t, allowAccountUpgrade && !isV0)

	if isV0 {
		if allowAccountUpgrade {
			versionAfterBatch = accountT.VersionTaprootEnabled
		} else {
			// Use a batch version previous to
			// UpgradeAccountTaprootBatchVersion.
			traderOpt = append(traderOpt, batchVersionOpt(
				orderT.UnannouncedChannelsBatchVersion,
			))
		}
	}

	// We run the same subtest multiple times, so we don't want to use the
	// same trader. Let's replace the already existing Bob with a custom
	// one.
	// We need a third lnd node, Charlie that is used for the second trader.
	bob := t.lndHarness.NewNode(t.t, "bob", lndDefaultArgs)
	trader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, bob, t.auctioneer,
	)
	defer shutdownAndAssert(t, bob, trader)
	t.lndHarness.SendCoins(t.t, 5_000_000, bob)

	// We need a third lnd node, Charlie that is used for the second trader.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
		traderOpt...,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders. To test the message multi-plexing between token IDs
	// and accounts, we add a secondary account to the second trader.
	account1 := openAccountAndAssert(
		t, trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
			Version: rpcVersion(accountT.VersionTaprootEnabled),
		},
	)
	account2 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
			Version: rpcVersion(initialVersion),
		},
	)
	account3 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
			Version: rpcVersion(initialVersion),
		},
	)

	// We'll add a third trader that we will shut down after placing an
	// order, to ensure batch execution still can proceed.
	dave := t.lndHarness.NewNode(t.t, "dave", lndDefaultArgs)
	thirdTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
	)
	defer shutdownAndAssert(t, dave, thirdTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, dave)

	account4 := openAccountAndAssert(
		t, thirdTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
			Version: rpcVersion(initialVersion),
		},
	)

	// Before we start batch execution, we want to allow the master account
	// to upgrade itself to a Taproot account. We did start out with a v0
	// (p2wsh) master account for this test, so we should be able to upgrade
	// it with a restart.
	assertAuctioneerVersion(t, account.VersionInitialNoVersion)
	assertAuctioneerOutputType(t, account.VersionInitialNoVersion)

	t.auctioneer.serverCfg.DefaultAuctioneerVersion = account.VersionTaprootEnabled
	t.restartServer()
	assertTraderSubscribed(t, trader, account1, 3)
	assertTraderSubscribed(t, secondTrader, account2, 3)
	assertTraderSubscribed(t, secondTrader, account3, 3)

	// Now that the accounts are confirmed, submit an ask order from our
	// default trader, selling 15 units (1.5M sats) of liquidity.
	const orderFixedRate = 100
	askAmt := btcutil.Amount(1_500_000)
	ask1Nonce, err := submitAskOrder(
		trader, account1.TraderKey, orderFixedRate, askAmt,
	)
	require.NoError(t.t, err)
	ask1Created := time.Now()

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity. So let's submit an order for that.
	bidAmt := btcutil.Amount(800_000)
	bid1Nonce, err := submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate, bidAmt,
	)
	require.NoError(t.t, err)
	bid1Created := time.Now()

	// From the secondary account of the second trader, we also create an
	// order to buy some units. The order should also make it into the same
	// batch and the second trader should sign a message for both orders at
	// the same time.
	bidAmt2 := btcutil.Amount(400_000)
	bid2Nonce, err := submitBidOrder(
		secondTrader, account3.TraderKey, orderFixedRate, bidAmt2,
	)
	require.NoError(t.t, err)
	bid2Created := time.Now()

	// Make the third account submit a bid that will get matched, but shut
	// down the trader immediately after.
	_, err = submitBidOrder(
		thirdTrader, account4.TraderKey, orderFixedRate, bidAmt2,
	)
	require.NoError(t.t, err)

	if err := thirdTrader.stop(false); err != nil {
		t.Fatalf("unable to stop trader %v", err)
	}

	// To ensure the venue is aware of account deposits/withdrawals, we'll
	// process a deposit for the account behind the ask.
	depositResp, err := trader.DepositAccount(
		ctx, &poolrpc.DepositAccountRequest{
			TraderKey:       account1.TraderKey,
			AmountSat:       100_000,
			FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
			NewVersion: rpcVersion(
				accountT.VersionTaprootEnabled,
			),
		},
	)
	require.NoError(t.t, err)

	// We should expect to see the transaction causing the deposit.
	depositTxid, _ := chainhash.NewHash(depositResp.Account.Outpoint.Txid)
	txids, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Client, 1, minerMempoolTimeout,
	)
	require.NoError(t.t, err)
	require.Equal(t.t, depositTxid, txids[0])

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

	// There was no successful batch so the auctioneer should not have
	// upgraded its version.
	assertAuctioneerVersion(t, account.VersionInitialNoVersion)

	// Proceed to fully confirm the account deposit.
	_ = mineBlocks(t, t.lndHarness, 5, 0)
	assertAuctioneerAccountState(
		t, depositResp.Account.TraderKey, account.StateOpen,
	)

	// Let's kick the auctioneer once again to try and create a batch.
	_, batchTXIDs := executeBatch(t, 1)
	firstBatchTXID := batchTXIDs[0]

	// The auctioneer should have upgraded to taproot.
	assertAuctioneerVersion(t, account.VersionTaprootEnabled)
	assertAuctioneerOutputType(t, account.VersionTaprootEnabled)

	// At this point, the lnd nodes backed by each trader should have a
	// single pending channel, which matches the amount of the order
	// executed above.
	//
	// In our case, Bob is the maker so he should be marked as the
	// initiator of the channel.
	assertPendingChannel(
		t, trader.cfg.LndNode, bidAmt, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt, false, trader.cfg.LndNode.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt2, false, trader.cfg.LndNode.PubKey,
	)

	// We'll also make sure that the account now is in the special state
	// where it is allowed to participate in the next batch without on-chain
	// confirmation.
	assertAuctioneerAccountState(
		t, account1.TraderKey, account.StatePendingBatch,
	)

	// We now check that all accounts have the correct version we expect.
	// This is still the same version as in the beginning, except for the
	// accounts of the second trader (Charlie), which we allowed to upgrade
	// the account during batches.
	assertTraderAccountState(
		t.t, trader, account1.TraderKey,
		poolrpc.AccountState_PENDING_BATCH,
		versionCheck(accountT.VersionTaprootEnabled),
	)
	assertTraderAccountState(
		t.t, secondTrader, account2.TraderKey,
		poolrpc.AccountState_PENDING_BATCH,
		versionCheck(versionAfterBatch),
	)
	assertTraderAccountState(
		t.t, secondTrader, account3.TraderKey,
		poolrpc.AccountState_PENDING_BATCH,
		versionCheck(versionAfterBatch),
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
	require.NoError(t.t, err)
	acctOutPoint := masterAcct.Outpoint
	require.Equal(t.t, firstBatchTXID[:], acctOutPoint.Txid)

	// We'll now mine another 3 blocks to ensure the channel itself is
	// fully confirmed.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// Now that the channels are confirmed, they should both be active, and
	// we should be able to make a payment between this new channel
	// established.
	assertActiveChannel(
		t, trader.cfg.LndNode, int64(bidAmt), *firstBatchTXID,
		charlie.PubKey, defaultOrderDuration,
	)
	assertActiveChannel(
		t, trader.cfg.LndNode, int64(bidAmt2), *firstBatchTXID,
		charlie.PubKey, defaultOrderDuration,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt), *firstBatchTXID,
		trader.cfg.LndNode.PubKey, defaultOrderDuration,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt2), *firstBatchTXID,
		trader.cfg.LndNode.PubKey, defaultOrderDuration,
	)

	// All executed orders should now have several events recorded. The ask
	// was matched twice so it should have two sets of prepare->accepted->
	// signed->finalized events plus one DB update.
	assertOrderEvents(t, trader, ask1Nonce[:], ask1Created, 1, 8)
	assertOrderEvents(t, secondTrader, bid1Nonce[:], bid1Created, 1, 4)
	assertOrderEvents(t, secondTrader, bid2Nonce[:], bid2Created, 1, 4)

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
	require.NoError(t.t, err)

	ctxt, cancel = context.WithTimeout(ctxb, defaultWaitTimeout)
	defer cancel()
	err = completePaymentRequests(
		ctxt, trader.cfg.LndNode, []string{resp.PaymentRequest}, true,
	)
	require.NoError(t.t, err)

	// Now that the batch has been fully executed, we'll ensure that all
	// the expected state has been updated from the client's PoV.
	//
	// Charlie, the trader that just bought a channel should have no
	// present orders.
	assertNoOrders(t, secondTrader)

	// The party with the sell orders open should still have a single open
	// ask order with 300k unfilled (3 units).
	assertAskOrderState(t, trader, 3, ask1Nonce)

	// We should now be able to find this snapshot. As this is the only
	// batch created atm, we pass a nil batch ID so it'll look up the prior
	// batch.
	firstBatchID := assertBatchSnapshot(
		t, nil, secondTrader, map[uint32][]uint64{
			defaultOrderDuration: {uint64(bidAmt), uint64(bidAmt2)},
		}, map[uint32]orderT.FixedRatePremium{
			defaultOrderDuration: orderFixedRate,
		},
	)
	assertTraderAssets(t, trader, 2, []*chainhash.Hash{firstBatchTXID})
	assertTraderAssets(t, secondTrader, 2, []*chainhash.Hash{firstBatchTXID})

	// We'll now do an additional round to ensure that we're able to
	// fulfill back to back batches. In this round, Charlie will submit
	// another order for 3 units, which should be matched with Bob's
	// remaining Ask order that should now have zero units remaining.
	bidAmt3 := btcutil.Amount(300_000)
	_, err = submitBidOrder(secondTrader, account2.TraderKey, 100, bidAmt3)
	require.NoError(t.t, err)

	// We'll now tick off another batch, which should trigger a clearing of
	// the market, to produce another channel which Charlie has just
	// purchased. Let's kick the auctioneer now to try and create a batch.
	_, batchTXIDs = executeBatch(t, 1)
	secondBatchTXID := batchTXIDs[0]

	// Both parties should once again have a pending channel.
	assertPendingChannel(
		t, trader.cfg.LndNode, bidAmt3, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt3, false, trader.cfg.LndNode.PubKey,
	)
	blocks = mineBlocks(t, t.lndHarness, 1, 1)
	assertTxInBlock(t, blocks[0], secondBatchTXID)

	// We'll conclude by mining enough blocks to have the channels be
	// confirmed.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// At this point, both traders should have no outstanding orders as
	// they've all be bundled up into a batch.
	assertNoOrders(t, secondTrader)
	assertNoOrders(t, trader)

	// We should also be able to find this batch as well, with only 2
	// orders being included in the batch. This time, we'll query with the
	// exact batch ID we expect.
	firstBatchKey, err := btcec.ParsePubKey(firstBatchID[:])
	require.NoError(t.t, err)
	secondBatchKey := poolscript.IncrementKey(firstBatchKey)
	secondBatchID := secondBatchKey.SerializeCompressed()
	assertBatchSnapshot(
		t, secondBatchID, secondTrader, map[uint32][]uint64{
			defaultOrderDuration: {uint64(bidAmt3)},
		}, map[uint32]orderT.FixedRatePremium{
			defaultOrderDuration: orderFixedRate,
		},
	)
	assertNumFinalBatches(t, 2)
	batchTXIDs = []*chainhash.Hash{firstBatchTXID, secondBatchTXID}
	assertTraderAssets(t, trader, 3, batchTXIDs)
	assertTraderAssets(t, secondTrader, 3, batchTXIDs)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
}

// closeAllChannels closes and asserts all channels to node are closed.
func closeAllChannels(ctx context.Context, t *harnessTest,
	node *lntest.HarnessNode) {

	chanReq := &lnrpc.ListChannelsRequest{}
	openChans, err := node.ListChannels(context.Background(), chanReq)
	require.NoError(t.t, err)
	for _, openChan := range openChans.Channels {
		chanPointStr := openChan.ChannelPoint
		chanPointParts := strings.Split(chanPointStr, ":")
		txid, err := chainhash.NewHashFromStr(chanPointParts[0])
		require.NoError(t.t, err)
		index, err := strconv.Atoi(chanPointParts[1])
		require.NoError(t.t, err)

		chanPoint := &lnrpc.ChannelPoint{
			FundingTxid: &lnrpc.ChannelPoint_FundingTxidBytes{
				FundingTxidBytes: txid[:],
			},
			OutputIndex: uint32(index),
		}
		closeUpdates, _, err := t.lndHarness.CloseChannel(
			node, chanPoint, false,
		)
		require.NoError(t.t, err)

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
	// trader.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)

	// We fund both traders so they have enough funds to open multiple
	// accounts.
	walletAmt := btcutil.Amount(
		(unconfirmedBatches + 1) * defaultAccountValue,
	)
	t.lndHarness.SendCoins(t.t, walletAmt, charlie)
	t.lndHarness.SendCoins(t.t, walletAmt, t.trader.cfg.LndNode)

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
		)
		if err != nil {
			t.Fatalf("could not submit ask order: %v", err)
		}

		_, err = submitBidOrder(
			secondTrader, account2.TraderKey, orderFixedRate, chanAmt,
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
			t.lndHarness.Miner.Client, i+1, minerMempoolTimeout,
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
			charlie.PubKey, defaultOrderDuration,
		)

		assertActiveChannel(
			t, charlie, int64(chanAmt), *batchTXID,
			t.trader.cfg.LndNode.PubKey, defaultOrderDuration,
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
// RPC stuff is in.
func assertBatchSnapshot(t *harnessTest, batchID []byte, trader *traderHarness,
	expectedOrderAmts map[uint32][]uint64,
	clearingPrices map[uint32]orderT.FixedRatePremium) orderT.BatchID {

	ctxb := context.Background()
	batchSnapshot, err := trader.BatchSnapshot(
		ctxb, &auctioneerrpc.BatchSnapshotRequest{
			BatchId: batchID,
		},
	)
	require.NoError(t.t, err)

	// The creation timestamp should be within one minute of the current
	// time.
	require.InDelta(
		t.t, uint64(time.Now().UnixNano()),
		batchSnapshot.CreationTimestampNs, float64(time.Minute),
	)

	// Verify there is a distinct clearing price and orders for each sub
	// batch.
	for duration, market := range batchSnapshot.MatchedMarkets {
		expectedAmts := expectedOrderAmts[duration]
		expectedPrice := uint32(clearingPrices[duration])

		require.Equal(t.t, expectedPrice, market.ClearingPriceRate)

		// Next we'll compile a map of the included ask and bid orders so we
		// can assert the existence of the orders we created above.
		matchedOrderAmts := make(map[uint64]struct{})
		for _, o := range market.MatchedOrders {
			matchedOrderAmts[o.TotalSatsCleared] = struct{}{}
		}

		// Next we'll assert that all the expected orders have been found in
		// this batch.
		for _, orderAmt := range expectedAmts {
			require.Contains(t.t, matchedOrderAmts, orderAmt)
		}
	}

	var serverbatchID orderT.BatchID
	copy(serverbatchID[:], batchSnapshot.BatchId)

	return serverbatchID
}

// assertNumFinalBatches makes sure the auctioneer has the given number of
// finalized batches in its database.
func assertNumFinalBatches(t *harnessTest, numTotalBatches int) {
	resp, err := t.trader.BatchSnapshots(
		context.Background(), &auctioneerrpc.BatchSnapshotsRequest{
			NumBatchesBack: 100,
		},
	)
	require.NoError(t.t, err)
	require.Len(t.t, resp.Batches, numTotalBatches)
}

type leaseCheck func(leases []*poolrpc.Lease) error

// assertTraderAssets ensures that the given trader has the expected number of
// assets with any of the given transaction hashes.
func assertTraderAssets(t *harnessTest, trader *traderHarness, numAssets int,
	txids []*chainhash.Hash, additionalChecks ...leaseCheck) {

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

	for _, check := range additionalChecks {
		require.NoError(t.t, check(resp.Leases))
	}
}

// testServiceLevelEnforcement ensures that the auctioneer correctly enforces
// a channel's lifetime by punishing the offending trader (the maker).
func testServiceLevelEnforcement(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

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
	_, err := submitAskOrder(t.trader, account1.TraderKey, 100, askAmt)
	if err != nil {
		t.Fatalf("could not submit ask order: %v", err)
	}

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity. So let's submit an order for that.
	bidAmt := btcutil.Amount(800_000)
	_, err = submitBidOrder(secondTrader, account2.TraderKey, 100, bidAmt)
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
		charlie.PubKey, defaultOrderDuration,
	)
	_ = assertActiveChannel(
		t, charlie, int64(bidAmt), *batchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration,
	)

	// Proceed to force close the channel from the initiator. They should be
	// banned accordingly.
	closeUpdates, _, err := t.lndHarness.CloseChannel(
		t.trader.cfg.LndNode, chanPoint, true,
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
	_, err = submitAskOrder(t.trader, account1.TraderKey, 100, 100_000)
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
	_, err = submitAskOrder(secondTrader, account2.TraderKey, 100, 100_000)
	if err != nil {
		t.Fatalf("expected order submission to succeed: %v", err)
	}
}

// testScriptLevelEnforcement ensures that the channel output scripts and the
// auctioneer correctly enforce a channel's lifetime by punishing the offending
// trader (the maker).
func testScriptLevelEnforcement(t *harnessTest) {
	ctx := context.Background()

	extraArgs := append(
		[]string{"--protocol.script-enforced-lease"}, lndDefaultArgs...,
	)

	charlie := t.lndHarness.NewNode(t.t, "charlie", extraArgs)
	charlieTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, charlieTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	dave := t.lndHarness.NewNode(t.t, "dave", extraArgs)
	daveTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
	)
	defer shutdownAndAssert(t, dave, daveTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, dave)

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders.
	charlieAccount := openAccountAndAssert(
		t, charlieTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	daveAccount := openAccountAndAssert(
		t, daveTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// Now that the accounts are confirmed, submit an ask order from our
	// default trader, selling 15 units (1.5M sats) of liquidity.
	const scriptEnforcedChannelType = auctioneerrpc.OrderChannelType_ORDER_CHANNEL_TYPE_SCRIPT_ENFORCED
	askAmt := btcutil.Amount(1_500_000)
	_, err := submitAskOrder(
		charlieTrader, charlieAccount.TraderKey, 100, askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.Details.ChannelType = scriptEnforcedChannelType
		},
	)
	require.NoError(t.t, err)

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity. So let's submit an order for that.
	bidAmt := btcutil.Amount(800_000)
	_, err = submitBidOrder(
		daveTrader, daveAccount.TraderKey, 100, bidAmt,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.Details.ChannelType = scriptEnforcedChannelType
		},
	)
	require.NoError(t.t, err)

	// Let's kick the auctioneer now to try and create a batch.
	_, batchTXIDs := executeBatch(t, 1)
	batchTXID := batchTXIDs[0]

	// At this point, the lnd nodes backed by each trader should have a
	// single pending channel, which matches the amount of the order
	// executed above.
	//
	// In our case, Bob is the maker so he should be marked as the
	// initiator of the channel.
	assertPendingChannel(t, charlie, bidAmt, true, dave.PubKey)
	assertPendingChannel(t, dave, bidAmt, false, charlie.PubKey)

	_, heightBeforeBatch, err := t.lndHarness.Miner.Client.GetBestBlock()
	require.NoError(t.t, err)
	leaseExpiry := uint32(heightBeforeBatch) + defaultOrderDuration

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
		t, charlie, int64(bidAmt), *batchTXID, dave.PubKey, leaseExpiry,
	)
	_ = assertActiveChannel(
		t, dave, int64(bidAmt), *batchTXID, charlie.PubKey, leaseExpiry,
	)

	// Proceed to force close the channel from the initiator. They should be
	// banned accordingly.
	_, _, err = t.lndHarness.CloseChannel(charlie, chanPoint, true)
	require.NoError(t.t, err)

	_ = mineBlocks(t, t.lndHarness, 1, 1)

	err = wait.NoError(func() error {
		ctxt, cancel := context.WithTimeout(ctx, defaultWaitTimeout)
		defer cancel()
		resp, err := charlie.PendingChannels(
			ctxt, &lnrpc.PendingChannelsRequest{},
		)
		if err != nil {
			return err
		}

		if len(resp.PendingForceClosingChannels) != 1 {
			return fmt.Errorf("expected %d pending force closing "+
				"channel, got %d", 1,
				len(resp.PendingForceClosingChannels))
		}

		channel := resp.PendingForceClosingChannels[0]
		if channel.MaturityHeight != leaseExpiry {
			return fmt.Errorf("expected maturity height %d, got %d",
				leaseExpiry, channel.MaturityHeight)
		}

		return nil
	}, defaultWaitTimeout)
	require.NoError(t.t, err)

	// The trader responsible should no longer be able to modify their
	// account or submit orders.
	_, err = submitAskOrder(charlieTrader, charlieAccount.TraderKey, 100, 100_000)
	if err == nil || !strings.Contains(err.Error(), "banned") {
		t.Fatalf("expected order submission to fail due to account ban")
	}
	_, err = charlieTrader.DepositAccount(ctx, &poolrpc.DepositAccountRequest{
		TraderKey:       charlieAccount.TraderKey,
		AmountSat:       1000,
		FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
	})
	if err == nil || !strings.Contains(err.Error(), "banned") {
		t.Fatalf("expected account deposit to fail due to account ban")
	}

	// The offended trader should still be able to however.
	_, err = submitAskOrder(daveTrader, daveAccount.TraderKey, 100, 100_000)
	require.NoError(t.t, err)
}

// testBatchExecutionDustOutputs checks that an account is considered closed if
// the remaining balance is below the dust limit.
func testBatchExecutionDustOutputs(t *harnessTest) {
	ctx := context.Background()
	ctxt, cancel := context.WithTimeout(ctx, defaultWaitTimeout)
	defer cancel()

	t.t.Run("version 0", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runBatchExecutionDustOutputsTest(
			ctxt, ht, accountT.VersionInitialNoVersion,
		)
	})
	t.t.Run("version 1", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runBatchExecutionDustOutputsTest(
			ctxt, ht, accountT.VersionTaprootEnabled,
		)
	})
}

// runBatchExecutionDustOutputsTest checks that an account is considered closed
// if the remaining balance is below the dust limit for an account of the given
// version.
func runBatchExecutionDustOutputsTest(ctx context.Context, t *harnessTest,
	version accountT.Version) {

	// We need a third lnd node, Charlie that is used for the second trader.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	// Create an account for the maker with plenty of sats.
	account1 := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
			Version: rpcVersion(version),
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
			Version: rpcVersion(version),
		},
	)

	// We'll create an order that matches with a ridiculously high
	// premium, in order to make what's left on the bidders account into
	// dust.
	//
	// 453_500 per billion of 100_000 sats for 2016 blocks is 91_425 sats,
	// so the trader will only have 8575 sats left to pay for chain fees
	// and execution fees.
	//
	// The execution fee is 101 sats, chain fee 8162 sats (at the static
	// fee rate of 12500 s/kw), so what is left will be dust (< 678 sats).
	const orderSize = 100_000
	matchRate := uint32(453_500)
	const durationBlocks = 2016

	// Taproot accounts use 41 fewer vBytes on chain. With the fee rate of
	// 12500 s/kw, which is 50 sat/vByte, we pay 2025 sats less. So we need
	// to bump the match rate to 453_500 which is 93_744 sats.
	if version == accountT.VersionTaprootEnabled {
		matchRate = 464_000
	}

	// Submit an ask and bid which will match exactly.
	_, err := submitAskOrder(
		t.trader, account1.TraderKey, matchRate, orderSize,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.LeaseDurationBlocks = durationBlocks
		},
	)
	require.NoError(t.t, err)

	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, matchRate, orderSize,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = durationBlocks
		},
	)
	require.NoError(t.t, err)

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
	require.Len(t.t, batchTx.TxIn, 3)

	// There should be 3 outputs: master account, the new channel, and the
	// first trader. The second trader's output is dust and does not
	// materialize.
	require.Len(t.t, batchTx.TxOut, 3)

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
	// We need a third lnd node, Charlie that is used for the second trader.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	// We start with the default version for all accounts and then attempt
	// to bump it during the batches.
	version := accountT.VersionInitialNoVersion

	// Create an account for the maker with plenty of sats.
	askAccount := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
			Version: rpcVersion(version),
		},
	)

	// We'll also create accounts for the bidder as well.
	bidAccount := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
			Version: rpcVersion(version),
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

	// Submit an ask an bid that matches one third of the ask.
	_, err := submitAskOrder(
		t.trader, askAccount.TraderKey, askRate, askSize,
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, bidAccount.TraderKey, askRate, bid1Size,
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
		validTestAddr, version,
	)
	_, err = submitBidOrder(
		secondTrader, bidAccount.TraderKey, askRate, bid3Size,
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
	// We need a third and fourth lnd node, Charlie and Dave that are used
	// for the second and third trader. Charlie is very picky and only wants
	// channels from new nodes.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
		newNodesOnlyOpt(),
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	dave := t.lndHarness.NewNode(t.t, "dave", lndDefaultArgs)
	thirdTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
	)
	defer shutdownAndAssert(t, dave, thirdTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, dave)

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
	const durationBlocks = 2016

	// Submit an ask an bid that matches half of the ask.
	_, err := submitAskOrder(
		t.trader, askAccount.TraderKey, askRate, askSize,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.LeaseDurationBlocks = durationBlocks
		},
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, bidAccountCharlie.TraderKey, askRate, askSize/2,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = durationBlocks
		},
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
		askSize/2, func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = durationBlocks
		},
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		thirdTrader, bidAccountDave.TraderKey, askRate,
		askSize/2, func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = durationBlocks
		},
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
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	// And as an "innocent bystander" we also create Dave who will buy a
	// channel during the second round so we can make sure being matched
	// multiple times in the same batch (because another node rejected and
	// the matchmaking had to be restarted) can still succeed.
	dave := t.lndHarness.NewNode(t.t, "dave", lndDefaultArgs)
	thirdTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
	)
	defer shutdownAndAssert(t, dave, thirdTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, dave)

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
	const durationBlocks = 2016

	// Submit an ask order that is large enough to be matched multiple
	// times.
	ask1Nonce, err := submitAskOrder(
		t.trader, askAccount.TraderKey, askRate, askSize,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.LeaseDurationBlocks = durationBlocks
		},
	)
	require.NoError(t.t, err)
	ask1Created := time.Now()

	// We manually open a channel between Bob and Charlie now to saturate
	// the maxpendingchannels setting.
	t.lndHarness.EnsureConnected(t.t, t.trader.cfg.LndNode, charlie)
	_, err = t.lndHarness.OpenPendingChannel(
		t.trader.cfg.LndNode, charlie, 1_000_000, 0,
	)
	require.NoError(t.t, err)

	// Let's now create a bid that will be matched against the ask but
	// should result in Charlie rejecting it because of too many pending
	// channels. That is, the asker, Bob, will open the channel and Charlie
	// will reject the channel funding. That leads to both trader daemons
	// rejecting the batch because of a failed funding attempt.
	bid1Nonce, err := submitBidOrder(
		secondTrader, bidAccountCharlie.TraderKey, askRate, bidSize1,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = durationBlocks
		},
	)
	require.NoError(t.t, err)
	bid1Created := time.Now()

	// Dave also participates. He should be matched twice with a pending
	// channel already initiated during the first try. We need to make sure
	// the same matched order can still result in a channel the second time
	// around.
	bid2Nonce, err := submitBidOrder(
		thirdTrader, bidAccountDave.TraderKey, askRate, bidSize2,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = durationBlocks
		},
	)
	require.NoError(t.t, err)
	bid2Created := time.Now()

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

	// All executed orders should now have several events recorded. The ask
	// should have 2 prepare, 2 accept, 2 rejected events, then prepare,
	// update, accept, signed and finalized events for only one order,
	// giving a total of 10. Charlie just prepared, accepted then rejected.
	// Dave prepared, accepted, updated and signed, then had to start over,
	// going through prepare, accept, update, sign and finalize again.
	assertOrderEvents(
		t, t.trader, ask1Nonce[:], ask1Created, 1, 10,
		poolrpc.MatchRejectReason_PARTIAL_REJECT_COLLATERAL,
		poolrpc.MatchRejectReason_PARTIAL_REJECT_CHANNEL_FUNDING_FAILED,
	)
	assertOrderEvents(
		t, secondTrader, bid1Nonce[:], bid1Created, 0, 3,
		poolrpc.MatchRejectReason_PARTIAL_REJECT_CHANNEL_FUNDING_FAILED,
	)
	assertOrderEvents(t, thirdTrader, bid2Nonce[:], bid2Created, 2, 7)

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

// testBatchMatchingConditions tests that all order and account preconditions
// for being matched are applied correctly.
func testBatchMatchingConditions(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

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
	const orderFixedRate = 100
	askAmt := btcutil.Amount(1_500_000)
	ask1Nonce, err := submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate, askAmt,
	)
	require.NoError(t.t, err)

	// Our second trader, connected to Charlie, wants to buy 4 units of
	// liquidity. So let's submit an order for that.
	bidAmt := btcutil.Amount(400_000)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate, bidAmt,
	)
	require.NoError(t.t, err)

	// To ensure the venue is aware of account deposits/withdrawals, we'll
	// process a deposit for the account behind the ask.
	depositResp, err := t.trader.DepositAccount(
		ctx, &poolrpc.DepositAccountRequest{
			TraderKey:       account1.TraderKey,
			AmountSat:       100_000,
			FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
		},
	)
	require.NoError(t.t, err)

	// We should expect to see the transaction causing the deposit.
	depositTxid, _ := chainhash.NewHash(depositResp.Account.Outpoint.Txid)
	txids, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Client, 1, minerMempoolTimeout,
	)
	require.NoError(t.t, err)
	require.Equal(t.t, depositTxid, txids[0])

	// Let's go ahead and confirm it. The account should remain in
	// PendingUpdate as it hasn't met all of the required confirmations.
	block := mineBlocks(t, t.lndHarness, 1, 1)[0]
	_ = assertTxInBlock(t, block, depositTxid)
	assertAuctioneerAccountState(
		t, account1.TraderKey, account.StatePendingUpdate,
	)

	// Since the ask account is pending an update, a batch should not be
	// cleared. Let's make sure the reason for not clearing was what we
	// expected.
	_, _ = executeBatch(t, 0)
	assertServerLogContains(
		t, "Filtered out order %v with account not ready, state=%v",
		ask1Nonce, account.StatePendingUpdate,
	)

	// Let's now fully confirm the account again, but then ban the trader.
	_ = mineBlocks(t, t.lndHarness, 2, 0)
	assertAuctioneerAccountState(t, account1.TraderKey, account.StateOpen)
	_, err = t.auctioneer.AddBan(ctx, &adminrpc.BanRequest{
		Duration: 3,
		Ban: &adminrpc.BanRequest_Account{
			Account: account1.TraderKey,
		},
	})
	require.NoError(t.t, err)

	// The batch should now fail because of the banned account.
	_, _ = executeBatch(t, 0)
	assertServerLogContains(
		t, "Filtered out order %v with banned trader (node=%x, acct=%x",
		ask1Nonce, t.trader.cfg.LndNode.PubKey, account1.TraderKey,
	)

	// Let's unban the trader now and make sure the orders go through.
	_, err = t.auctioneer.RemoveBan(ctx, &adminrpc.RemoveBanRequest{
		Ban: &adminrpc.RemoveBanRequest_Account{
			Account: account1.TraderKey,
		},
	})
	require.NoError(t.t, err)
	_, _ = executeBatch(t, 1)
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bidAmt, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt, false, t.trader.cfg.LndNode.PubKey,
	)
	mineBlocks(t, t.lndHarness, 1, 1)

	// Let's add a second bid order, this time with a max fee rate that is
	// too low.
	bid2Nonce, err := submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate, bidAmt,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.Details.MaxBatchFeeRateSatPerKw = 255
		},
	)
	require.NoError(t.t, err)

	// The batch should now fail because the bid is filtered out based on
	// its max fee rate.
	_, _ = executeBatch(t, 0)
	assertServerLogContains(
		t, "Filtered out order %v with max fee rate %v", bid2Nonce, 255,
	)
}

// testBatchExecutionDurationBuckets tests that we can clear multiple markets
// with distinct lease durations in the same batch.
func testBatchExecutionDurationBuckets(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 40_000_000, charlie)

	// We'll create orders in three distinct lease duration buckets: The
	// default 2016 block bucket that is already available on startup and
	// the multiples 4032 and 6048 that we explicitly add now.
	_, err := t.auctioneer.StoreLeaseDuration(ctx, &adminrpc.LeaseDuration{
		Duration:    4032,
		BucketState: auctioneerrpc.DurationBucketState_MARKET_OPEN,
	})
	require.NoError(t.t, err)
	_, err = t.auctioneer.StoreLeaseDuration(ctx, &adminrpc.LeaseDuration{
		Duration:    6048,
		BucketState: auctioneerrpc.DurationBucketState_MARKET_OPEN,
	})
	require.NoError(t.t, err)

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders. To test the message multi-plexing between token IDs
	// and accounts, we add a secondary account to the second trader.
	account1 := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue * 8,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	account2 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue * 8,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	account3 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue * 8,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// Now that the accounts are confirmed, we can start submitting orders.
	// We first make sure that we cannot submit an order outside of the
	// legacy duration bucket if we're not on the correct order version.
	const orderFixedRate = 100
	askAmt := btcutil.Amount(1_500_000)
	_, err = submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate, askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.LeaseDurationBlocks = defaultOrderDuration * 2
			ask.Ask.Version = uint32(orderT.VersionNodeTierMinMatch)
		},
	)
	require.Error(t.t, err)
	require.Contains(t.t, err.Error(), "outside of default 2016 duration")

	// Now that the accounts are confirmed, submit ask orders from our
	// default trader, selling 15 units (1.5M sats) of liquidity in each
	// duration bucket.
	ask1aNonce, err := submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate, askAmt,
	)
	require.NoError(t.t, err)
	ask1bNonce, err := submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate*2, askAmt*2,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.LeaseDurationBlocks = defaultOrderDuration * 2
		},
	)
	require.NoError(t.t, err)
	ask1cNonce, err := submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate*3, askAmt*3,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.LeaseDurationBlocks = defaultOrderDuration * 3
		},
	)
	require.NoError(t.t, err)

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity for each duration. So let's submit orders for that.
	bidAmt := btcutil.Amount(800_000)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate, bidAmt,
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate*2, bidAmt*2,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = defaultOrderDuration * 2
		},
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate*3, bidAmt*3,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = defaultOrderDuration * 3
		},
	)
	require.NoError(t.t, err)

	// From the secondary account of the second trader, we also create an
	// order to buy some units. The order should also make it into the same
	// batch and the second trader should sign a message for both orders at
	// the same time.
	bidAmt2 := btcutil.Amount(300_000)
	_, err = submitBidOrder(
		secondTrader, account3.TraderKey, orderFixedRate, bidAmt2,
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, account3.TraderKey, orderFixedRate*2, bidAmt2*2,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = defaultOrderDuration * 2
		},
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, account3.TraderKey, orderFixedRate*3, bidAmt2*3,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = defaultOrderDuration * 3
		},
	)
	require.NoError(t.t, err)

	// Let's kick the auctioneer to try and make a batch with three sub
	// batches for the different durations.
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
		t, t.trader.cfg.LndNode, bidAmt*2, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bidAmt*3, true, charlie.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt, false, t.trader.cfg.LndNode.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt*2, false, t.trader.cfg.LndNode.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt*3, false, t.trader.cfg.LndNode.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt2, false, t.trader.cfg.LndNode.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt2*2, false, t.trader.cfg.LndNode.PubKey,
	)
	assertPendingChannel(
		t, charlie, bidAmt2*3, false, t.trader.cfg.LndNode.PubKey,
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
	require.NoError(t.t, err)
	acctOutPoint := masterAcct.Outpoint
	require.Equal(t.t, firstBatchTXID[:], acctOutPoint.Txid)

	// We'll now mine another 3 blocks to ensure the channel itself is
	// fully confirmed.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// Now that the channels are confirmed, they should both be active, and
	// we should be able to make a payment between this new channel
	// established.
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt), *firstBatchTXID,
		charlie.PubKey, defaultOrderDuration,
	)
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt*2), *firstBatchTXID,
		charlie.PubKey, defaultOrderDuration*2,
	)
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt*3), *firstBatchTXID,
		charlie.PubKey, defaultOrderDuration*3,
	)
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt2), *firstBatchTXID,
		charlie.PubKey, defaultOrderDuration,
	)
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt2*2), *firstBatchTXID,
		charlie.PubKey, defaultOrderDuration*2,
	)
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt2*3), *firstBatchTXID,
		charlie.PubKey, defaultOrderDuration*3,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt*2), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration*2,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt*3), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration*3,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt2*2), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration*2,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt2*3), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration*3,
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
	require.NoError(t.t, err)

	ctxt, cancel = context.WithTimeout(ctxb, defaultWaitTimeout)
	defer cancel()
	err = completePaymentRequests(
		ctxt, t.trader.cfg.LndNode, []string{resp.PaymentRequest}, true,
	)
	require.NoError(t.t, err)

	// Now that the batch has been fully executed, we'll ensure that all
	// the expected state has been updated from the client's PoV.
	//
	// Charlie, the trader that just bought a channel should have no
	// present orders.
	assertNoOrders(t, secondTrader)

	// The party with the sell orders open should still have all open ask
	// orders with 300k unfilled (3 units).
	assertAskOrderState(t, t.trader, 4, ask1aNonce)
	assertAskOrderState(t, t.trader, 8, ask1bNonce)
	assertAskOrderState(t, t.trader, 12, ask1cNonce)

	// We should now be able to find this snapshot. As this is the only
	// batch created atm, we pass a nil batch ID so it'll look up the prior
	// batch.
	_ = assertBatchSnapshot(
		t, nil, secondTrader, map[uint32][]uint64{
			defaultOrderDuration: {
				uint64(bidAmt), uint64(bidAmt2),
			},
			defaultOrderDuration * 2: {
				uint64(bidAmt * 2), uint64(bidAmt2 * 2),
			},
			defaultOrderDuration * 3: {
				uint64(bidAmt * 3), uint64(bidAmt2 * 3),
			},
		}, map[uint32]orderT.FixedRatePremium{
			defaultOrderDuration:     orderFixedRate,
			defaultOrderDuration * 2: orderFixedRate * 2,
			defaultOrderDuration * 3: orderFixedRate * 3,
		},
	)
	assertTraderAssets(t, t.trader, 6, []*chainhash.Hash{firstBatchTXID})
	assertTraderAssets(t, secondTrader, 6, []*chainhash.Hash{firstBatchTXID})

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
}

// testCustomExecutionFee tests that a trader can be given a custom execution
// fee and that doesn't interfere with what other traders calculate.
func testCustomExecutionFee(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)

	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)

	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	// To execute a batch, we'll need an asker and bidder.
	askAccount := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	bidAccount := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	const orderAmt = 500_000
	const orderRate = 20
	_, err := submitAskOrder(
		t.trader, askAccount.TraderKey, orderRate, orderAmt,
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, bidAccount.TraderKey, orderRate, orderAmt,
	)
	require.NoError(t.t, err)

	// Before we execute the batch, we give the bidder a special discount
	// based upon their LSAT token ID.
	bidderTokenID, err := secondTrader.server.GetIdentity()
	require.NoError(t.t, err)
	_, err = t.auctioneer.StoreTraderTerms(ctx, &adminrpc.TraderTerms{
		LsatId:  bidderTokenID[:],
		BaseFee: 42,
		FeeRate: 0,
	})
	require.NoError(t.t, err)

	// Execute the batch and make sure there's a channel between Bob and
	// Charlie now.
	_, chainTxns := executeBatch(t, 1)
	assertPendingChannel(
		t, t.trader.cfg.LndNode, orderAmt, true, charlie.PubKey,
	)
	assertTraderAccountState(
		t.t, secondTrader, bidAccount.TraderKey,
		poolrpc.AccountState_PENDING_BATCH,
	)
	assertAuctioneerAccountState(
		t, bidAccount.TraderKey, auctioneerAccount.StatePendingBatch,
	)
	_ = mineBlocks(t, t.lndHarness, 6, 1)

	// Make sure the bidder only paid the 42 sats of base fee.
	assertTraderAssets(
		t, secondTrader, 1, chainTxns,
		func(leases []*poolrpc.Lease) error {
			if len(leases) > 1 {
				return fmt.Errorf("invalid number of leases")
			}

			if leases[0].ExecutionFeeSat != 42 {
				return fmt.Errorf("invalid execution fee "+
					"of %d sats paid, expected %d",
					leases[0].ExecutionFeeSat, 42)
			}

			return nil
		},
	)

	// The asker should pay the default 1+1ppm fee.
	assertTraderAssets(
		t, t.trader, 1, chainTxns,
		func(leases []*poolrpc.Lease) error {
			if len(leases) > 1 {
				return fmt.Errorf("invalid number of leases")
			}

			if leases[0].ExecutionFeeSat != 501 {
				return fmt.Errorf("invalid execution fee "+
					"of %d sats paid, expected %d",
					leases[0].ExecutionFeeSat, 501)
			}

			return nil
		},
	)
}

// testBatchAccountAutoRenewal tests that accounts that participate in a batch
// are auto-renewed if the trader supports auto-renewal.
func testBatchAccountAutoRenewal(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader.
	// We'll use a batch version that doesn't support account renewal.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
		batchVersionOpt(orderT.DefaultBatchVersion),
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	_, startHeight, err := t.lndHarness.Miner.Client.GetBestBlock()
	require.NoError(t.t, err)

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders. To test the message multi-plexing between token IDs
	// and accounts, we add a secondary account to the second trader.
	account1 := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_AbsoluteHeight{
				AbsoluteHeight: uint32(startHeight) + 1_000,
			},
		},
	)
	account2 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_AbsoluteHeight{
				AbsoluteHeight: uint32(startHeight) + 1_000,
			},
		},
	)
	account3 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_AbsoluteHeight{
				AbsoluteHeight: uint32(startHeight) + 1_000,
			},
		},
	)

	// Now that the accounts are confirmed, submit an ask order from our
	// default trader, selling 15 units (1.5M sats) of liquidity.
	const orderFixedRate = 100
	askAmt := btcutil.Amount(1_500_000)
	_, err = submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate, askAmt,
	)
	require.NoError(t.t, err)

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity. So let's submit an order for that.
	bidAmt := btcutil.Amount(800_000)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate, bidAmt,
	)
	require.NoError(t.t, err)

	// From the secondary account of the second trader, we also create an
	// order to buy some units. The order should also make it into the same
	// batch and the second trader should sign a message for both orders at
	// the same time.
	bidAmt2 := btcutil.Amount(400_000)
	_, err = submitBidOrder(
		secondTrader, account3.TraderKey, orderFixedRate, bidAmt2,
	)
	require.NoError(t.t, err)

	// To ensure the venue is aware of account deposits/withdrawals, we'll
	// process a deposit for the account behind the ask. We also use this
	// to renew the account by a day.
	depositResp, err := t.trader.DepositAccount(
		ctx, &poolrpc.DepositAccountRequest{
			TraderKey:       account1.TraderKey,
			AmountSat:       100_000,
			FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
			AccountExpiry: &poolrpc.DepositAccountRequest_RelativeExpiry{
				RelativeExpiry: 144,
			},
		},
	)
	require.NoError(t.t, err)

	// We should expect to see the transaction causing the deposit.
	depositTxid, _ := chainhash.NewHash(depositResp.Account.Outpoint.Txid)
	txids, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Client, 1, minerMempoolTimeout,
	)
	require.NoError(t.t, err)
	require.Equal(t.t, depositTxid, txids[0])

	// Let's go ahead and confirm it. The account should remain in
	// PendingUpdate as it hasn't met all the required confirmations.
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

	// We also make sure that the first trader's account was auto-renewed
	// while the second trader's account(s) wasn't, since the client doesn't
	// support auto-renewal. We created three accounts and one batch since
	// we started, which each mines 6 blocks. So we expect the auto-renewed
	// account to be extended by the default of 3024 blocks from the current
	// height.
	_, currentHeight, err := t.lndHarness.Miner.Client.GetBestBlock()
	require.NoError(t.t, err)
	expectedHeight := uint32(currentHeight + 3024)
	assertTraderAccountState(
		t.t, t.trader, account1.TraderKey,
		poolrpc.AccountState_PENDING_BATCH,
		func(a *poolrpc.Account) error {
			if a.ExpirationHeight != expectedHeight {
				return fmt.Errorf("unexpected account "+
					"expiry, got %d wanted %d",
					a.ExpirationHeight, expectedHeight)
			}

			return nil
		},
	)

	// The second trader's account is still at the initial expiry.
	expectedHeight = uint32(startHeight) + 1_000
	assertTraderAccountState(
		t.t, secondTrader, account2.TraderKey,
		poolrpc.AccountState_PENDING_BATCH,
		func(a *poolrpc.Account) error {
			if a.ExpirationHeight != expectedHeight {
				return fmt.Errorf("unexpected account "+
					"expiry, got %d wanted %d",
					a.ExpirationHeight, expectedHeight)
			}

			return nil
		},
	)
	assertTraderAccountState(
		t.t, secondTrader, account3.TraderKey,
		poolrpc.AccountState_PENDING_BATCH,
		func(a *poolrpc.Account) error {
			if a.ExpirationHeight != expectedHeight {
				return fmt.Errorf("unexpected account "+
					"expiry, got %d wanted %d",
					a.ExpirationHeight, expectedHeight)
			}

			return nil
		},
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
	require.NoError(t.t, err)
	acctOutPoint := masterAcct.Outpoint
	require.Equal(t.t, firstBatchTXID[:], acctOutPoint.Txid)

	// We'll now mine another 3 blocks to ensure the channel itself is
	// fully confirmed.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// Now that the channels are confirmed, they should both be active, and
	// we should be able to make a payment between this new channel
	// established.
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt), *firstBatchTXID,
		charlie.PubKey, defaultOrderDuration,
	)
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt2), *firstBatchTXID,
		charlie.PubKey, defaultOrderDuration,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt2), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration,
	)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
}

// testOrderNodeIDFilters tests that accounts that participate in a batch
// are filtered properly using the allowed/not allowed node id lists.
func testOrderNodeIDFilters(t *harnessTest) {
	// We need a third lnd node, Charlie that is used for the second trader.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

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
	// default trader, selling 5 units (0.5M sats) of liquidity. This order
	// is unable to match with orders created by account2.
	askAmt := btcutil.Amount(500_000)
	_, err := submitAskOrder(
		t.trader, account1.TraderKey, 100, askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			notAllowed := [][]byte{charlie.PubKey[:]}
			ask.Ask.Details.NotAllowedNodeIds = notAllowed
		},
	)
	require.NoError(t.t, err)

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity. So let's submit an order for that.
	bidAmt := btcutil.Amount(800_000)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, 100, bidAmt,
		func(ask *poolrpc.SubmitOrderRequest_Bid) {
			allowed := [][]byte{t.trader.cfg.LndNode.PubKey[:]}
			ask.Bid.Details.AllowedNodeIds = allowed
		},
	)
	require.NoError(t.t, err)

	// Let's kick the auctioneer now to try and create a batch. The only
	// think that makes that there are not valid matches in this batch is
	// the AllowedNodeIDsPredicate.
	_, _ = executeBatch(t, 0)

	// Create a second order from account1 that is able to match
	// with the one from account2. We create it with a different amount
	// so we can be sure that it is the one matching in the next batch.
	matchingAskAmt := btcutil.Amount(200_000)
	_, err = submitAskOrder(
		t.trader, account1.TraderKey, 100, matchingAskAmt,
	)
	require.NoError(t.t, err)

	// Now we expect to see a match of the only bid with the compatible
	// ask one.
	_, _ = executeBatch(t, 1)

	// At this point, the lnd nodes backed by each trader should have a
	// single pending channel, which matches the amount of the order
	// executed above.
	//
	// In our case, Bob is the maker so he should be marked as the
	// initiator of the channel.
	assertPendingChannel(
		t, t.trader.cfg.LndNode, matchingAskAmt, true, charlie.PubKey,
	)

	// Let's now mine the withdrawal to clean up the mempool.
	_ = mineBlocks(t, t.lndHarness, 3, 1)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(context.Background(), t, charlie)
}

// testBatchUnannouncedChannels tests the matching conditions for orders based
// on the resulting channel announcements.
func testBatchUnannouncedChannels(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader
	// who already updated his batch version to UnannouncedChannels.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
		batchVersionOpt(orderT.UnannouncedChannelsBatchVersion),
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	// We need a fourth lnd node, Dave that is used as the third trader who
	// did not update his batch version to UnannouncedChannels yet.
	dave := t.lndHarness.NewNode(t.t, "dave", lndDefaultArgs)
	thirdTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
		batchVersionOpt(orderT.ExtendAccountBatchVersion),
	)
	defer shutdownAndAssert(t, dave, thirdTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, dave)

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for the traders.
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
		t, thirdTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// Now that the accounts are confirmed, submit an ask order from dave
	// selling 1 unit (0.1M sats) of liquidity. This order is unable to
	// match with the order created by account2 because the trader
	// does not support opening unannounced channel.
	askAmt := btcutil.Amount(100_000)
	_, err := submitAskOrder(thirdTrader, account3.TraderKey, 100, askAmt)
	require.NoError(t.t, err)

	// Our second trader (Charlie) wants to buy 1 unit of liquidity for
	// an unannounced channel.
	bidAmt := btcutil.Amount(100_000)
	bidNonce, err := submitBidOrder(
		secondTrader, account2.TraderKey, 100, bidAmt,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.UnannouncedChannel = true
		},
	)
	require.NoError(t.t, err)

	// We now instruct the auctioneer to attempt to create a batch. The only
	// match-invalidating criteria in this batch is the unannounced channel.
	_, _ = executeBatch(t, 0)

	// Cancel the order that did not match so we do not have to worry about
	// it matching during the rest of the tests.
	CancelOrder(t.t, secondTrader, bidNonce)

	// If Charlie bids for an announced channel it should be a match.
	_, err = submitBidOrder(secondTrader, account2.TraderKey, 100, bidAmt)
	require.NoError(t.t, err)

	_, _ = executeBatch(t, 1)
	assertPendingChannel(
		t, thirdTrader.cfg.LndNode, askAmt, true, charlie.PubKey,
		privateChannelCheck(false),
	)

	// Let's now mine to clean up the mempool.
	_ = mineBlocks(t, t.lndHarness, 3, 1)

	// Let's make the main trader the asker and provide liquidity only for
	// unannounced channels.
	_, err = submitAskOrder(
		t.trader, account1.TraderKey, 100, askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			// Only match unannounced channels.
			ask.Ask.AnnouncementConstraints = onlyUnannounced
		},
	)
	require.NoError(t.t, err)

	// This order won't match the liquidity of our main trader because
	// bids for announced channels.
	bidNonce, err = submitBidOrder(
		secondTrader, account2.TraderKey, 100, bidAmt,
	)
	require.NoError(t.t, err)

	_, _ = executeBatch(t, 0)

	// Cancel the order that did not match so we do not have to worry about
	// it matching during the rest of the tests.
	CancelOrder(t.t, secondTrader, bidNonce)

	// Bidding for unannounced should create a match.
	_, err = submitBidOrder(secondTrader, account2.TraderKey, 100, bidAmt,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.UnannouncedChannel = true
		},
	)
	require.NoError(t.t, err)

	_, _ = executeBatch(t, 1)
	assertPendingChannel(
		t, t.trader.cfg.LndNode, askAmt, true,
		charlie.PubKey, privateChannelCheck(true),
	)

	// Let's now mine to clean up the mempool.
	_ = mineBlocks(t, t.lndHarness, 3, 1)

	// Let's create an ask providing liquidity for announced and unannounced
	// channels.
	_, err = submitAskOrder(
		t.trader, account1.TraderKey, 100, 2*askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			// No preference.
			ask.Ask.AnnouncementConstraints = announceNoPreference
		},
	)
	require.NoError(t.t, err)

	_, err = submitBidOrder(secondTrader, account2.TraderKey, 100, bidAmt)
	require.NoError(t.t, err)

	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, 100, bidAmt,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.UnannouncedChannel = true
		},
	)
	require.NoError(t.t, err)

	// Because the asker specified "no preference" the bidder should get
	// two matches.
	txs, _ := executeBatch(t, 1)

	require.Len(t.t, txs, 1)

	// Five new outputs:
	// - Update auctioneer account
	// - Update the two trader accounts
	// - Open two channels one announced and one unannounced.
	require.Len(t.t, txs[0].TxOut, 5)

	// Let's now mine to clean up the mempool.
	_ = mineBlocks(t, t.lndHarness, 3, 1)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
	closeAllChannels(ctx, t, dave)
}

// testBatchZeroConfChannels tests the matching conditions for orders based
// on the number of confirmations that the resulting channel needs before
// starting routing.
func testBatchZeroConfChannels(t *harnessTest) {
	ctx := context.Background()

	extraArgs := []string{
		"--protocol.option-scid-alias",
		"--protocol.zero-conf",
		"--protocol.anchors",
		"--protocol.script-enforced-lease",
	}

	// We need a third lnd node, Charlie that is used for the second trader
	// who already updated his batch version to ZeroConfChannels.
	charlie := t.lndHarness.NewNode(t.t, "charlie", extraArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
		batchVersionOpt(orderT.ZeroConfChannelsBatchVersion),
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	// We need a fourth lnd node, Dave that is used as the third trader who
	// did not update his batch version to ZeroConfChannels yet.
	dave := t.lndHarness.NewNode(t.t, "dave", lndDefaultArgs)
	thirdTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
		batchVersionOpt(orderT.UnannouncedChannelsBatchVersion),
	)
	defer shutdownAndAssert(t, dave, thirdTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, dave)

	// We need a fifth lnd node, Elle that is used for the fourth trader
	// who already updated his batch version to ZeroConfChannels.
	elle := t.lndHarness.NewNode(t.t, "elle", extraArgs)
	fourthTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, elle, t.auctioneer,
		batchVersionOpt(orderT.ZeroConfChannelsBatchVersion),
	)
	defer shutdownAndAssert(t, elle, fourthTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, elle)

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for the traders.
	account2 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	account3 := openAccountAndAssert(
		t, thirdTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	account4 := openAccountAndAssert(
		t, fourthTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// Now that the accounts are confirmed, submit an ask order from dave
	// selling 1 unit (0.1M sats) of liquidity. This order is unable to
	// match with the order created by account1 because the trader
	// does not support opening zero conf channels.
	askAmt := btcutil.Amount(100_000)
	_, err := submitAskOrder(thirdTrader, account3.TraderKey, 100, askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.ConfirmationConstraints = onlyConfirmedChannels
		},
	)
	require.NoError(t.t, err)

	// Elle wants to buy 1 unit of liquidity for a zero conf channel.
	bidAmt := btcutil.Amount(100_000)
	bidNonce, err := submitBidOrder(
		fourthTrader, account4.TraderKey, 100, bidAmt,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.ZeroConfChannel = true
			bid.Bid.Details.ChannelType = scriptEnforcedType
		},
	)
	require.NoError(t.t, err)

	// We now instruct the auctioneer to attempt to create a batch. The only
	// match-invalidating criteria in this batch is the zero conf channel.
	_, _ = executeBatch(t, 0)

	// Cancel the order that did not match so we do not have to worry about
	// it matching during the rest of the tests.
	CancelOrder(t.t, fourthTrader, bidNonce)

	// If Elle bids for a non zero conf channel it should be a match.
	_, err = submitBidOrder(fourthTrader, account4.TraderKey, 100, bidAmt)
	require.NoError(t.t, err)

	txs, _ := executeBatch(t, 1)
	assertPendingChannel(
		t, thirdTrader.cfg.LndNode, askAmt, true,
		fourthTrader.cfg.LndNode.PubKey,
	)

	// Let's now mine to clean up the mempool.
	_ = mineBlocks(t, t.lndHarness, 3, 1)

	// Close all active channels so we start fresh.
	assertActiveChannel(
		t, fourthTrader.cfg.LndNode, int64(bidAmt), txs[0].TxHash(),
		dave.PubKey, defaultOrderDuration,
	)
	closeAllChannels(ctx, t, elle)

	// Let's make the fourth trader the asker and provide liquidity only for
	// ZeroConf channels.
	_, err = submitAskOrder(
		fourthTrader, account4.TraderKey, 100, askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			// Only match zero conf channels.
			ask.Ask.ConfirmationConstraints = onlyZeroConfChannels

			// Script enforced.
			ask.Ask.Details.ChannelType = scriptEnforcedType
		},
	)
	require.NoError(t.t, err)

	// This order won't match the liquidity of our main trader because
	// bids for non-zero conf channels.
	bidNonce, err = submitBidOrder(
		secondTrader, account2.TraderKey, 100, bidAmt,
	)
	require.NoError(t.t, err)

	_, _ = executeBatch(t, 0)

	// Cancel the order that did not match so we do not have to worry about
	// it matching during the rest of the tests.
	CancelOrder(t.t, secondTrader, bidNonce)

	// Bidding for zero conf channels should create a match.
	_, err = submitBidOrder(secondTrader, account2.TraderKey, 100, bidAmt,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.ZeroConfChannel = true
			bid.Bid.Details.ChannelType = scriptEnforcedType
		},
	)
	require.NoError(t.t, err)
	_, _ = executeBatch(t, 1)

	// Check that we are able to route before getting any confirmation.
	resp, err := elle.ListChannels(ctx, &lnrpc.ListChannelsRequest{})
	require.NoError(t.t, err)
	require.Len(t.t, resp.Channels, 1)
	require.True(t.t, resp.Channels[0].Active)
	require.True(t.t, resp.Channels[0].ZeroConf)
	require.NotEmpty(t.t, resp.Channels[0].AliasScids)

	// Let's now mine to clean up the mempool.
	_ = mineBlocks(t, t.lndHarness, 3, 1)

	// Let's create an ask providing liquidity for a zero conf and a normal
	// channel.
	_, err = submitAskOrder(
		fourthTrader, account4.TraderKey, 100, 2*askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			// No preference.
			ask.Ask.ConfirmationConstraints = zcNoPreference
			// Script enforced.
			ask.Ask.Details.ChannelType = scriptEnforcedType
		},
	)
	require.NoError(t.t, err)

	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, 100, bidAmt,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.Details.ChannelType = scriptEnforcedType
		},
	)
	require.NoError(t.t, err)

	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, 100, bidAmt,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.ZeroConfChannel = true
			bid.Bid.Details.ChannelType = scriptEnforcedType
		},
	)
	require.NoError(t.t, err)

	// Because the asker specified "no preference" the bidder should get
	// two matches.
	txs, _ = executeBatch(t, 1)

	require.Len(t.t, txs, 1)

	// Five new outputs:
	// - Update auctioneer account
	// - Update the two trader accounts
	// - Open two channels one zero conf and one that needs confirmations.
	require.Len(t.t, txs[0].TxOut, 5)

	// Elle should have two zero conf channels and one pending one.
	resp, err = elle.ListChannels(ctx, &lnrpc.ListChannelsRequest{})
	require.NoError(t.t, err)
	require.Len(t.t, resp.Channels, 2)
	assertPendingChannel(
		t, fourthTrader.cfg.LndNode, askAmt, true, charlie.PubKey,
	)

	// Let's now mine to clean up the mempool.
	_ = mineBlocks(t, t.lndHarness, 3, 1)

	assertNumPendingChannels(t, elle, 0)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
	closeAllChannels(ctx, t, dave)
	closeAllChannels(ctx, t, elle)
}
