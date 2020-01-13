package itest

import (
	"context"

	"github.com/lightninglabs/agora/client/clmrpc"
)

// testAccountCreation tests that the trader can successfully create an account
// on-chain and confirm it.
func testAccountCreation(t *harnessTest) {
	var ctx = context.Background()

	// We need the current best block for the account expiry.
	_, currentHeight, err := t.lndHarness.Miner.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("could not query current block height: %v", err)
	}

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	_ = openAccountAndAssert(
		ctx, t, t.lndHarness, t.trader, &clmrpc.InitAccountRequest{
			AccountValue:  2000000,
			AccountExpiry: uint32(currentHeight) + 1000,
		},
	)
}
