package itest

import (
	"bytes"
	"encoding/hex"

	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightninglabs/loop/lsat"
)

var (
	zeroID lsat.TokenID
)

// testAccountCreation tests that the trader can successfully create an account
// on-chain and close it.
func testAccountCreation(t *harnessTest) {
	// We need the current best block for the account expiry.
	_, currentHeight, err := t.lndHarness.Miner.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("could not query current block height: %v", err)
	}

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	const accountValue = 2000000
	account := openAccountAndAssert(t, &clmrpc.InitAccountRequest{
		AccountValue:  accountValue,
		AccountExpiry: uint32(currentHeight) + 1000,
	})

	// Proceed to close it to a custom output where half of the account
	// value goes towards it and the rest towards fees.
	const outputValue = accountValue / 2
	outputScript, _ := hex.DecodeString(
		"00203d626e5ad72f78b884333f7db7c612eb448fae27307abe0b27098aab036cb5a7",
	)
	closeTx := closeAccountAndAssert(t, &clmrpc.CloseAccountRequest{
		TraderKey: account.TraderKey,
		Outputs: []*clmrpc.Output{
			{
				Value:  outputValue,
				Script: outputScript,
			},
		},
	})

	// Ensure the transaction was crafted as expected.
	if len(closeTx.TxOut) != 1 {
		t.Fatalf("expected 1 output in close transaction, found %v",
			len(closeTx.TxOut))
	}
	if closeTx.TxOut[0].Value != outputValue {
		t.Fatalf("expected output value %v, found %v", outputValue,
			closeTx.TxOut[0].Value)
	}
	if !bytes.Equal(closeTx.TxOut[0].PkScript, outputScript) {
		t.Fatalf("expected output script %x, found %x", outputScript,
			closeTx.TxOut[0].PkScript)
	}
}

// testAccountSubscription tests that a trader registers for an account after
// opening one and that the reconnection mechanism works if the server is
// stopped for maintenance.
func testAccountSubscription(t *harnessTest) {
	// We need the current best block for the account expiry.
	_, currentHeight, err := t.lndHarness.Miner.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("could not query current block height: %v", err)
	}

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	acct := openAccountAndAssert(t, &clmrpc.InitAccountRequest{
		AccountValue:  2000000,
		AccountExpiry: uint32(currentHeight) + 1000,
	})
	assertTraderSubscribed(t, zeroID, acct)

	// Now that the trader is connected, let's shut down the auctioneer
	// server to simulate maintenance and see if the trader reconnects after
	// a while.
	t.restartServer()
	assertTraderSubscribed(t, zeroID, acct)
}
