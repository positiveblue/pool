package itest

import (
	"bytes"
	"context"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/pool/auctioneerrpc"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/pool/sidecar"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
)

// sidecarChannelsHappyPath tests that opening a sidecar channel through a bid
// order is possible.
func sidecarChannelsHappyPath(ctx context.Context, t *harnessTest, auto bool) {
	// We need a third and fourth lnd node for the additional participants.
	// Charlie is the sidecar channel provider (has an account, submits the
	// bid order) and Dave is the sidecar channel recipient (has no account
	// and only receives the channel).
	charlie, err := t.lndHarness.NewNode("charlie", nil)
	require.NoError(t.t, err)
	providerTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, providerTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	require.NoError(t.t, err)

	dave, err := t.lndHarness.NewNode("dave", nil)
	require.NoError(t.t, err)
	recipientTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
	)
	defer shutdownAndAssert(t, dave, recipientTrader)

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders. To test the message multi-plexing between token IDs
	// and accounts, we add a secondary account to the second trader.
	makerAccount := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	providerAccount := openAccountAndAssert(
		t, providerTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// Now that the accounts are confirmed, submit an ask order from our
	// maker, selling 200 units (200k sats) of liquidity.
	const (
		orderFixedRate  = 100
		askAmt          = 200_000
		selfChanBalance = 100_000
	)
	_, err = submitAskOrder(
		t.trader, makerAccount.TraderKey, orderFixedRate, askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.Version = uint32(orderT.VersionSidecarChannel)
		},
	)
	require.NoError(t.t, err)

	// Create the sidecar channel ticket now, including the offer, order and
	// channel expectation.
	firstSidecarBid := makeSidecar(
		t, providerTrader, recipientTrader, providerAccount.TraderKey,
		orderFixedRate, askAmt, selfChanBalance, auto,
	)

	// We are now ready to start the actual batch execution process.
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
		t, t.trader.cfg.LndNode, askAmt+selfChanBalance, true,
		dave.PubKey, remotePendingBalanceCheck(selfChanBalance),
	)
	assertPendingChannel(
		t, dave, askAmt+selfChanBalance, false,
		t.trader.cfg.LndNode.PubKey,
		localPendingBalanceCheck(selfChanBalance),
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
		t, t.trader.cfg.LndNode, int64(askAmt+selfChanBalance),
		*firstBatchTXID, dave.PubKey, defaultOrderDuration,
		remoteActiveBalanceCheck(selfChanBalance),
	)
	assertActiveChannel(
		t, dave, int64(askAmt+selfChanBalance), *firstBatchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration,
		localActiveBalanceCheck(selfChanBalance),
	)

	// Assert that our lease is returned correctly on the provider side.
	assertSidecarLease(
		t, providerTrader, 1, []*chainhash.Hash{firstBatchTXID},
		firstSidecarBid, selfChanBalance,
	)

	// Finally make sure the accounts were charged correctly. The base order
	// fee is 1 satoshi and the rate is 1/1000.
	submissionFee := 1 + (btcutil.Amount(askAmt) / 1000)
	premium := orderT.FixedRatePremium(orderFixedRate).LumpSumPremium(
		askAmt, defaultOrderDuration,
	)
	chainFees := orderT.EstimateTraderFee(1, chainfee.SatPerKWeight(12_500))
	makerBalance := btcutil.Amount(defaultAccountValue) - submissionFee -
		chainFees - askAmt + premium
	takerBalance := btcutil.Amount(defaultAccountValue) - submissionFee -
		chainFees - selfChanBalance - premium
	assertTraderAccount(
		t, t.trader, makerAccount.TraderKey, makerBalance,
		makerAccount.ExpirationHeight, poolrpc.AccountState_OPEN,
	)
	assertTraderAccount(
		t, providerTrader, providerAccount.TraderKey, takerBalance,
		providerAccount.ExpirationHeight, poolrpc.AccountState_OPEN,
	)

	// As a final test, we want to make sure that a recipient can execute a
	// sidecar order at the same time as they also lease a normal channel
	// from their own account. Let's create an account for the recipient now
	// and submit new orders.
	_ = t.lndHarness.SendCoins(ctx, 5_000_000, dave)
	recipientAccount := openAccountAndAssert(
		t, recipientTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	const bidAmt = 300_000
	askAmtLarge := btcutil.Amount(askAmt + bidAmt)
	_, err = submitAskOrder(
		t.trader, makerAccount.TraderKey, orderFixedRate, askAmtLarge,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.Version = uint32(orderT.VersionSidecarChannel)
		},
	)
	require.NoError(t.t, err)

	// Create the sidecar channel ticket now, including the offer, order and
	// channel expectation.
	secondSidecarBid := makeSidecar(
		t, providerTrader, recipientTrader, providerAccount.TraderKey,
		orderFixedRate, askAmt, 0, auto,
	)

	// Also add a normal bid order from the recipient's account.
	_, err = submitBidOrder(
		recipientTrader, recipientAccount.TraderKey, orderFixedRate,
		bidAmt, func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.Version = uint32(orderT.VersionSidecarChannel)
			bid.Bid.Details.MinUnitsMatch = 3
			bid.Bid.MinNodeTier = auctioneerrpc.NodeTier_TIER_0
		},
	)
	require.NoError(t.t, err)

	// We are now ready to start the actual batch execution process.
	// Let's kick the auctioneer again to try and create a batch.
	_, batchTXIDs = executeBatch(t, 1)
	secondBatchTXID := batchTXIDs[0]

	// At this point the recipient should have two pending channels from the
	// maker node.
	assertPendingChannel(t, t.trader.cfg.LndNode, askAmt, true, dave.PubKey)
	assertPendingChannel(
		t, dave, askAmt, false, t.trader.cfg.LndNode.PubKey,
	)
	assertPendingChannel(t, t.trader.cfg.LndNode, bidAmt, true, dave.PubKey)
	assertPendingChannel(
		t, dave, bidAmt, false, t.trader.cfg.LndNode.PubKey,
	)

	// Clear the mempool and all channels for the following tests.
	_ = mineBlocks(t, t.lndHarness, 3, 1)
	closeAllChannels(ctx, t, dave)

	// Assert that our leases are returned correctly on the provider side.
	assertSidecarLease(
		t, providerTrader, 2, []*chainhash.Hash{
			firstBatchTXID, secondBatchTXID,
		}, secondSidecarBid, 0,
	)
}

// testSidecarChannelsHappyPath tests that opening a sidecar channel through a
// bid order is possible.
func testSidecarChannelsHappyPath(t *harnessTest) {
	ctx := context.Background()

	// We'll ensure that the protocol works when we're using both automated
	// and manual negotiation.
	for _, auto := range []bool{true, false} {
		sidecarChannelsHappyPath(ctx, t, auto)
	}
}

// testSidecarChannelsRejectNewNodesOnly makes sure that if a sidecar channel
// recipient rejects a batch because of duplicate channels the execution is
// re-attempted correctly.
func testSidecarChannelsRejectNewNodesOnly(t *harnessTest) {
	ctx := context.Background()

	// We need a third and fourth lnd node for the additional participants.
	// Charlie is the sidecar channel provider (has an account, submits the
	// bid order) and Dave is the sidecar channel recipient (has no account
	// and only receives the channel).
	charlie, err := t.lndHarness.NewNode("charlie", nil)
	require.NoError(t.t, err)
	providerTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, providerTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	require.NoError(t.t, err)

	// Dave only wants channels from new nodes. We use this to make sure
	// they reject the sidecar channel if they already have a channel from
	// the maker.
	dave, err := t.lndHarness.NewNode("dave", nil)
	require.NoError(t.t, err)
	recipientTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
		newNodesOnlyOpt(),
	)
	defer shutdownAndAssert(t, dave, recipientTrader)

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders. To test the message multi-plexing between token IDs
	// and accounts, we add a secondary account to the second trader.
	makerAccount := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	providerAccount := openAccountAndAssert(
		t, providerTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// We manually open a channel between Bob and Dave now to saturate the
	// newnodesonly setting.
	err = t.lndHarness.EnsureConnected(ctx, t.trader.cfg.LndNode, dave)
	require.NoError(t.t, err)
	_, err = t.lndHarness.OpenPendingChannel(
		ctx, t.trader.cfg.LndNode, dave, 1_000_000, 0,
	)
	require.NoError(t.t, err)
	_ = mineBlocks(t, t.lndHarness, 1, 1)

	// Now that the accounts are confirmed and the pending channel is open,
	// submit an ask order from our maker, selling 200 units (200k sats) of
	// liquidity.
	const (
		orderFixedRate = 100
		askAmt         = 200_000
	)
	_, err = submitAskOrder(
		t.trader, makerAccount.TraderKey, orderFixedRate, askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.Version = uint32(orderT.VersionSidecarChannel)
		},
	)
	require.NoError(t.t, err)

	// Create the sidecar channel ticket now, including the offer, order and
	// channel expectation.
	firstSidecarBid := makeSidecar(
		t, providerTrader, recipientTrader, providerAccount.TraderKey,
		orderFixedRate, askAmt, 0, false,
	)

	// We are now ready to start the actual batch execution process. Let's
	// kick the auctioneer again to try and create a batch. This should
	// fail because the recipient rejected.
	_, _ = executeBatch(t, 0)

	// Close the offending channel and try again.
	closeAllChannels(ctx, t, dave)
	_, batchTXIDs := executeBatch(t, 1)
	firstBatchTXID := batchTXIDs[0]

	// At this point the recipient should have two pending channels from the
	// maker node.
	assertPendingChannel(t, t.trader.cfg.LndNode, askAmt, true, dave.PubKey)
	assertPendingChannel(
		t, dave, askAmt, false, t.trader.cfg.LndNode.PubKey,
	)

	// Clear the mempool and all channels for the following tests.
	_ = mineBlocks(t, t.lndHarness, 3, 1)
	closeAllChannels(ctx, t, dave)

	// Assert that our lease is returned correctly on the provider side.
	assertSidecarLease(
		t, providerTrader, 1, []*chainhash.Hash{firstBatchTXID},
		firstSidecarBid, 0,
	)
}

// testSidecarChannelsRejectMinChanSize makes sure that if a sidecar channel
// recipient rejects a batch because of unmet minimum channel size the execution
// is aborted.
func testSidecarChannelsRejectMinChanSize(t *harnessTest) {
	ctx := context.Background()

	// We need a third and fourth lnd node for the additional participants.
	// Charlie is the sidecar channel provider (has an account, submits the
	// bid order) and Dave is the sidecar channel recipient (has no account
	// and only receives the channel).
	charlie, err := t.lndHarness.NewNode("charlie", nil)
	require.NoError(t.t, err)
	providerTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, providerTrader)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	require.NoError(t.t, err)

	// Dave only wants big channels. We use this to provoke a failure during
	// the signing phase instead of the preparation phase.
	dave, err := t.lndHarness.NewNode(
		"dave", []string{"--minchansize=300000"},
	)
	require.NoError(t.t, err)
	recipientTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
	)
	defer shutdownAndAssert(t, dave, recipientTrader)

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders. To test the message multi-plexing between token IDs
	// and accounts, we add a secondary account to the second trader.
	makerAccount := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	providerAccount := openAccountAndAssert(
		t, providerTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// We manually open a channel between Bob and Dave now to saturate the
	// newnodesonly setting.
	err = t.lndHarness.EnsureConnected(ctx, t.trader.cfg.LndNode, dave)
	require.NoError(t.t, err)
	_, err = t.lndHarness.OpenPendingChannel(
		ctx, t.trader.cfg.LndNode, dave, 1_000_000, 0,
	)
	require.NoError(t.t, err)
	_ = mineBlocks(t, t.lndHarness, 1, 1)

	// Now that the accounts are confirmed and the pending channel is open,
	// submit an ask order from our maker, selling 200 units (200k sats) of
	// liquidity.
	const (
		orderFixedRate = 100
		askAmt         = 200_000
	)
	_, err = submitAskOrder(
		t.trader, makerAccount.TraderKey, orderFixedRate, askAmt,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.Version = uint32(orderT.VersionSidecarChannel)
		},
	)
	require.NoError(t.t, err)

	// Create the sidecar channel ticket now, including the offer, order and
	// channel expectation.
	makeSidecar(
		t, providerTrader, recipientTrader, providerAccount.TraderKey,
		orderFixedRate, askAmt, 0, false,
	)

	// We are now ready to start the actual batch execution process. Let's
	// kick the auctioneer again to try and create a batch. This should
	// fail because the recipient rejected.
	_, _ = executeBatch(t, 0)
}

// makeSidecar creates a sidecar ticket with an offer on the provider node,
// registers the offer with the recipient node, creates a bid order for the
// provider and finally adds the channel expectation to the recipient's node.
func makeSidecar(t *harnessTest, providerTrader, recipientTrader *traderHarness,
	providerAccountKey []byte, orderFixedRate uint32, // nolint:unparam
	askAmt btcutil.Amount, selfChanBalance uint64, // nolint:unparam
	auto bool) orderT.Nonce { // nolint:unparam

	ctx := context.Background()

	var bid *poolrpc.Bid
	if auto {
		bid = &poolrpc.Bid{
			Details: &poolrpc.Order{
				TraderKey:               providerAccountKey,
				RateFixed:               orderFixedRate,
				Amt:                     uint64(askAmt),
				MinUnitsMatch:           2,
				MaxBatchFeeRateSatPerKw: uint64(12500),
			},
			LeaseDurationBlocks: defaultOrderDuration,
			Version:             uint32(orderT.VersionSidecarChannel),
			SelfChanBalance:     selfChanBalance,
			MinNodeTier:         auctioneerrpc.NodeTier_TIER_0,
		}
	} else {
		bid = &poolrpc.Bid{
			Details: &poolrpc.Order{
				TraderKey: providerAccountKey,
				Amt:       uint64(askAmt),
			},
			SelfChanBalance:     selfChanBalance,
			LeaseDurationBlocks: defaultOrderDuration,
		}
	}

	// The first step for creating a sidecar channel is creating an offer.
	// This is the responsibility of the provider. We'll create an offer for
	// 2 units of liquidity with 100k sats self channel balance.
	offerResp, err := providerTrader.OfferSidecar(
		ctx, &poolrpc.OfferSidecarRequest{
			AutoNegotiate: auto,
			Bid:           bid,
		},
	)
	require.NoError(t.t, err)

	// The next step 2/4 is to register the offer with the recipient so they
	// can add their information to the ticket.
	registerResp, err := recipientTrader.RegisterSidecar(
		ctx, &poolrpc.RegisterSidecarRequest{
			Ticket: offerResp.Ticket,
		},
	)
	require.NoError(t.t, err)

	// If we're using automated negotiation, then the last two steps will
	// be done automatically, so we can exit here.
	//
	// TODO(roasbeef): need additional synchronization to assert order
	// details?
	if auto {
		// If this is an auto negotiated sidecar ticket, then we'll
		// wait for the sidecar responder to connect as a new trader.
		registeredTicket, err := sidecar.DecodeString(
			registerResp.Ticket,
		)
		require.NoError(t.t, err)

		assertSidecarTraderSubscribed(
			t, registeredTicket.Recipient.MultiSigPubKey,
		)

		var bidNonce orderT.Nonce
		copy(bidNonce[:], registeredTicket.Order.BidNonce[:])
		return bidNonce
	}

	// Step 3/4 is for the provider to submit a bid order referencing the
	// registered ticket.
	bidNonce, err := submitBidOrder(
		providerTrader, providerAccountKey, orderFixedRate,
		askAmt, func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.SelfChanBalance = selfChanBalance
			bid.Bid.Version = uint32(orderT.VersionSidecarChannel)
			bid.Bid.Details.MinUnitsMatch = 2
			bid.Bid.MinNodeTier = auctioneerrpc.NodeTier_TIER_0
			bid.Bid.SidecarTicket = registerResp.Ticket
		},
	)
	require.NoError(t.t, err)

	// We need to query the order after submission to get the updated ticket
	// with the order information inside.
	bids, err := providerTrader.ListOrders(ctx, &poolrpc.ListOrdersRequest{
		ActiveOnly: true,
	})
	require.NoError(t.t, err)
	require.Len(t.t, bids.Bids, 1)
	offeredTicket := bids.Bids[0].SidecarTicket

	// And the final step 4/4 is for the recipient to start expecting the
	// incoming channel.
	_, err = recipientTrader.ExpectSidecarChannel(
		ctx, &poolrpc.ExpectSidecarChannelRequest{
			Ticket: offeredTicket,
		},
	)
	require.NoError(t.t, err)

	return bidNonce
}

// assertSidecarLease makes sure that the leased sidecar channel can be found
// among the list of leases on the provider side.
func assertSidecarLease(t *harnessTest, trader *traderHarness,
	numTotalAssets int, chainTxns []*chainhash.Hash, bidNonce orderT.Nonce,
	selfChanBalance uint64) {

	assertTraderAssets(
		t, trader, numTotalAssets, chainTxns,
		func(leases []*poolrpc.Lease) error {
			var sidecarLease *poolrpc.Lease

			for _, lease := range leases {
				lease := lease
				if bytes.Equal(lease.OrderNonce, bidNonce[:]) {
					sidecarLease = lease
					break
				}
			}

			if sidecarLease == nil {
				return fmt.Errorf("lease for sidecar channel " +
					"not found")
			}

			if !sidecarLease.SidecarChannel {
				return fmt.Errorf("lease is not detected as " +
					"sidecar channel")
			}

			if sidecarLease.SelfChanBalance != selfChanBalance {
				return fmt.Errorf("unexpected lease self "+
					"chan balance %d, wanted %d",
					sidecarLease.SelfChanBalance,
					selfChanBalance)
			}

			return nil
		},
	)
}

// localPendingBalanceCheck is a channel check predicate for making sure the
// local balance of a pending channel is correct.
func localPendingBalanceCheck(balance int64) pendingChanCheck {
	return func(c *lnrpc.PendingChannelsResponse_PendingChannel) error {
		if c.LocalBalance != balance {
			return fmt.Errorf("unexpected local balance %d, "+
				"wanted %d", c.LocalBalance, balance)
		}

		return nil
	}
}

// remotePendingBalanceCheck is a channel check predicate for making sure the
// remote balance of a pending channel is correct.
func remotePendingBalanceCheck(balance int64) pendingChanCheck {
	return func(c *lnrpc.PendingChannelsResponse_PendingChannel) error {
		if c.RemoteBalance != balance {
			return fmt.Errorf("unexpected remote balance %d, "+
				"wanted %d", c.RemoteBalance, balance)
		}

		return nil
	}
}

// localActiveBalanceCheck is a channel check predicate for making sure the
// local balance of an active channel is correct.
func localActiveBalanceCheck(balance int64) activeChanCheck {
	return func(c *lnrpc.Channel) error {
		if c.LocalBalance != balance {
			return fmt.Errorf("unexpected local balance %d, "+
				"wanted %d", c.LocalBalance, balance)
		}

		return nil
	}
}

// remoteActiveBalanceCheck is a channel check predicate for making sure the
// remote balance of an active channel is correct.
func remoteActiveBalanceCheck(balance int64) activeChanCheck {
	return func(c *lnrpc.Channel) error {
		if c.RemoteBalance != balance {
			return fmt.Errorf("unexpected remote balance %d, "+
				"wanted %d", c.RemoteBalance, balance)
		}

		return nil
	}
}
