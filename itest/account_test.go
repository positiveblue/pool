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
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/pool/poolscript"
	auctioneerAccount "github.com/lightninglabs/subasta/account"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
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
	account := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
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
	closeTx := closeAccountAndAssert(t, t.trader, &poolrpc.CloseAccountRequest{
		TraderKey: account.TraderKey,
		FundsDestination: &poolrpc.CloseAccountRequest_Outputs{
			Outputs: &poolrpc.OutputsWithImplicitFee{
				Outputs: []*poolrpc.Output{
					{
						ValueSat: outputValue,
						Address:  closeAddr,
					},
				},
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
	_, err = t.trader.InitAccount(ctx, &poolrpc.InitAccountRequest{
		AccountValue: uint64(11 * btcutil.SatoshiPerBitcoin),
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
		Fees: &poolrpc.InitAccountRequest_ConfTarget{ConfTarget: 6},
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
	account := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
	})

	// With the account open, we'll now attempt to withdraw half of the
	// funds we committed to a P2WPKH address. We'll first try an address
	// that does not belong to the current network, which should fail.
	withdrawValue := account.Value / 2
	withdrawReq := &poolrpc.WithdrawAccountRequest{
		TraderKey: account.TraderKey,
		Outputs: []*poolrpc.Output{
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
	withdrawTxid, valueAfterWithdrawal := withdrawAccountAndAssertMempool(
		t, t.trader, account.TraderKey, int64(account.Value),
		withdrawValue, validTestAddr,
	)

	// We'll attempt to bump the fee rate of the withdrawal from 1 sat/vbyte
	// to 10 sat/vbyte. The withdrawal transaction doesn't contain an output
	// under the backing lnd node's control, so the call should fail.
	_, err = t.trader.BumpAccountFee(ctx, &poolrpc.BumpAccountFeeRequest{
		TraderKey:       account.TraderKey,
		FeeRateSatPerKw: withdrawReq.FeeRateSatPerKw * 250 * 10,
	})
	if err == nil || !strings.Contains(err.Error(), "eligible outputs") {
		t.Fatalf("expected BumpAccountFee to fail on account "+
			"transaction without eligible outputs, got err=%v", err)
	}

	// Confirm the withdrawal, and once again assert that the account state
	// is reflected correctly.
	block := mineBlocks(t, t.lndHarness, 6, 1)[0]
	_ = assertTxInBlock(t, block, withdrawTxid)
	assertTraderAccount(
		t, t.trader, account.TraderKey, valueAfterWithdrawal,
		account.ExpirationHeight, poolrpc.AccountState_OPEN,
	)
	assertAuctioneerAccount(
		t, account.TraderKey, valueAfterWithdrawal,
		auctioneerAccount.StateOpen,
	)

	// Finally, end the test by closing the account.
	_ = closeAccountAndAssert(t, t.trader, &poolrpc.CloseAccountRequest{
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
	account := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: initialAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
	})

	// With the account open, we'll now attempt to deposit the same amount
	// we initially funded the account with. The new value of the account
	// should therefore be twice that its initial value.
	const valueAfterDeposit = initialAccountValue * 2
	depositReq := &poolrpc.DepositAccountRequest{
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
		t.lndHarness.Miner.Client, 1, minerMempoolTimeout,
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
		t, t.trader, depositResp.Account.TraderKey, valueAfterDeposit,
		account.ExpirationHeight, poolrpc.AccountState_PENDING_UPDATE,
	)
	assertAuctioneerAccount(
		t, depositResp.Account.TraderKey, valueAfterDeposit,
		auctioneerAccount.StatePendingUpdate,
	)

	// We'll assume the fee rate wasn't enough for the deposit to confirm,
	// so we'll attempt to bump it from 1 sat/vbyte to 10 sat/vbyte. We
	// should then see two transactions in the mempool.
	_, err = t.trader.BumpAccountFee(ctx, &poolrpc.BumpAccountFeeRequest{
		TraderKey:       account.TraderKey,
		FeeRateSatPerKw: depositReq.FeeRateSatPerKw * 250 * 10,
	})
	if err != nil {
		t.Fatalf("unable to bump account fee: %v", err)
	}
	_, err = waitForNTxsInMempool(
		t.lndHarness.Miner.Client, 2, minerMempoolTimeout,
	)
	if err != nil {
		t.Fatalf("deposit and bump transaction not found in mempool: %v",
			err)
	}

	// Confirm the deposit, and once again assert that the account state
	// is reflected correctly.
	block := mineBlocks(t, t.lndHarness, 6, 2)[0]
	_ = assertTxInBlock(t, block, depositTxid)
	assertTraderAccount(
		t, t.trader, depositResp.Account.TraderKey, valueAfterDeposit,
		account.ExpirationHeight, poolrpc.AccountState_OPEN,
	)
	assertAuctioneerAccount(
		t, depositResp.Account.TraderKey, valueAfterDeposit,
		auctioneerAccount.StateOpen,
	)

	// We'll then attempt a deposit using a NP2WKH input. To do so, we'll
	// need a NP2WKH available to spend. We send over 4 BTC and will attempt
	// a 3 BTC deposit to ensure the NP2WKH input is chosen.
	t.lndHarness.SendCoinsNP2WKH(
		t.t, btcutil.SatoshiPerBitcoin*4, t.trader.cfg.LndNode,
	)
	depositReq.AmountSat = btcutil.SatoshiPerBitcoin * 3
	valueAfterSecondDeposit := btcutil.Amount(
		depositResp.Account.Value + depositReq.AmountSat,
	)
	depositResp, err = t.trader.DepositAccount(ctx, depositReq)
	if err != nil {
		t.Fatalf("unable to process account deposit: %v", err)
	}

	// We should expect to see the transaction causing the deposit.
	depositTxid, _ = chainhash.NewHash(depositResp.Account.Outpoint.Txid)
	txids, err = waitForNTxsInMempool(
		t.lndHarness.Miner.Client, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.Fatalf("deposit transaction not found in mempool: %v", err)
	}
	if !txids[0].IsEqual(depositTxid) {
		t.Fatalf("found mempool transaction %v instead of %v",
			txids[0], depositTxid)
	}

	// The deposit transaction should contain at least one NP2WKH input.
	depositTx, err := t.lndHarness.Miner.Client.GetRawTransaction(depositTxid)
	if err != nil {
		t.Fatalf("unable to retrieve mempool transaction: %v", err)
	}
	foundNP2WKHInput := false
	for _, input := range depositTx.MsgTx().TxIn {
		if len(input.SignatureScript) > 0 {
			foundNP2WKHInput = true
		}
	}
	if !foundNP2WKHInput {
		t.Fatalf("expected NP2WKH input in deposit transaction %v: %v",
			depositTxid, depositTx)
	}

	// Confirm the deposit, and once again assert that the account state
	// is reflected correctly.
	block = mineBlocks(t, t.lndHarness, 6, 1)[0]
	_ = assertTxInBlock(t, block, depositTxid)
	assertTraderAccount(
		t, t.trader, depositResp.Account.TraderKey,
		valueAfterSecondDeposit, account.ExpirationHeight,
		poolrpc.AccountState_OPEN,
	)
	assertAuctioneerAccount(
		t, depositResp.Account.TraderKey, valueAfterSecondDeposit,
		auctioneerAccount.StateOpen,
	)

	// Finally, end the test by closing the account.
	_ = closeAccountAndAssert(t, t.trader, &poolrpc.CloseAccountRequest{
		TraderKey: account.TraderKey,
	})
}

// testAccountRenewal ensures that we can renew an account in its confirmed
// state and after it has expired.
func testAccountRenewal(t *harnessTest) {
	ctx := context.Background()

	// Create an account for 500K sats that is valid for the next 1000
	// blocks and validate its confirmation on-chain.
	const initialAccountValue = 500_000
	account := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: initialAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
	})

	// For our first case, we'll renew our account such that it expires in
	// 144 blocks from now.
	const newRelativeExpiry = 144
	_, bestHeight, err := t.lndHarness.Miner.Client.GetBestBlock()
	require.NoError(t.t, err)
	absoluteExpiry := uint32(bestHeight) + newRelativeExpiry

	// Wait for the lnd backend to catch up as well.
	ctxt, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	err = t.lndHarness.Bob.WaitForBlockchainSync(ctxt)
	require.NoError(t.t, err)

	updateReq := &poolrpc.RenewAccountRequest{
		AccountKey: account.TraderKey,
		AccountExpiry: &poolrpc.RenewAccountRequest_RelativeExpiry{
			RelativeExpiry: newRelativeExpiry,
		},
		FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
	}
	updateResp1, err := t.trader.RenewAccount(ctx, updateReq)
	require.NoError(t.t, err)

	// We'll define a helper closure to perform various assertions
	// throughout the expiration state machine.
	assertAccountState := func(valueBeforeUpdate btcutil.Amount) {
		t.t.Helper()

		// Assert that the account state is reflected correctly for both
		// the trader and auctioneer while the update hasn't confirmed.
		const multiSigUpdateFee = 153
		valueAfterMultiSigUpdate := valueBeforeUpdate - multiSigUpdateFee
		assertTraderAccount(
			t, t.trader, account.TraderKey, valueAfterMultiSigUpdate,
			absoluteExpiry, poolrpc.AccountState_PENDING_UPDATE,
		)
		assertAuctioneerAccount(
			t, account.TraderKey, valueAfterMultiSigUpdate,
			auctioneerAccount.StatePendingUpdate,
		)

		// Confirm the update transaction and continue to mine blocks up
		// until one before the expiry is met. The account should be
		// found open for both the trader and auctioneer.
		_ = mineBlocks(t, t.lndHarness, newRelativeExpiry-1, 1)
		assertTraderAccount(
			t, t.trader, account.TraderKey, valueAfterMultiSigUpdate,
			absoluteExpiry, poolrpc.AccountState_OPEN,
		)
		assertAuctioneerAccount(
			t, account.TraderKey, valueAfterMultiSigUpdate,
			auctioneerAccount.StateOpen,
		)

		// Finally, mine one more block, which should mark the account
		// as expired.
		_ = mineBlocks(t, t.lndHarness, 1, 0)
		assertTraderAccount(
			t, t.trader, account.TraderKey, valueAfterMultiSigUpdate,
			absoluteExpiry, poolrpc.AccountState_EXPIRED,
		)
		assertAuctioneerAccount(
			t, account.TraderKey, valueAfterMultiSigUpdate,
			auctioneerAccount.StateExpired,
		)
	}

	// We'll assert that that expiration update is successful on both the
	// trader and auctioneer. We'll let the account expire to test the
	// renewal case once again.
	assertAccountState(initialAccountValue)

	// We'll then process another renewal request, this time specifying the
	// expiration (144 blocks) as an absolute height.
	_, bestHeight, err = t.lndHarness.Miner.Client.GetBestBlock()
	require.NoError(t.t, err)
	absoluteExpiry = uint32(bestHeight) + newRelativeExpiry

	updateReq.AccountExpiry = &poolrpc.RenewAccountRequest_AbsoluteExpiry{
		AbsoluteExpiry: absoluteExpiry,
	}
	_, err = t.trader.RenewAccount(ctx, updateReq)
	require.NoError(t.t, err)

	// Once again, we'll assert that that expiration update is successful on
	// both the trader and auctioneer.
	assertAccountState(btcutil.Amount(updateResp1.Account.Value))

	// Close the account now that it's expired.
	_ = closeAccountAndAssert(t, t.trader, &poolrpc.CloseAccountRequest{
		TraderKey: account.TraderKey,
	})

	// Attempting another renewal request should fail as the account's been
	// closed and the funds have left the multi-sig construct.
	_, err = t.trader.RenewAccount(ctx, updateReq)
	require.Error(t.t, err)
}

// testAccountSubscription tests that a trader registers for an account after
// opening one and that the reconnection mechanism works if the server is
// stopped for maintenance.
func testAccountSubscription(t *harnessTest) {
	// Create an account over 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	acct := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: 2000000,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
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
	const defaultRelativeExpiration uint32 = 1_000

	// We create three full accounts. One that is closed again, one
	// that remains open and one that is pending open, waiting for on-chain
	// confirmation.
	closed := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: defaultRelativeExpiration,
		},
	})
	closeAccountAndAssert(t, t.trader, &poolrpc.CloseAccountRequest{
		TraderKey: closed.TraderKey,
	})
	open := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: defaultRelativeExpiration,
		},
	})
	pending, err := t.trader.InitAccount(ctxb, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: defaultRelativeExpiration,
		},
		Fees: &poolrpc.InitAccountRequest_ConfTarget{ConfTarget: 6},
	})
	if err != nil {
		t.Fatalf("could not create account: %v", err)
	}
	_, err = waitForNTxsInMempool(
		t.lndHarness.Miner.Client, 1, minerMempoolTimeout,
	)
	if err != nil {
		t.Fatalf("open tx not published in time: %v", err)
	}

	// Now that we've opened the account(s), we should also have an LSAT.
	tokenID, err := t.trader.server.GetIdentity()
	if err != nil {
		t.Fatalf("could not get the trader's identity: %v", err)
	}
	idCtx := getTokenContext(tokenID)

	// Also create an order for the open account so we can make sure it'll
	// be canceled on recovery. We need to fetch the nonce of it so we can
	// query it directly.
	_, err = t.trader.SubmitOrder(ctxb, &poolrpc.SubmitOrderRequest{
		Details: &poolrpc.SubmitOrderRequest_Ask{
			Ask: &poolrpc.Ask{
				Details: &poolrpc.Order{
					TraderKey:               open.TraderKey,
					RateFixed:               100,
					Amt:                     1500000,
					MinUnitsMatch:           1,
					MaxBatchFeeRateSatPerKw: uint64(12500),
				},
				LeaseDurationBlocks: 2016,
				Version: uint32(
					order.VersionChannelType,
				),
			},
		},
	})
	if err != nil {
		t.Fatalf("could not submit order: %v", err)
	}
	list, err := t.trader.ListOrders(ctxb, &poolrpc.ListOrdersRequest{})
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
	_, minerHeight, err := t.lndHarness.Miner.Client.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to retrieve miner height: %v", err)
	}
	resRecoveryFailed := addReservation(
		getTokenContext(&lsat.TokenID{0x02}), t, t.lndHarness.Bob,
		defaultAccountValue, uint32(minerHeight)+defaultRelativeExpiration,
		false,
	)
	resRecoveryOk := addReservation(
		idCtx, t, t.lndHarness.Bob, defaultAccountValue,
		uint32(minerHeight)+defaultRelativeExpiration, true,
	)

	// Now we simulate data loss by shutting down the trader and removing
	// its data directory completely.
	err = t.trader.stop(true)
	if err != nil {
		t.Fatalf("could not stop trader: %v", err)
	}

	// Now we just create a new trader, connected to the same lnd instance.
	t.trader = setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, t.lndHarness.Bob, t.auctioneer,
	)

	// Make sure the trader doesn't remember any accounts anymore.
	accounts, err := t.trader.ListAccounts(
		ctxb, &poolrpc.ListAccountsRequest{},
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
		ctxb, &poolrpc.RecoverAccountsRequest{},
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
		ctxb, &poolrpc.ListAccountsRequest{},
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
		poolrpc.AccountState_RECOVERY_FAILED,
	)
	assertTraderAccountState(
		t.t, t.trader, resRecoveryOk, poolrpc.AccountState_PENDING_OPEN,
	)
	assertTraderAccountState(
		t.t, t.trader, closed.TraderKey, poolrpc.AccountState_CLOSED,
	)
	assertTraderAccountState(
		t.t, t.trader, closed.TraderKey, poolrpc.AccountState_CLOSED,
	)
	assertTraderAccountState(
		t.t, t.trader, open.TraderKey, poolrpc.AccountState_OPEN,
	)
	assertTraderAccountState(
		t.t, t.trader, pending.TraderKey,
		poolrpc.AccountState_PENDING_OPEN,
	)

	// Mine the rest of the blocks to make the pending accounts fully
	// confirmed. Then check their state again.
	_ = mineBlocks(t, t.lndHarness, 5, 0)
	assertTraderAccountState(
		t.t, t.trader, pending.TraderKey, poolrpc.AccountState_OPEN,
	)
	assertTraderAccountState(
		t.t, t.trader, resRecoveryOk, poolrpc.AccountState_OPEN,
	)

	// Finally, make sure we can close out all open accounts.
	closeAccountAndAssert(t, t.trader, &poolrpc.CloseAccountRequest{
		TraderKey: open.TraderKey,
	})
	closeAccountAndAssert(t, t.trader, &poolrpc.CloseAccountRequest{
		TraderKey: pending.TraderKey,
	})
	closeAccountAndAssert(t, t.trader, &poolrpc.CloseAccountRequest{
		TraderKey: resRecoveryOk,
	})

	// Query the auctioneer directly about the status of the ask we
	// submitted earlier.
	resp, err := t.auctioneer.OrderState(
		idCtx, &auctioneerrpc.ServerOrderStateRequest{OrderNonce: askNonce},
	)
	if err != nil {
		t.Fatalf("could not query order status: %v", err)
	}
	if resp.State != auctioneerrpc.OrderState_ORDER_CANCELED {
		t.Fatalf("unexpected order state, got %d wanted %d",
			resp.State, auctioneerrpc.OrderState_ORDER_CANCELED)
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
			KeyFamily: int32(poolscript.AccountKeyFamily),
		},
	)
	if err != nil {
		t.Fatalf("could not derive key for reservation: %v", err)
	}

	// Reserve the account with the auctioneer now and parse the returned
	// keys so we can derive the account script later.
	res, err := t.auctioneer.ReserveAccount(
		lsatCtx, &auctioneerrpc.ReserveAccountRequest{
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
	script, err := poolscript.AccountScript(
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
		context.Background(), lsat.HeaderAuthorization,
		fmt.Sprintf("LSATID %x", token[:]),
	)
}
