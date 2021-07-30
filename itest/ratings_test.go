package itest

import (
	"context"

	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/stretchr/testify/require"
)

// testNodeRatingAgencyAndMatching tests that we're able to properly
// incorporate the ratings agency into match making, as well a query/modify the
// set of ratings for a given node.
func testNodeRatingAgencyAndMatching(t *harnessTest) {
	ctx := context.Background()

	// We'll start by creating two fresh node: Charlie and Dave who will be
	// creating accounts shortly in our new market.
	charlie := t.lndHarness.NewNode(t.t, "charlie", nil)
	charlieTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, charlieTrader)
	t.lndHarness.SendCoins(ctx, t.t, 5_000_000, charlie)

	dave := t.lndHarness.NewNode(t.t, "dave", nil)
	daveTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, dave, t.auctioneer,
	)
	defer shutdownAndAssert(t, dave, daveTrader)
	t.lndHarness.SendCoins(ctx, t.t, 5_000_000, dave)

	// Next, we'll have both of them open a new account.
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

	// At this point, if we query for the node ratings for both nodes, we
	// should find that they're both on the lowest tier.
	nodeRatings, err := charlieTrader.NodeRatings(ctx, &poolrpc.NodeRatingRequest{
		NodePubkeys: [][]byte{charlie.PubKey[:], dave.PubKey[:]},
	})
	require.NoError(t.t, err)

	for _, nodeRating := range nodeRatings.NodeRatings {
		require.Equal(t.t, nodeRating.NodeTier, auctioneerrpc.NodeTier_TIER_0)
	}

	// Now that we have accounts open for both parties, we'll start to
	// submit some orders ensuring that they'll be able to match (ignoring
	// node ratings).
	const (
		askSize        = 600_000
		askRate        = 2000
		bidSize        = 300_000
		durationBlocks = 2016
	)

	// Submit an ask order that is large enough to be matched multiple
	// times.
	_, err = submitAskOrder(
		charlieTrader, charlieAccount.TraderKey, askRate, askSize,
		func(ask *poolrpc.SubmitOrderRequest_Ask) {
			ask.Ask.LeaseDurationBlocks = durationBlocks
		},
	)
	require.NoError(t.t, err)

	// Next, we'll submit a bid order, but by NOT specifying a node tier,
	// we'll be opting into only the highest tier.
	_, err = submitBidOrder(
		daveTrader, daveAccount.TraderKey, askRate, bidSize,
		func(bid *poolrpc.SubmitOrderRequest_Bid) {
			bid.Bid.LeaseDurationBlocks = durationBlocks
			bid.Bid.MinNodeTier = 0
		},
	)
	require.NoError(t.t, err)

	// If we try to clear a batch now, we should find that no batch is
	// possible since Charlie (the one with the ask order) is still in the
	// base tier.
	expectNoPossibleMarket(t)

	// Now we'll modify the rating of Charlie's LN node to reside in the
	// next highest node tier.
	_, err = t.auctioneer.ModifyNodeRatings(ctx, &adminrpc.ModifyRatingRequest{
		NodeKey:     charlie.PubKey[:],
		NewNodeTier: uint32(order.NodeTier1),
	})
	require.NoError(t.t, err)

	// We'll now re-run match making, and we should find that the two
	// orders above were executed.
	_, batchTXIDs := executeBatch(t, 1)
	batchTXID := batchTXIDs[0]

	// Mine enough blocks to confirm the batch, then confirm the channel
	// fully.
	blocks := mineBlocks(t, t.lndHarness, 1, 1)
	assertTxInBlock(t, blocks[0], batchTXID)
	_ = mineBlocks(t, t.lndHarness, 3, 0)

	// Now that we're done here, we'll close these channels to ensure that
	// all the created nodes have a clean state after this test execution.
	closeAllChannels(ctx, t, charlie)
}
