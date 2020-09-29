package itest

import (
	"context"

	"github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/stretchr/testify/require"
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
		MaxDurationBlocks: 365*144 + 1,
		Version:           uint32(order.CurrentVersion),
	}
	_, err := t.trader.SubmitOrder(ctx, &poolrpc.SubmitOrderRequest{
		Details: &poolrpc.SubmitOrderRequest_Ask{
			Ask: rpcAsk,
		},
	})
	require.Error(t.t, err)

	// Now try a correct one.
	rpcAsk.MaxDurationBlocks = 2 * dayInBlocks
	ask, err := t.trader.SubmitOrder(ctx, &poolrpc.SubmitOrderRequest{
		Details: &poolrpc.SubmitOrderRequest_Ask{
			Ask: rpcAsk,
		},
	})
	require.NoError(t.t, err)
	require.NotNil(t.t, ask.GetAcceptedOrderNonce())
	require.NotNil(t.t, ask.GetInvalidOrder())

	// Now list all orders and validate order status.
	list, err := t.trader.ListOrders(ctx, &poolrpc.ListOrdersRequest{})
	require.NoError(t.t, err)
	require.Len(t.t, list.Asks, 1)
	require.Equal(
		t.t, poolrpc.OrderState_ORDER_SUBMITTED,
		list.Asks[0].Details.State,
	)
}
