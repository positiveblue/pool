package itest

import (
	"context"

	"github.com/lightninglabs/llm/clmrpc"
	"github.com/lightninglabs/llm/order"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

const (
	dayInBlocks uint32 = 144
)

// testOrderSubmission tests that a simple ask order can be created on both the
// trader server and the auction server.
func testOrderSubmission(t *harnessTest) {
	ctx := context.Background()

	// Start by creating an account over 2M sats that is valid for the next
	// 1000 blocks.
	acct := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
	})

	// Now that the account is confirmed, submit an order over part of the
	// account balance. First, try with an invalid duration to check it is
	// enforced. The order shouldn't be stored.
	rpcAsk := &clmrpc.Ask{
		Details: &clmrpc.Order{
			TraderKey:               acct.TraderKey,
			RateFixed:               100,
			Amt:                     1500000,
			MaxBatchFeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
		},
		MaxDurationBlocks: 365*144 + 1,
		Version:           uint32(order.CurrentVersion),
	}
	_, err := t.trader.SubmitOrder(ctx, &clmrpc.SubmitOrderRequest{
		Details: &clmrpc.SubmitOrderRequest_Ask{
			Ask: rpcAsk,
		},
	})
	if err == nil {
		t.Fatalf("expected invalid order to fail but err was nil")
	}

	// Now try a correct one.
	rpcAsk.MaxDurationBlocks = 2 * dayInBlocks
	ask, err := t.trader.SubmitOrder(ctx, &clmrpc.SubmitOrderRequest{
		Details: &clmrpc.SubmitOrderRequest_Ask{
			Ask: rpcAsk,
		},
	})
	if err != nil {
		t.Fatalf("could not submit order: %v", err)
	}
	if ask.GetAcceptedOrderNonce() == nil || ask.GetInvalidOrder() != nil {
		t.Fatalf("order submission failed: %v", ask)
	}

	// Now list all orders and validate order status.
	list, err := t.trader.ListOrders(ctx, &clmrpc.ListOrdersRequest{})
	if err != nil {
		t.Fatalf("could not list orders: %v", err)
	}
	if len(list.Asks) != 1 {
		t.Fatalf("unexpected number of asks. got %d, expected %d",
			len(list.Asks), 1)
	}
	if list.Asks[0].Details.State != clmrpc.OrderState_ORDER_SUBMITTED {
		t.Fatalf("unexpected account state. got %v, expected %v",
			list.Asks[0].Details.State,
			clmrpc.OrderState_ORDER_SUBMITTED)
	}
}
