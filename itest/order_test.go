package itest

import (
	"context"

	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightninglabs/agora/client/order"
)

const (
	dayInBlocks int64 = 144
)

// testOrderSubmission tests that a simple ask order can be created on both the
// trader server and the auction server.
func testOrderSubmission(t *harnessTest) {
	ctx := context.Background()

	// We need the current best block for the account expiry.
	_, currentHeight, err := t.lndHarness.Miner.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("could not query current block height: %v", err)
	}

	// Start by creating an account over 2M sats that is valid for the next
	// 1000 blocks.
	acct := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue:  defaultAccountValue,
		AccountExpiry: uint32(currentHeight) + 1000,
	})

	// Now that the account is confirmed, submit an order over part of the
	// account balance.
	ask, err := t.trader.SubmitOrder(ctx, &clmrpc.SubmitOrderRequest{
		Details: &clmrpc.SubmitOrderRequest_Ask{
			Ask: &clmrpc.Ask{
				Details: &clmrpc.Order{
					UserSubKey:     acct.TraderKey,
					RateFixed:      100,
					Amt:            1500000,
					FundingFeeRate: 0,
				},
				MaxDurationBlocks: 2 * dayInBlocks,
				Version:           uint32(order.CurrentVersion),
			},
		},
	})
	if err != nil {
		t.Fatalf("could not submit order: %v", err)
	}
	switch details := ask.Details.(type) {
	case *clmrpc.SubmitOrderResponse_AcceptedOrderNonce:
		// Great, order accepted.

	case *clmrpc.SubmitOrderResponse_InvalidOrder:
		t.Fatalf("order submission failed: %v", details)

	default:
		t.Fatalf("unknown response in order submission: %v", details)
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
	if list.Asks[0].Details.State != "submitted" {
		t.Fatalf("unexpected account state. got %s, expected %s",
			list.Asks[0].Details.State, "submitted")
	}
}
