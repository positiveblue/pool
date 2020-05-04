package itest

import (
	"context"
	"time"

	"github.com/lightninglabs/agora/adminrpc"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightninglabs/agora/client/order"
)

func testBatchExecution(t *harnessTest) {
	ctx := context.Background()

	// We need the current best block for the account expiry.
	_, currentHeight, err := t.lndHarness.Miner.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("could not query current block height: %v", err)
	}

	// We need a third lnd node, Charlie that is used for the second trader.
	charlie, err := t.lndHarness.NewNode("charlie", nil)
	if err != nil {
		t.Fatalf("unable to set up charlie: %v", err)
	}
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	if err != nil {
		t.Fatalf("unable to send coins to carol: %v", err)
	}

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders.
	account1 := openAccountAndAssert(
		t, t.trader, &clmrpc.InitAccountRequest{
			AccountValue:  defaultAccountValue,
			AccountExpiry: uint32(currentHeight) + 1000,
		},
	)
	account2 := openAccountAndAssert(
		t, secondTrader, &clmrpc.InitAccountRequest{
			AccountValue:  defaultAccountValue,
			AccountExpiry: uint32(currentHeight) + 1000,
		},
	)

	// Now that the accounts are confirmed, submit an ask order from our
	// default trader, selling 15 units (1.5M sats) of liquidity.
	_, err = t.trader.SubmitOrder(ctx, &clmrpc.SubmitOrderRequest{
		Details: &clmrpc.SubmitOrderRequest_Ask{
			Ask: &clmrpc.Ask{
				Details: &clmrpc.Order{
					UserSubKey:     account1.TraderKey,
					RateFixed:      100,
					Amt:            1_500_000,
					FundingFeeRate: 0,
				},
				MaxDurationBlocks: 2 * dayInBlocks,
				Version:           uint32(order.CurrentVersion),
			},
		},
	})
	if err != nil {
		t.Fatalf("could not submit ask order: %v", err)
	}

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity. So let's submit an order for that.
	_, err = secondTrader.SubmitOrder(ctx, &clmrpc.SubmitOrderRequest{
		Details: &clmrpc.SubmitOrderRequest_Bid{
			Bid: &clmrpc.Bid{
				Details: &clmrpc.Order{
					UserSubKey:     account2.TraderKey,
					RateFixed:      100,
					Amt:            800_000,
					FundingFeeRate: 0,
				},
				MinDurationBlocks: 1 * dayInBlocks,
				Version:           uint32(order.CurrentVersion),
			},
		},
	})
	if err != nil {
		t.Fatalf("could not submit ask order: %v", err)
	}

	// Let's kick the auctioneer now to try and create a batch.
	_, err = t.auctioneer.AuctionAdminClient.BatchTick(
		ctx, &adminrpc.EmptyRequest{},
	)
	if err != nil {
		t.Fatalf("could not trigger batch tick: %v", err)
	}

	// TODO(guggero): Finish test logic once #66 has landed.
	time.Sleep(2 * time.Second)
}
