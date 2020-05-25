package itest

import (
	"bytes"
	"context"
	"strings"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcutil"
	auctioneerAccount "github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightningnetwork/lnd/lnrpc"
)

const (
	defaultAccountValue uint64 = 2_000_000
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

// testAccountWithdrawal tests that the auctioneer is able to handle a trader's
// request to withdraw funds from an account.
func testAccountWithdrawal(t *harnessTest) {
	ctx := context.Background()

	// We need the current best block for the account expiry.
	_, currentHeight, err := t.lndHarness.Miner.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("could not query current block height: %v", err)
	}

	// Create an account for 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	account := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue:  defaultAccountValue,
		AccountExpiry: uint32(currentHeight) + 1_000,
	})

	// With the account open, we'll now attempt to withdraw half of the
	// funds we committed to a P2WPKH address. We'll first try an address
	// that does not belong to the current network, which should fail.
	withdrawValue := account.Value / 2
	withdrawReq := &clmrpc.WithdrawAccountRequest{
		TraderKey: account.TraderKey,
		Outputs: []*clmrpc.Output{
			{
				Value:   withdrawValue,
				Address: "bc1qvata6vu0eldas9qqm6qguflcf55x20exkzxujh",
			},
		},
		SatPerVbyte: 1,
	}
	_, err = t.trader.WithdrawAccount(ctx, withdrawReq)
	isInvalidAddrErr := err != nil &&
		strings.Contains(err.Error(), "invalid address")
	if err == nil || !isInvalidAddrErr {
		t.Fatalf("expected invalid address error, got %v", err)
	}

	// Now try a valid address.
	withdrawReq.Outputs[0].Address = "bcrt1qwajhg774mykrkz0nvqpxleqnl708pgvzkfuqm2"
	withdrawResp, err := t.trader.WithdrawAccount(ctx, withdrawReq)
	if err != nil {
		t.Fatalf("unable to process account withdrawal: %v", err)
	}

	// We should expect to see the transaction causing the withdrawal.
	withdrawTxid, _ := chainhash.NewHash(withdrawResp.Account.Outpoint.Txid)
	txids, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.Fatalf("withdrawal transaction not found in mempool: %v", err)
	}
	if !txids[0].IsEqual(withdrawTxid) {
		t.Fatalf("found mempool transaction %v instead of %v",
			txids[0], withdrawTxid)
	}

	// Assert that the account state is reflected correctly for both the
	// trader and auctioneer while the withdrawal hasn't confirmed.
	const withdrawalFee = 184
	valueAfterWithdrawal := btcutil.Amount(account.Value) -
		btcutil.Amount(withdrawValue) - withdrawalFee
	assertTraderAccount(
		t, withdrawResp.Account.TraderKey, valueAfterWithdrawal,
		clmrpc.AccountState_PENDING_UPDATE,
	)
	assertAuctioneerAccount(
		t, withdrawResp.Account.TraderKey, valueAfterWithdrawal,
		auctioneerAccount.StatePendingUpdate,
	)

	// Confirm the withdrawal, and once again assert that the account state
	// is reflected correctly.
	block := mineBlocks(t, t.lndHarness, 6, 1)[0]
	_ = assertTxInBlock(t, block, withdrawTxid)
	assertTraderAccount(
		t, withdrawResp.Account.TraderKey, valueAfterWithdrawal,
		clmrpc.AccountState_OPEN,
	)
	assertAuctioneerAccount(
		t, withdrawResp.Account.TraderKey, valueAfterWithdrawal,
		auctioneerAccount.StateOpen,
	)

	// Finally, end the test by closing the account.
	_ = closeAccountAndAssert(t, t.trader, &clmrpc.CloseAccountRequest{
		TraderKey: account.TraderKey,
	})
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
