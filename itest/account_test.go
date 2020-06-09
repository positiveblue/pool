package itest

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcutil"
	auctioneerAccount "github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightningnetwork/lnd/lnrpc"
	"google.golang.org/grpc/metadata"
)

const (
	defaultAccountValue uint64 = 2_000_000
	validTestAddr       string = "bcrt1qwajhg774mykrkz0nvqpxleqnl708pgvzkfuqm2"
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
				ValueSat: outputValue,
				Address:  closeAddr,
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

	// Make sure the default account limit is enforced on the auctioneer
	// side.
	_, err = t.trader.InitAccount(ctx, &clmrpc.InitAccountRequest{
		AccountValue:  uint64(btcutil.SatoshiPerBitcoin + 1),
		AccountExpiry: uint32(currentHeight) + 1000,
	})
	if err == nil {
		t.Fatalf("expected error when exceeding account value limit")
	}
	if !strings.Contains(err.Error(), "maximum account value") {
		t.Fatalf("unexpected error, got '%s' wanted '%s'", err.Error(),
			"maximum account value allowed is 1 BTC")
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
				ValueSat: withdrawValue,
				Address:  "bc1qvata6vu0eldas9qqm6qguflcf55x20exkzxujh",
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
	withdrawReq.Outputs[0].Address = validTestAddr
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

// testAccountDeposit tests that the auctioneer is able to handle a trader's
// request to deposit funds into an account.
func testAccountDeposit(t *harnessTest) {
	ctx := context.Background()

	// We need the current best block for the account expiry.
	_, currentHeight, err := t.lndHarness.Miner.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("could not query current block height: %v", err)
	}

	// Create an account for 500K sats that is valid for the next 1000
	// blocks and validate its confirmation on-chain.
	const initialAccountValue = 500_000
	account := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue:  initialAccountValue,
		AccountExpiry: uint32(currentHeight) + 1_000,
	})

	// With the account open, we'll now attempt to deposit the same amount
	// we initially funded the account with. The new value of the account
	// should therefore be twice that its initial value.
	const valueAfterDeposit = initialAccountValue * 2
	depositReq := &clmrpc.DepositAccountRequest{
		TraderKey:   account.TraderKey,
		AmountSat:   initialAccountValue,
		SatPerVbyte: 1,
	}
	depositResp, err := t.trader.DepositAccount(ctx, depositReq)
	if err != nil {
		t.Fatalf("unable to process account deposit: %v", err)
	}

	// We should expect to see the transaction causing the deposit.
	depositTxid, _ := chainhash.NewHash(depositResp.Account.Outpoint.Txid)
	txids, err := waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.Fatalf("deposit transaction not found in mempool: %v", err)
	}
	if !txids[0].IsEqual(depositTxid) {
		t.Fatalf("found mempool transaction %v instead of %v",
			txids[0], depositTxid)
	}

	// Assert that the account state is reflected correctly for both the
	// trader and auctioneer while the deposit hasn't confirmed.
	assertTraderAccount(
		t, depositResp.Account.TraderKey, valueAfterDeposit,
		clmrpc.AccountState_PENDING_UPDATE,
	)
	assertAuctioneerAccount(
		t, depositResp.Account.TraderKey, valueAfterDeposit,
		auctioneerAccount.StatePendingUpdate,
	)

	// Confirm the deposit, and once again assert that the account state
	// is reflected correctly.
	block := mineBlocks(t, t.lndHarness, 6, 1)[0]
	_ = assertTxInBlock(t, block, depositTxid)
	assertTraderAccount(
		t, depositResp.Account.TraderKey, valueAfterDeposit,
		clmrpc.AccountState_OPEN,
	)
	assertAuctioneerAccount(
		t, depositResp.Account.TraderKey, valueAfterDeposit,
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

// testServerAssistedAccountRecovery tests that a trader can recover all
// accounts with the help of the auctioneer in case they lose their local data
// directory. This assumes that the connected lnd instance still runs with the
// same seed the accounts originally were created with.
func testServerAssistedAccountRecovery(t *harnessTest) {
	ctxb := context.Background()

	// We need the current best block for the account expiry.
	_, currentHeight, err := t.lndHarness.Miner.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("could not query current block height: %v", err)
	}

	// We create three accounts. One that is closed again, one that remains
	// open and one that is pending open, waiting for on-chain confirmation.
	closed := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue:  2000000,
		AccountExpiry: uint32(currentHeight) + 1000,
	})
	closeAccountAndAssert(t, t.trader, &clmrpc.CloseAccountRequest{
		TraderKey: closed.TraderKey,
		Outputs: []*clmrpc.Output{{
			ValueSat: defaultAccountValue / 2,
			Address:  validTestAddr,
		}},
	})
	open := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue:  2000000,
		AccountExpiry: uint32(currentHeight) + 1000,
	})
	pending, err := t.trader.InitAccount(ctxb, &clmrpc.InitAccountRequest{
		AccountValue:  2000000,
		AccountExpiry: uint32(currentHeight) + 1000,
	})
	if err != nil {
		t.Fatalf("could not create account: %v", err)
	}
	_, err = waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.t.Fatalf("open tx not published in time: %v", err)
	}

	// Also create an order for the open account so we can make sure it'll
	// be canceled on recovery. We need to fetch the nonce of it so we can
	// query it directly.
	_, err = t.trader.SubmitOrder(ctxb, &clmrpc.SubmitOrderRequest{
		Details: &clmrpc.SubmitOrderRequest_Ask{
			Ask: &clmrpc.Ask{
				Details: &clmrpc.Order{
					TraderKey:      open.TraderKey,
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
	list, err := t.trader.ListOrders(ctxb, &clmrpc.ListOrdersRequest{})
	if err != nil {
		t.Fatalf("could not list orders: %v", err)
	}
	if len(list.Asks) != 1 {
		t.Fatalf("unexpected number of asks. got %d, expected %d",
			len(list.Asks), 1)
	}
	askNonce := list.Asks[0].Details.OrderNonce

	// Now we simulate data loss by shutting down the trader and removing
	// its data directory completely.
	err = t.trader.stop()
	if err != nil {
		t.Fatalf("could not stop trader: %v", err)
	}

	// Now we just create a new trader, connected to the same lnd instance.
	t.trader = setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, t.lndHarness.Bob, t.auctioneer,
	)

	// Make sure the trader doesn't remember any accounts anymore.
	accounts, err := t.trader.ListAccounts(
		ctxb, &clmrpc.ListAccountsRequest{},
	)
	if err != nil {
		t.Fatalf("could not query accounts: %v", err)
	}
	if len(accounts.Accounts) != 0 {
		t.Fatalf("unexpected number of accounts. got %d wanted %d",
			len(accounts.Accounts), 0)
	}

	// Start the recovery process. We expect three accounts to be recovered.
	recovery, err := t.trader.RecoverAccounts(
		ctxb, &clmrpc.RecoverAccountsRequest{},
	)
	if err != nil {
		t.Fatalf("could not recover accounts: %v", err)
	}
	if recovery.NumRecoveredAccounts != 3 {
		t.Fatalf("unexpected number of recovered accounts. got %d "+
			"wanted %d", recovery.NumRecoveredAccounts, 3)
	}

	// Now make sure the accounts are all in the correct state.
	accounts, err = t.trader.ListAccounts(
		ctxb, &clmrpc.ListAccountsRequest{},
	)
	if err != nil {
		t.Fatalf("could not query accounts: %v", err)
	}
	if len(accounts.Accounts) != 3 {
		t.Fatalf("unexpected number of accounts. got %d wanted %d",
			len(accounts.Accounts), 3)
	}
	assertTraderAccountState(
		t.t, t.trader, closed.TraderKey, clmrpc.AccountState_CLOSED,
	)
	assertTraderAccountState(
		t.t, t.trader, open.TraderKey, clmrpc.AccountState_OPEN,
	)
	assertTraderAccountState(
		t.t, t.trader, pending.TraderKey,
		clmrpc.AccountState_PENDING_OPEN,
	)

	// Mine the rest of the blocks to make the pending account fully
	// confirmed. Then check its state again.
	_ = mineBlocks(t, t.lndHarness, 5, 0)
	assertTraderAccountState(
		t.t, t.trader, pending.TraderKey, clmrpc.AccountState_OPEN,
	)

	// Finally, make sure we can close out both open accounts.
	closeAccountAndAssert(t, t.trader, &clmrpc.CloseAccountRequest{
		TraderKey: open.TraderKey,
		Outputs: []*clmrpc.Output{{
			ValueSat: defaultAccountValue / 2,
			Address:  validTestAddr,
		}},
	})
	closeAccountAndAssert(t, t.trader, &clmrpc.CloseAccountRequest{
		TraderKey: pending.TraderKey,
		Outputs: []*clmrpc.Output{{
			ValueSat: defaultAccountValue / 2,
			Address:  validTestAddr,
		}},
	})

	// Query the auctioneer directly about the status of the ask we
	// submitted earlier.
	tokenID, err := t.trader.server.GetIdentity()
	if err != nil {
		t.Fatalf("could not get the trader's identity: %v", err)
	}
	idStr := fmt.Sprintf("LSATID %x", tokenID)
	idCtx := metadata.AppendToOutgoingContext(
		ctxb, auth.HeaderAuthorization, idStr,
	)
	resp, err := t.auctioneer.OrderState(
		idCtx, &clmrpc.ServerOrderStateRequest{OrderNonce: askNonce},
	)
	if err != nil {
		t.Fatalf("could not query order status: %v", err)
	}
	if resp.State != clmrpc.OrderState_ORDER_CANCELED {
		t.Fatalf("unexpected order state, got %d wanted %d",
			resp.State, clmrpc.OrderState_ORDER_CANCELED)
	}
}
