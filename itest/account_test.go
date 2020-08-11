package itest

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/llm/clmrpc"
	"github.com/lightninglabs/llm/clmscript"
	"github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/loop/lsat"
	auctioneerAccount "github.com/lightninglabs/subasta/account"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
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

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	account := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
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
		AccountValue: uint64(btcutil.SatoshiPerBitcoin + 1),
		AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
		Fees: &clmrpc.InitAccountRequest_ConfTarget{ConfTarget: 6},
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

	// Create an account for 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	account := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
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
		FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
	}
	_, err := t.trader.WithdrawAccount(ctx, withdrawReq)
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

	// Create an account for 500K sats that is valid for the next 1000
	// blocks and validate its confirmation on-chain.
	const initialAccountValue = 500_000
	account := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue: initialAccountValue,
		AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
	})

	// With the account open, we'll now attempt to deposit the same amount
	// we initially funded the account with. The new value of the account
	// should therefore be twice that its initial value.
	const valueAfterDeposit = initialAccountValue * 2
	depositReq := &clmrpc.DepositAccountRequest{
		TraderKey:       account.TraderKey,
		AmountSat:       initialAccountValue,
		FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
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
	// Create an account over 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	acct := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue: 2000000,
		AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
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
	tokenID, err := t.trader.server.GetIdentity()
	if err != nil {
		t.Fatalf("could not get the trader's identity: %v", err)
	}
	idCtx := getTokenContext(tokenID)

	const defaultExpiration uint32 = 1_000

	// We create three full accounts. One that is closed again, one
	// that remains open and one that is pending open, waiting for on-chain
	// confirmation.
	closed := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: defaultExpiration,
		},
	})
	closeAccountAndAssert(t, t.trader, &clmrpc.CloseAccountRequest{
		TraderKey: closed.TraderKey,
		Outputs: []*clmrpc.Output{{
			ValueSat: defaultAccountValue / 2,
			Address:  validTestAddr,
		}},
	})
	open := openAccountAndAssert(t, t.trader, &clmrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: defaultExpiration,
		},
	})
	pending, err := t.trader.InitAccount(ctxb, &clmrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &clmrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: defaultExpiration,
		},
		Fees: &clmrpc.InitAccountRequest_ConfTarget{ConfTarget: 6},
	})
	if err != nil {
		t.Fatalf("could not create account: %v", err)
	}
	_, err = waitForNTxsInMempool(
		t.lndHarness.Miner.Node, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.Fatalf("open tx not published in time: %v", err)
	}

	// Also create an order for the open account so we can make sure it'll
	// be canceled on recovery. We need to fetch the nonce of it so we can
	// query it directly.
	_, err = t.trader.SubmitOrder(ctxb, &clmrpc.SubmitOrderRequest{
		Details: &clmrpc.SubmitOrderRequest_Ask{
			Ask: &clmrpc.Ask{
				Details: &clmrpc.Order{
					TraderKey:               open.TraderKey,
					RateFixed:               100,
					Amt:                     1500000,
					MaxBatchFeeRateSatPerKw: uint64(12500),
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

	// Now we also create two reservations. One we send funds to, the other
	// we don't. The trader won't know of any of them but when recovering
	// will still try to recover them. We need to use a dummy token for the
	// first one, otherwise we couldn't register the second one.
	resRecoveryFailed := addReservation(
		getTokenContext(&lsat.TokenID{0x02}), t, t.lndHarness.Bob,
		defaultAccountValue, defaultExpiration, false,
	)
	resRecoveryOk := addReservation(
		idCtx, t, t.lndHarness.Bob,
		defaultAccountValue, defaultExpiration, true,
	)

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

	// Start the recovery process. We expect four accounts to be recovered
	// even though there were 5 accounts. One of them isn't counted because
	// it should be in the database marked with the state of recovery
	// failure.
	recovery, err := t.trader.RecoverAccounts(
		ctxb, &clmrpc.RecoverAccountsRequest{},
	)
	if err != nil {
		t.Fatalf("could not recover accounts: %v", err)
	}
	if recovery.NumRecoveredAccounts != 4 {
		t.Fatalf("unexpected number of recovered accounts. got %d "+
			"wanted %d", recovery.NumRecoveredAccounts, 4)
	}

	// Now make sure the accounts are all in the correct state.
	accounts, err = t.trader.ListAccounts(
		ctxb, &clmrpc.ListAccountsRequest{},
	)
	if err != nil {
		t.Fatalf("could not query accounts: %v", err)
	}
	if len(accounts.Accounts) != 5 {
		t.Fatalf("unexpected number of accounts. got %d wanted %d",
			len(accounts.Accounts), 5)
	}
	assertTraderAccountState(
		t.t, t.trader, resRecoveryFailed,
		clmrpc.AccountState_RECOVERY_FAILED,
	)
	assertTraderAccountState(
		t.t, t.trader, resRecoveryOk, clmrpc.AccountState_PENDING_OPEN,
	)
	assertTraderAccountState(
		t.t, t.trader, closed.TraderKey, clmrpc.AccountState_CLOSED,
	)
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

	// Mine the rest of the blocks to make the pending accounts fully
	// confirmed. Then check their state again.
	_ = mineBlocks(t, t.lndHarness, 5, 0)
	assertTraderAccountState(
		t.t, t.trader, pending.TraderKey, clmrpc.AccountState_OPEN,
	)
	assertTraderAccountState(
		t.t, t.trader, resRecoveryOk, clmrpc.AccountState_OPEN,
	)

	// Finally, make sure we can close out all open accounts.
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
	closeAccountAndAssert(t, t.trader, &clmrpc.CloseAccountRequest{
		TraderKey: resRecoveryOk,
		Outputs: []*clmrpc.Output{{
			ValueSat: defaultAccountValue / 2,
			Address:  validTestAddr,
		}},
	})

	// Query the auctioneer directly about the status of the ask we
	// submitted earlier.
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

func addReservation(lsatCtx context.Context, t *harnessTest,
	node *lntest.HarnessNode, value uint64, expiry uint32,
	sendFunds bool) []byte {

	ctxb := context.Background()

	// Derive a new key for the reserved account so the trader will try to
	// recover with it.
	keyDesc, err := node.WalletKitClient.DeriveNextKey(
		ctxb, &walletrpc.KeyReq{
			KeyFamily: int32(clmscript.AccountKeyFamily),
		},
	)
	if err != nil {
		t.Fatalf("could not derive key for reservation: %v", err)
	}

	// Reserve the account with the auctioneer now and parse the returned
	// keys so we can derive the account script later.
	res, err := t.auctioneer.ReserveAccount(
		lsatCtx, &clmrpc.ReserveAccountRequest{
			AccountValue:  value,
			TraderKey:     keyDesc.RawKeyBytes,
			AccountExpiry: expiry,
		},
	)
	if err != nil {
		t.Fatalf("could not reserve account: %v", err)
	}
	traderKey, err := btcec.ParsePubKey(keyDesc.RawKeyBytes, btcec.S256())
	if err != nil {
		t.Fatalf("could not parse trader key: %v", err)
	}
	auctioneerKey, err := btcec.ParsePubKey(res.AuctioneerKey, btcec.S256())
	if err != nil {
		t.Fatalf("could not parse auctioneer key: %v", err)
	}
	batchKey, err := btcec.ParsePubKey(res.InitialBatchKey, btcec.S256())
	if err != nil {
		t.Fatalf("could not parse batch key: %v", err)
	}

	// To know the script we need to get the derived secret. Unfortunately
	// the signer RPC of the lnd harness node isn't exposed so we have to
	// open a new connection for that.
	//
	// TODO(guggero): Expose signer client in lnd test harness.
	conn, err := node.ConnectRPC(true)
	if err != nil {
		t.Fatalf("could not connect to node RPC: %v", err)
	}
	signer := signrpc.NewSignerClient(conn)
	keyRes, err := signer.DeriveSharedKey(ctxb, &signrpc.SharedKeyRequest{
		EphemeralPubkey: res.AuctioneerKey,
		KeyLoc:          keyDesc.KeyLoc,
	})
	if err != nil {
		t.Fatalf("could not derive shared key: %v", err)
	}

	var sharedKey [32]byte
	copy(sharedKey[:], keyRes.SharedKey)
	script, err := clmscript.AccountScript(
		expiry, traderKey, auctioneerKey, batchKey, sharedKey,
	)
	if err != nil {
		t.Fatalf("could not derive account script: %v", err)
	}

	if !sendFunds {
		return keyDesc.RawKeyBytes
	}

	_, err = t.lndHarness.Bob.WalletKitClient.SendOutputs(
		ctxb, &walletrpc.SendOutputsRequest{
			Outputs: []*signrpc.TxOut{{
				Value:    int64(value),
				PkScript: script,
			}},
			SatPerKw: 300,
		},
	)
	if err != nil {
		t.Fatalf("could not send to reserved account: %v", err)
	}

	return keyDesc.RawKeyBytes
}

func getTokenContext(token *lsat.TokenID) context.Context {
	return metadata.AppendToOutgoingContext(
		context.Background(), auth.HeaderAuthorization,
		fmt.Sprintf("LSATID %x", token[:]),
	)
}
