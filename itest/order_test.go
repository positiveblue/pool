package itest

import (
	"context"
	"time"

	"github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/stretchr/testify/require"
)

const (
	defaultOrderDuration uint32 = 2016
)

// testOrderSubmission tests that a simple ask order can be created on both the
// trader server and the auction server.
func testOrderSubmission(t *harnessTest) {
	ctx := context.Background()

	// Start by creating an account over 2M sats that is valid for the next
	// 1000 blocks.
	acct := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
	})

	// Now that the account is confirmed, submit an order over part of the
	// account balance. First, try with an invalid duration to check it is
	// enforced. The order shouldn't be stored.
	rpcAsk := &poolrpc.Ask{
		Details: &poolrpc.Order{
			TraderKey:               acct.TraderKey,
			RateFixed:               100,
			Amt:                     1500000,
			MaxBatchFeeRateSatPerKw: uint64(12500),
		},
		LeaseDurationBlocks: 365*144 + 1,
		Version:             uint32(order.CurrentVersion),
	}
	_, err := t.trader.SubmitOrder(ctx, &poolrpc.SubmitOrderRequest{
		Details: &poolrpc.SubmitOrderRequest_Ask{
			Ask: rpcAsk,
		},
	})
	require.Error(t.t, err)

	// Now try a correct one.
	rpcAsk.LeaseDurationBlocks = 2016
	ask, err := t.trader.SubmitOrder(ctx, &poolrpc.SubmitOrderRequest{
		Details: &poolrpc.SubmitOrderRequest_Ask{
			Ask: rpcAsk,
		},
	})
	require.NoError(t.t, err)
	require.NotNil(t.t, ask.GetAcceptedOrderNonce())
	require.Nil(t.t, ask.GetInvalidOrder())
	assertOrderEvents(
		t, t.trader, ask.GetAcceptedOrderNonce(), time.Now(), 0, 0,
	)

	// Now list all orders and validate order status.
	list, err := t.trader.ListOrders(ctx, &poolrpc.ListOrdersRequest{})
	require.NoError(t.t, err)
	require.Len(t.t, list.Asks, 1)
	require.Equal(
		t.t, poolrpc.OrderState_ORDER_SUBMITTED,
		list.Asks[0].Details.State,
	)

	// Next, we'll submit a Bid as well to test the other code paths.
	rpcBid := &poolrpc.Bid{
		Details: &poolrpc.Order{
			TraderKey:               acct.TraderKey,
			RateFixed:               100,
			Amt:                     1500000,
			MaxBatchFeeRateSatPerKw: uint64(12500),
		},
		LeaseDurationBlocks: 2016,
		Version:             uint32(order.CurrentVersion),
	}
	_, err = t.trader.SubmitOrder(ctx, &poolrpc.SubmitOrderRequest{
		Details: &poolrpc.SubmitOrderRequest_Bid{
			Bid: rpcBid,
		},
	})
	if err != nil {
		t.Fatalf("unable to submit order: %v", err)
	}

	// There should be two orders now, with one of them being an ask with the
	// proper state and node tier set.
	list, err = t.trader.ListOrders(ctx, &poolrpc.ListOrdersRequest{})
	require.NoError(t.t, err)
	require.Len(t.t, list.Bids, 1)
	require.Equal(
		t.t, poolrpc.OrderState_ORDER_SUBMITTED,
		list.Bids[0].Details.State,
	)

	// We didn't submit a default node tier, but this should have returned
	// the current default node tier.
	require.Equal(
		t.t, poolrpc.NodeTier_TIER_1,
		list.Bids[0].MinNodeTier,
	)

	// We'll cancel this bid, then submit another one with an explicit node
	// tier, this one should now match exactly.
	_, err = t.trader.CancelOrder(ctx, &poolrpc.CancelOrderRequest{
		OrderNonce: list.Bids[0].Details.OrderNonce,
	})
	require.NoError(t.t, err)

	// This time the bid will have an explicit node tier of 0, which means
	// they want to opt-out to all ratings.
	rpcBid = &poolrpc.Bid{
		Details: &poolrpc.Order{
			TraderKey:               acct.TraderKey,
			RateFixed:               100,
			Amt:                     1500000,
			MaxBatchFeeRateSatPerKw: uint64(12500),
		},
		LeaseDurationBlocks: 2016,
		Version:             uint32(order.CurrentVersion),
		MinNodeTier:         poolrpc.NodeTier_TIER_0,
	}
	_, err = t.trader.SubmitOrder(ctx, &poolrpc.SubmitOrderRequest{
		Details: &poolrpc.SubmitOrderRequest_Bid{
			Bid: rpcBid,
		},
	})
	require.NoError(t.t, err)

	// Grab the set of orders again, but this time the order that isn't
	// marked as cancelled should have the expected node tier.
	list, err = t.trader.ListOrders(ctx, &poolrpc.ListOrdersRequest{})
	require.NoError(t.t, err)
	require.Len(t.t, list.Bids, 2)

	var cancelIdx int
	for i, bid := range list.Bids {
		if bid.Details.State != poolrpc.OrderState_ORDER_SUBMITTED {
			continue
		}

		cancelIdx = i
		if bid.MinNodeTier != poolrpc.NodeTier_TIER_0 {
			t.Fatalf("wrong node tier, expected %v, got %v",
				poolrpc.NodeTier_TIER_0, bid.MinNodeTier)
		}
	}

	// To close, we'll the final ask, then query again to confirm that all
	// orders are now cancelled.
	_, err = t.trader.CancelOrder(ctx, &poolrpc.CancelOrderRequest{
		OrderNonce: list.Asks[0].Details.OrderNonce,
	})
	if err != nil {
		t.Fatalf("unable to cancel order: %v", err)
	}
	_, err = t.trader.CancelOrder(ctx, &poolrpc.CancelOrderRequest{
		OrderNonce: list.Bids[cancelIdx].Details.OrderNonce,
	})
	if err != nil {
		t.Fatalf("unable to cancel order: %v", err)
	}
	list, err = t.trader.ListOrders(ctx, &poolrpc.ListOrdersRequest{})
	if err != nil {
		t.Fatalf("could not list orders: %v", err)
	}
	for _, bid := range list.Bids {
		if bid.Details.State != poolrpc.OrderState_ORDER_CANCELED {
			t.Fatalf("bid not marked as canceled")
		}
	}
	for _, ask := range list.Asks {
		if ask.Details.State != poolrpc.OrderState_ORDER_CANCELED {
			t.Fatalf("ask not marked as canceled")
		}
	}
}
