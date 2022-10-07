package itest

import (
	"context"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightninglabs/pool/auctioneerrpc"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/stretchr/testify/require"
)

const (
	orderFixedRate = 100
	askAmt         = btcutil.Amount(1_500_000)
	bidAmt         = btcutil.Amount(800_000)
	bidAmt2        = btcutil.Amount(300_000)
)

// testOrderMetricsFlow tests that we're able to properly
// incorporate the metrics manager into the auctioneer,
// and checks that the rpc handles populating and retreiving
// order metrics properly.
func testOrderMetricsFlow(t *harnessTest) {
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

	ask1nonce, ask2nonce, ask3nonce := createOrders(t, ctx, charlie, secondTrader, account1, account2, account3, uint32(2016))

	metrics, err := t.auctioneer.OrderMetrics(
		ctx, &auctioneerrpc.OrderMetricsRequest{},
	)

	require.NoError(t.t, err)
	require.Equal(t.t, 3, len(metrics.OrderMetricsByLeaseDuration))

	expectedOrderMetrics := map[uint32]map[string]int64{
		uint32(2016): {
			"num_asks":   int64(1),
			"num_bids":   int64(2),
			"bid_volume": int64(1100000),
			"ask_volume": int64(1500000),
		},
		uint32(4032): {
			"num_asks":   int64(1),
			"num_bids":   int64(2),
			"bid_volume": int64(2200000),
			"ask_volume": int64(3000000),
		},
		uint32(6048): {
			"num_asks":   int64(1),
			"num_bids":   int64(2),
			"bid_volume": int64(3300000),
			"ask_volume": int64(4500000),
		},
	}
	for leaseDuration, metric := range metrics.OrderMetricsByLeaseDuration {
		require.Contains(t.t, expectedOrderMetrics, leaseDuration)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["ask_volume"], metric.AskVolume)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["bid_volume"], metric.BidVolume)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["num_asks"], metric.NumAsks)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["num_bids"], metric.NumBids)
	}

	// execute a batch
	createBatch(t, ctx, charlie, secondTrader, account1, account2, account3, ask1nonce, ask2nonce, ask3nonce, uint32(2016))

	// Should set the refresh rate to 1000s
	newTimeIntervalIn := uint64(1e+12)
	_, err = t.auctioneer.AuctionAdminClient.SetRefreshRate(ctx, &adminrpc.SetRefreshRateRequest{
		TimeDuration: newTimeIntervalIn,
	})
	require.NoError(t.t, err)

	metrics, err = t.auctioneer.OrderMetrics(
		ctx, &auctioneerrpc.OrderMetricsRequest{},
	)

	require.NoError(t.t, err)
	require.Equal(t.t, 3, len(metrics.OrderMetricsByLeaseDuration))

	// At this point the refresh rate has not been hit, so the cached orders should
	// still be used for metrics calculation rather than the actual outstanding bids/asks
	for leaseDuration, metric := range metrics.OrderMetricsByLeaseDuration {
		require.Contains(t.t, expectedOrderMetrics, leaseDuration)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["ask_volume"], metric.AskVolume)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["bid_volume"], metric.BidVolume)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["num_asks"], metric.NumAsks)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["num_bids"], metric.NumBids)
	}

	// Should set the refresh rate to 1000s
	newTimeIntervalIn = uint64(2)
	_, err = t.auctioneer.AuctionAdminClient.SetRefreshRate(ctx, &adminrpc.SetRefreshRateRequest{
		TimeDuration: newTimeIntervalIn,
	})
	require.NoError(t.t, err)

	// Sleep 2 ns to be safe
	time.Sleep(time.Duration(time.Duration(2).Nanoseconds()))

	metrics, err = t.auctioneer.OrderMetrics(
		ctx, &auctioneerrpc.OrderMetricsRequest{},
	)

	require.NoError(t.t, err)
	require.Equal(t.t, 3, len(metrics.OrderMetricsByLeaseDuration))

	expectedOrderMetrics = map[uint32]map[string]int64{
		uint32(2016): {
			"num_asks":   int64(1),
			"num_bids":   int64(0),
			"bid_volume": int64(0),
			"ask_volume": int64(400000),
		},
		uint32(4032): {
			"num_asks":   int64(1),
			"num_bids":   int64(0),
			"bid_volume": int64(0),
			"ask_volume": int64(800000),
		},
		uint32(6048): {
			"num_asks":   int64(1),
			"num_bids":   int64(0),
			"bid_volume": int64(0),
			"ask_volume": int64(1200000),
		},
	}

	// At this point the refresh rate has not been hit, so the cached orders should
	// still be used for metrics calculation rather than the actual outstanding bids/asks
	for leaseDuration, metric := range metrics.OrderMetricsByLeaseDuration {
		require.Contains(t.t, expectedOrderMetrics, leaseDuration)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["ask_volume"], metric.AskVolume)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["bid_volume"], metric.BidVolume)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["num_asks"], metric.NumAsks)
		require.Equal(t.t, expectedOrderMetrics[leaseDuration]["num_bids"], metric.NumBids)
	}

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
}

// testBatchMetricsFlow tests that we're able to properly
// incorporate the metrics manager into the auctioneer,
// and checks that the rpc handles populating and retreiving
// batch metrics properly. We set the RefreshRate to re-populate
// our cache.
func testBatchMetricsFlow(t *harnessTest) {
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

	// Should set the refresh rate to 1000s
	newTimeIntervalIn := uint64(1e+12)
	_, err = t.auctioneer.AuctionAdminClient.SetRefreshRate(ctx, &adminrpc.SetRefreshRateRequest{
		TimeDuration: newTimeIntervalIn,
	})
	require.NoError(t.t, err)

	metrics, err := t.auctioneer.BatchMetrics(
		ctx, &auctioneerrpc.BatchMetricsRequest{},
	)

	require.NoError(t.t, err)
	// We expect four time duration buckets
	require.Equal(t.t, 0, len(metrics.BatchMetricsByTimeframe))

	ask1nonce, ask2nonce, ask3nonce := createOrders(t, ctx, charlie, secondTrader, account1, account2, account3, uint32(2016))
	createBatch(t, ctx, charlie, secondTrader, account1, account2, account3, ask1nonce, ask2nonce, ask3nonce, uint32(2016))

	metrics, err = t.auctioneer.BatchMetrics(
		ctx, &auctioneerrpc.BatchMetricsRequest{},
	)

	require.NoError(t.t, err)
	// We expect that the cache has not yet been updated
	require.Equal(t.t, 0, len(metrics.BatchMetricsByTimeframe))

	// Should set the refresh rate to 1ms
	newTimeIntervalIn = uint64(2)
	_, err = t.auctioneer.AuctionAdminClient.SetRefreshRate(ctx, &adminrpc.SetRefreshRateRequest{
		TimeDuration: newTimeIntervalIn,
	})
	require.NoError(t.t, err)

	// Sleep 2 ns to be safe
	time.Sleep(time.Duration(time.Duration(2).Nanoseconds()))

	metrics, err = t.auctioneer.BatchMetrics(
		ctx, &auctioneerrpc.BatchMetricsRequest{},
	)

	require.NoError(t.t, err)

	expectedBatchMetrics := map[uint64]map[uint32]map[string]float64{
		1: {
			2016: {
				"total_sats_leased":  1100000,
				"total_fees_accrued": 200,
				"median_order_size":  550000,
				"median_apr":         float64(0.005256),
			},
			4032: {
				"total_sats_leased":  2200000,
				"total_fees_accrued": 400,
				"median_order_size":  1.1e+06,
				"median_apr":         float64(0.010512),
			},
			6048: {
				"total_sats_leased":  3300000,
				"total_fees_accrued": 600,
				"median_order_size":  1.65e+06,
				"median_apr":         float64(0.015768),
			},
		},
		7: {
			2016: {
				"total_sats_leased":  1100000,
				"total_fees_accrued": 200,
				"median_order_size":  550000,
				"median_apr":         float64(0.005256),
			},
			4032: {
				"total_sats_leased":  2200000,
				"total_fees_accrued": 400,
				"median_order_size":  1.1e+06,
				"median_apr":         float64(0.010512),
			},
			6048: {
				"total_sats_leased":  3300000,
				"total_fees_accrued": 600,
				"median_order_size":  1.65e+06,
				"median_apr":         float64(0.015768),
			},
		},
		30: {
			2016: {
				"total_sats_leased":  1100000,
				"total_fees_accrued": 200,
				"median_order_size":  550000,
				"median_apr":         float64(0.005256),
			},
			4032: {
				"total_sats_leased":  2200000,
				"total_fees_accrued": 400,
				"median_order_size":  1.1e+06,
				"median_apr":         float64(0.010512),
			},
			6048: {
				"total_sats_leased":  3300000,
				"total_fees_accrued": 600,
				"median_order_size":  1.65e+06,
				"median_apr":         float64(0.015768),
			},
		},
		365: {
			2016: {
				"total_sats_leased":  1100000,
				"total_fees_accrued": 200,
				"median_order_size":  550000,
				"median_apr":         float64(0.005256),
			},
			4032: {
				"total_sats_leased":  2200000,
				"total_fees_accrued": 400,
				"median_order_size":  1.1e+06,
				"median_apr":         float64(0.010512),
			},
			6048: {
				"total_sats_leased":  3300000,
				"total_fees_accrued": 600,
				"median_order_size":  1.65e+06,
				"median_apr":         float64(0.015768),
			},
		},
	}

	for timeframe, timeframeBucket := range metrics.BatchMetricsByTimeframe {
		for leaseDuration, batchMetric := range timeframeBucket.BatchMetricsByDuration {
			require.Equal(t.t, expectedBatchMetrics[timeframe][leaseDuration]["median_apr"], batchMetric.MedianApr)
			require.Equal(t.t, expectedBatchMetrics[timeframe][leaseDuration]["median_order_size"], batchMetric.MedianOrderSize)
			require.Equal(t.t, expectedBatchMetrics[timeframe][leaseDuration]["total_fees_accrued"], float64(batchMetric.TotalFeesAccrued))
			require.Equal(t.t, expectedBatchMetrics[timeframe][leaseDuration]["total_sats_leased"], float64(batchMetric.TotalSatsLeased))
		}
	}

	expectedWeeklyVolume := map[uint32]int64{
		2016: 1100000,
		4032: 2200000,
		6048: 3300000,
	}
	for day, weeklyMetrics := range metrics.WeeklyVolumeMetricsByDuration {
		require.Equal(t.t, 3, len(weeklyMetrics.TotalVolume))
		require.Equal(t.t, day, weeklyMetrics.CurrentDay)
		for duration, volume := range weeklyMetrics.TotalVolume {
			require.Equal(t.t, expectedWeeklyVolume[duration], volume)
		}
	}

	// At this point the cache interval has not been reached,
	// Meaning we should not see changes, despite adding batches
	require.Equal(t.t, 4, len(metrics.BatchMetricsByTimeframe))
	for day, weeklyMetrics := range metrics.WeeklyVolumeMetricsByDuration {
		require.Equal(t.t, 3, len(weeklyMetrics.TotalVolume))
		require.Equal(t.t, day, weeklyMetrics.CurrentDay)
		for duration, volume := range weeklyMetrics.TotalVolume {
			require.Equal(t.t, expectedWeeklyVolume[duration], volume)
		}
	}

	require.NoError(t.t, err)
	// At this point the cache interval should be reached,
	// meaning we should see changes in the batch metrics
	require.Equal(t.t, 4, len(metrics.BatchMetricsByTimeframe))
	for day, weeklyMetrics := range metrics.WeeklyVolumeMetricsByDuration {
		require.Equal(t.t, 3, len(weeklyMetrics.TotalVolume))
		require.Equal(t.t, day, weeklyMetrics.CurrentDay)
		for duration, volume := range weeklyMetrics.TotalVolume {
			require.Equal(t.t, expectedWeeklyVolume[duration], volume)
		}
	}

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
}

func createOrders(t *harnessTest, ctx context.Context, charlie *lntest.HarnessNode, secondTrader *traderHarness, account1 *poolrpc.Account, account2 *poolrpc.Account, account3 *poolrpc.Account, targetDuration uint32) (orderT.Nonce, orderT.Nonce, orderT.Nonce) {
	// Now that the accounts are confirmed, we can start submitting orders.
	// We first make sure that we cannot submit an order outside of the
	// legacy duration bucket if we're not on the correct order version.

	_, err := submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate, askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.LeaseDurationBlocks = targetDuration * 2
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
			ask.Ask.LeaseDurationBlocks = targetDuration * 2
		},
	)
	require.NoError(t.t, err)
	ask1cNonce, err := submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate*3, askAmt*3,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.LeaseDurationBlocks = targetDuration * 3
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
			bid.Bid.LeaseDurationBlocks = targetDuration * 2
		},
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate*3, bidAmt*3,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = targetDuration * 3
		},
	)
	require.NoError(t.t, err)

	// From the secondary account of the second trader, we also create an
	// order to buy some units. The order should also make it into the same
	// batch and the second trader should sign a message for both orders at
	// the same time.
	_, err = submitBidOrder(
		secondTrader, account3.TraderKey, orderFixedRate, bidAmt2,
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, account3.TraderKey, orderFixedRate*2, bidAmt2*2,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = targetDuration * 2
		},
	)
	require.NoError(t.t, err)
	_, err = submitBidOrder(
		secondTrader, account3.TraderKey, orderFixedRate*3, bidAmt2*3,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = targetDuration * 3
		},
	)
	require.NoError(t.t, err)

	return ask1aNonce, ask1bNonce, ask1cNonce
}

func createBatch(t *harnessTest, ctx context.Context, charlie *lntest.HarnessNode, secondTrader *traderHarness, account1 *poolrpc.Account, account2 *poolrpc.Account, account3 *poolrpc.Account, ask1aNonce orderT.Nonce, ask1bNonce orderT.Nonce, ask1cNonce orderT.Nonce, targetDuration uint32) {
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
		t, t.trader.cfg.LndNode, int64(bidAmt2), *firstBatchTXID,
		charlie.PubKey, targetDuration,
	)
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt2*2), *firstBatchTXID,
		charlie.PubKey, targetDuration*2,
	)
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt2*3), *firstBatchTXID,
		charlie.PubKey, targetDuration*3,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt2), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, targetDuration,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt2*2), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, targetDuration*2,
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt2*3), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, targetDuration*3,
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
			targetDuration: {
				uint64(bidAmt), uint64(bidAmt2),
			},
			targetDuration * 2: {
				uint64(bidAmt * 2), uint64(bidAmt2 * 2),
			},
			targetDuration * 3: {
				uint64(bidAmt * 3), uint64(bidAmt2 * 3),
			},
		}, map[uint32]orderT.FixedRatePremium{
			targetDuration:     orderFixedRate,
			targetDuration * 2: orderFixedRate * 2,
			targetDuration * 3: orderFixedRate * 3,
		},
	)
	assertTraderAssets(t, t.trader, 6, []*chainhash.Hash{firstBatchTXID})
	assertTraderAssets(t, secondTrader, 6, []*chainhash.Hash{firstBatchTXID})
}
