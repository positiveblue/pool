package itest

import (
	"context"
	"fmt"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	accountT "github.com/lightninglabs/pool/account"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
)

// testSelfChanBalance tests that opening a channel with a self channel balance
// through a bid order is possible.
func testSelfChanBalance(t *harnessTest) {
	t.t.Run("version 0", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runSelfChanBalanceTest(ht, accountT.VersionInitialNoVersion)
	})
	t.t.Run("version 1", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runSelfChanBalanceTest(ht, accountT.VersionTaprootEnabled)
	})
}

// runSelfChanBalanceTest tests that opening a channel with a self channel
// balance through a bid order is possible.
func runSelfChanBalanceTest(t *harnessTest, version accountT.Version) {
	// We need a third lnd node, Charlie that is used for the second trader.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders.
	makerAccount := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 5_000,
			},
			Version: rpcVersion(version),
		},
	)
	takerAccount := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 5_000,
			},
			Version: rpcVersion(version),
		},
	)

	// Now that the accounts are confirmed, submit an ask order from our
	// default trader, selling 2 units (200k sats) of liquidity. This ask
	// has the wrong order version and won't be matched with the self chan
	// balance bid.
	const orderFixedRate = 100
	ask1Amt := btcutil.Amount(200_000)
	ask1, err := submitAskOrder(
		t.trader, makerAccount.TraderKey, orderFixedRate, ask1Amt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.Version = uint32(orderT.VersionLeaseDurationBuckets)
		},
	)
	require.NoError(t.t, err)

	// From the secondary account of the second trader, we also create an
	// order to buy some units. The order has a self channel balance set.
	const selfChanBalance = 20_000
	bidAmt := btcutil.Amount(100_000)
	_, err = submitBidOrder(
		secondTrader, takerAccount.TraderKey, orderFixedRate, bidAmt,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.SelfChanBalance = selfChanBalance
			bid.Bid.Version = uint32(orderT.VersionSelfChanBalance)
		},
	)
	require.NoError(t.t, err)

	// Since the ask is of the wrong version, a batch should not be
	// cleared, so a batch transaction should not be broadcast. Kick the
	// auctioneer and wait for it to return back to the order submit state.
	// No tx should be in the mempool as no market should be possible.
	_, _ = executeBatch(t, 0)

	// Let's add a second ask with the correct version now so it can be
	// matched against the bid.
	ask2Amt := btcutil.Amount(300_000)
	ask2, err := submitAskOrder(
		t.trader, makerAccount.TraderKey, orderFixedRate, ask2Amt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.Version = uint32(orderT.VersionSelfChanBalance)
		},
	)
	require.NoError(t.t, err)

	// Let's kick the auctioneer again to try and create a batch.
	_, batchTXIDs := executeBatch(t, 1)
	firstBatchTXID := batchTXIDs[0]

	// At this point, the lnd nodes backed by each trader should have a
	// single pending channel, which matches the amount of the order
	// executed above.
	//
	// In our case, Bob is the maker so he should be marked as the
	// initiator of the channel.
	assertPendingChannel(
		t, t.trader.cfg.LndNode, bidAmt+selfChanBalance, true,
		charlie.PubKey,
		func(c *lnrpc.PendingChannelsResponse_PendingChannel) error {
			if c.RemoteBalance != selfChanBalance {
				return fmt.Errorf("unexpected remote balance "+
					"%d, wanted %d", c.RemoteBalance,
					selfChanBalance)
			}

			return nil
		},
	)
	assertPendingChannel(
		t, charlie, bidAmt+selfChanBalance, false,
		t.trader.cfg.LndNode.PubKey,
		func(c *lnrpc.PendingChannelsResponse_PendingChannel) error {
			if c.LocalBalance != selfChanBalance {
				return fmt.Errorf("unexpected local balance "+
					"%d, wanted %d", c.LocalBalance,
					selfChanBalance)
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

	// We'll now mine another 3 blocks to ensure the channel itself is
	// fully confirmed and the accounts in the open state again.
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// Now that the channels are confirmed, they should both be active, and
	// we should be able to make a payment between this new channel
	// established.
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt+selfChanBalance),
		*firstBatchTXID, charlie.PubKey, defaultOrderDuration,
		func(c *lnrpc.Channel) error {
			if c.RemoteBalance != selfChanBalance {
				return fmt.Errorf("unexpected remote balance "+
					"%d, wanted %d", c.RemoteBalance,
					selfChanBalance)
			}

			return nil
		},
	)
	assertActiveChannel(
		t, charlie, int64(bidAmt+selfChanBalance), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration,
		func(c *lnrpc.Channel) error {
			if c.LocalBalance != selfChanBalance {
				return fmt.Errorf("unexpected local balance "+
					"%d, wanted %d", c.LocalBalance,
					selfChanBalance)
			}

			return nil
		},
	)

	// Finally make sure the accounts were charged correctly. The base order
	// fee is 1 satoshi and the rate is 1/1000.
	submissionFee := 1 + (bidAmt / 1000)
	premium := orderT.FixedRatePremium(orderFixedRate).LumpSumPremium(
		bidAmt, defaultOrderDuration,
	)
	chainFees := orderT.EstimateTraderFee(
		1, chainfee.SatPerKWeight(12_500), version,
	)
	makerBalance := btcutil.Amount(defaultAccountValue) - submissionFee -
		chainFees - bidAmt + premium
	takerBalance := btcutil.Amount(defaultAccountValue) - submissionFee -
		chainFees - selfChanBalance - premium
	assertTraderAccount(
		t, t.trader, makerAccount.TraderKey, makerBalance,
		makerAccount.ExpirationHeight, poolrpc.AccountState_OPEN,
	)
	assertTraderAccount(
		t, secondTrader, takerAccount.TraderKey, takerBalance,
		takerAccount.ExpirationHeight, poolrpc.AccountState_OPEN,
	)

	// Make sure we can run the same subtest again with the same node, so
	// let's clean up.
	ctx := context.Background()
	closeAllChannels(ctx, t, charlie)
	_, _ = t.trader.CancelOrder(ctx, &poolrpc.CancelOrderRequest{
		OrderNonce: ask1[:],
	})
	_, _ = t.trader.CancelOrder(ctx, &poolrpc.CancelOrderRequest{
		OrderNonce: ask2[:],
	})
	closeAccountAndAssert(t, t.trader, &poolrpc.CloseAccountRequest{
		TraderKey: makerAccount.TraderKey,
	})
	closeAccountAndAssert(t, secondTrader, &poolrpc.CloseAccountRequest{
		TraderKey: takerAccount.TraderKey,
	})
}
