package itest

import (
	"bytes"
	"context"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightningnetwork/lnd/lnrpc"
)

const (
	defaultAccountValue uint32 = 2_000_000
)

// testAccountCreation tests that the trader can successfully create an account
// on-chain and close it.
func testAccountCreation(t *harnessTest) {
	ctx := context.Background()

	// We need the current best block for the account expiry.
	_, currentHeight, err := t.lndHarness.Miner.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("could not query current block height: %v", err)
	}

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	account := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue:  defaultAccountValue,
		AccountExpiry: uint32(currentHeight) + 1000,
	})

	// Proceed to close it to a custom output where half of the account
	// value goes towards it and the rest towards fees.
	const outputValue = defaultAccountValue / 2
	ctxt, cancel := context.WithTimeout(ctx, defaultWaitTimeout)
	defer cancel()
	resp, err := t.trader.cfg.LndNode.NewAddress(ctxt, &lnrpc.NewAddressRequest{
		Type: lnrpc.AddressType_WITNESS_PUBKEY_HASH,
	})
	if err != nil {
		t.Fatalf("could not create new address: %v", err)
	}
	closeAddr := resp.Address
	closeTx := closeAccountAndAssert(t, t.trader, &clmrpc.CloseAccountRequest{
		TraderKey: account.TraderKey,
		Outputs: []*clmrpc.Output{
			{
				Value:   outputValue,
				Address: closeAddr,
			},
		},
	})

	// Ensure the transaction was crafted as expected.
	if len(closeTx.TxOut) != 1 {
		t.Fatalf("expected 1 output in close transaction, found %v",
			len(closeTx.TxOut))
	}
	if closeTx.TxOut[0].Value != int64(outputValue) {
		t.Fatalf("expected output value %v, found %v", outputValue,
			closeTx.TxOut[0].Value)
	}
	addr, err := btcutil.DecodeAddress(closeAddr, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("unable to decode address %v: %v", closeAddr, err)
	}
	outputScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatalf("unable to construct output script: %v", err)
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
	acct := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue:  2000000,
		AccountExpiry: uint32(currentHeight) + 1000,
	})
	tokenID, err := t.trader.server.GetIdentity()
	if err != nil {
		t.Fatalf("could not get the trader's identity: %v", err)
	}
	assertTraderSubscribed(t, *tokenID, acct)

	// Now that the trader is connected, let's shut down the auctioneer
	// server to simulate maintenance and see if the trader reconnects after
	// a while.
	t.restartServer()
	assertTraderSubscribed(t, *tokenID, acct)
}
