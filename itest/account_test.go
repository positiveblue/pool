package itest

import (
	"context"
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/lightninglabs/aperture/lsat"
	accountT "github.com/lightninglabs/pool/account"
	"github.com/lightninglabs/pool/auctioneerrpc"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/pool/poolscript"
	auctioneerAccount "github.com/lightninglabs/subasta/account"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lntest/wait"
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

	t.t.Run("version 0", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runAccountCreationTest(
			ctx, ht, accountT.VersionInitialNoVersion,
		)
	})
	t.t.Run("version 1", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runAccountCreationTest(
			ctx, ht, accountT.VersionTaprootEnabled,
		)
	})
}

// runAccountCreationTest tests that the trader can successfully create an
// account of the given version on-chain and close it.
func runAccountCreationTest(ctx context.Context, t *harnessTest,
	version accountT.Version) {

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	account := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
		Version: rpcVersion(version),
	})

	// Proceed to close it to a custom output where half of the account
	// value goes towards it and the rest towards fees.
	const outputValue = defaultAccountValue / 2
	resp, err := t.trader.cfg.LndNode.NewAddress(ctx, &lnrpc.NewAddressRequest{
		Type: lnrpc.AddressType_WITNESS_PUBKEY_HASH,
	})
	require.NoError(t.t, err)
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
	require.Len(t.t, closeTx.TxOut, 1)
	require.Equal(t.t, int64(outputValue), closeTx.TxOut[0].Value)

	addr, err := btcutil.DecodeAddress(closeAddr, &chaincfg.MainNetParams)
	require.NoError(t.t, err)
	outputScript, err := txscript.PayToAddrScript(addr)
	require.NoError(t.t, err)
	require.Equal(t.t, outputScript, closeTx.TxOut[0].PkScript)

	// Make sure the default account limit is enforced on the auctioneer
	// side.
	_, err = t.trader.InitAccount(ctx, &poolrpc.InitAccountRequest{
		AccountValue: uint64(11 * btcutil.SatoshiPerBitcoin),
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
		Fees: &poolrpc.InitAccountRequest_ConfTarget{ConfTarget: 6},
	})
	require.Error(t.t, err)
	require.Contains(t.t, err.Error(), "maximum account value")
}

// testAccountWithdrawal tests that the auctioneer is able to handle a trader's
// request to withdraw funds from an account.
func testAccountWithdrawal(t *harnessTest) {
	ctx := context.Background()

	t.t.Run("version 0", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runAccountWithdrawalTest(
			ctx, ht, accountT.VersionInitialNoVersion,
		)
	})
	t.t.Run("version 1", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runAccountWithdrawalTest(
			ctx, ht, accountT.VersionTaprootEnabled,
		)
	})
}

// runAccountWithdrawalTest tests that the auctioneer is able to handle a
// trader's request to withdraw funds from an account of the given version.
func runAccountWithdrawalTest(ctx context.Context, t *harnessTest,
	version accountT.Version) {

	// Create an account for 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	account := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
		Version: rpcVersion(version),
	})

	// With the account open, we'll now attempt to withdraw half of the
	// funds we committed to a P2WPKH address. We'll first try an address
	// that does not belong to the current network, which should fail.
	withdrawValue := account.Value / 2
	withdrawReq := &poolrpc.WithdrawAccountRequest{
		TraderKey: account.TraderKey,
		Outputs: []*poolrpc.Output{{
			ValueSat: withdrawValue,
			Address:  "bc1qvata6vu0eldas9qqm6qguflcf55x20exkzxujh",
		}},
		FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
	}
	_, err := t.trader.WithdrawAccount(ctx, withdrawReq)
	require.Error(t.t, err)
	require.Contains(t.t, err.Error(), "invalid address")

	// Now try a valid address.
	withdrawTxid, valueAfterWithdrawal := withdrawAccountAndAssertMempool(
		t, t.trader, account.TraderKey, int64(account.Value),
		withdrawValue, validTestAddr, version,
	)

	// We'll attempt to bump the fee rate of the withdrawal from 1 sat/vbyte
	// to 10 sat/vbyte. The withdrawal transaction doesn't contain an output
	// under the backing lnd node's control, so the call should fail.
	_, err = t.trader.BumpAccountFee(ctx, &poolrpc.BumpAccountFeeRequest{
		TraderKey:       account.TraderKey,
		FeeRateSatPerKw: withdrawReq.FeeRateSatPerKw * 250 * 10,
	})
	require.Error(t.t, err)
	require.Contains(t.t, err.Error(), "eligible outputs")

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

	t.t.Run("version 0", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runAccountDepositTest(
			ctx, ht, accountT.VersionInitialNoVersion,
		)
	})
	t.t.Run("version 1", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runAccountDepositTest(
			ctx, ht, accountT.VersionTaprootEnabled,
		)
	})
}

// runAccountDepositTest tests that the auctioneer is able to handle a trader's
// request to deposit funds into an account of the given version.
func runAccountDepositTest(ctx context.Context, t *harnessTest,
	version accountT.Version) {

	// Create an account for 500K sats that is valid for the next 1000
	// blocks and validate its confirmation on-chain.
	const initialAccountValue = 500_000
	account := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: initialAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
		Version: rpcVersion(version),
	})

	depositAmount := btcutil.Amount(100_000)
	depositReq := &poolrpc.DepositAccountRequest{
		TraderKey:       account.TraderKey,
		AmountSat:       uint64(depositAmount),
		FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
	}

	addressTypes := []lnrpc.AddressType{
		lnrpc.AddressType_WITNESS_PUBKEY_HASH,
		lnrpc.AddressType_NESTED_PUBKEY_HASH,
		lnrpc.AddressType_TAPROOT_PUBKEY,
	}

	valueBeforeDeposit := btcutil.Amount(initialAccountValue)
	for _, addrType := range addressTypes {
		valueAfterDeposit := valueBeforeDeposit + depositAmount

		// Send all the coins to an address of the type that we want
		// to test.
		sendAllCoinsToAddrType(
			ctx, t, t.lndHarness, t.trader.cfg.LndNode, addrType,
		)

		depositResp, err := t.trader.DepositAccount(ctx, depositReq)
		require.NoError(t.t, err)

		// We should expect to see the transaction causing the deposit.
		depositTxid, _ := chainhash.NewHash(
			depositResp.Account.Outpoint.Txid,
		)
		txids, err := waitForNTxsInMempool(
			t.lndHarness.Miner.Client, 1, minerMempoolTimeout,
		)
		require.NoError(t.t, err)
		require.Len(t.t, txids, 1)

		// Assert that the account state is reflected correctly for both
		// the trader and auctioneer while the deposit hasn't confirmed.
		assertTraderAccount(
			t, t.trader, depositResp.Account.TraderKey,
			valueAfterDeposit, account.ExpirationHeight,
			poolrpc.AccountState_PENDING_UPDATE,
		)
		assertAuctioneerAccount(
			t, depositResp.Account.TraderKey, valueAfterDeposit,
			auctioneerAccount.StatePendingUpdate,
		)

		// We'll assume the fee rate wasn't enough for the deposit to
		// confirm, so we'll attempt to bump it from 1 sat/vbyte to
		// 10 sat/vbyte. We should then see two transactions in the
		// mempool.
		feeRate := depositReq.FeeRateSatPerKw * 250 * 10
		_, err = t.trader.BumpAccountFee(
			ctx, &poolrpc.BumpAccountFeeRequest{
				TraderKey:       account.TraderKey,
				FeeRateSatPerKw: feeRate,
			},
		)
		require.NoError(t.t, err)

		_, err = waitForNTxsInMempool(
			t.lndHarness.Miner.Client, 2, minerMempoolTimeout,
		)
		require.NoError(t.t, err)

		// Confirm the deposit, and once again assert that the account
		// state is reflected correctly.
		block := mineBlocks(t, t.lndHarness, 6, 2)[0]
		_ = assertTxInBlock(t, block, depositTxid)

		assertTraderAccount(
			t, t.trader, depositResp.Account.TraderKey,
			valueAfterDeposit, account.ExpirationHeight,
			poolrpc.AccountState_OPEN,
		)
		assertAuctioneerAccount(
			t, depositResp.Account.TraderKey,
			valueAfterDeposit, auctioneerAccount.StateOpen,
		)

		valueBeforeDeposit = valueAfterDeposit
	}

	// Finally, end the test by closing the account.
	_ = closeAccountAndAssert(t, t.trader, &poolrpc.CloseAccountRequest{
		TraderKey: account.TraderKey,
	})
}

// testAccountRenewal ensures that we can renew an account in its confirmed
// state and after it has expired.
func testAccountRenewal(t *harnessTest) {
	ctx := context.Background()

	t.t.Run("version 0", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runAccountRenewalTest(
			ctx, ht, accountT.VersionInitialNoVersion,
		)
	})
	t.t.Run("version 1", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runAccountRenewalTest(
			ctx, ht, accountT.VersionTaprootEnabled,
		)
	})
}

// runAccountRenewalTest ensures that we can renew an account of the given
// version in its confirmed state, and after it has expired.
func runAccountRenewalTest(ctx context.Context, t *harnessTest,
	version accountT.Version) {

	// Create an account for 500K sats that is valid for the next 1000
	// blocks and validate its confirmation on-chain.
	const initialAccountValue = 500_000
	account := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: initialAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
		Version: rpcVersion(version),
	})

	// For our first case, we'll renew our account such that it expires in
	// 432 blocks from now.
	const newRelativeExpiry = 432
	_, bestHeight, err := t.lndHarness.Miner.Client.GetBestBlock()
	require.NoError(t.t, err)
	absoluteExpiry := uint32(bestHeight) + newRelativeExpiry

	// Wait for the lnd backend to catch up as well.
	err = t.lndHarness.Bob.WaitForBlockchainSync()
	require.NoError(t.t, err)

	updateReq := &poolrpc.RenewAccountRequest{
		AccountKey: account.TraderKey,
		AccountExpiry: &poolrpc.RenewAccountRequest_RelativeExpiry{
			RelativeExpiry: newRelativeExpiry,
		},
		FeeRateSatPerKw: uint64(chainfee.FeePerKwFloor),
		NewVersion:      rpcVersion(version),
	}
	updateResp1, err := t.trader.RenewAccount(ctx, updateReq)
	require.NoError(t.t, err)

	// We'll define a helper closure to perform various assertions
	// throughout the expiration state machine.
	assertAccountState := func(valueBeforeUpdate btcutil.Amount) {
		t.t.Helper()

		// Assert that the account state is reflected correctly for both
		// the trader and auctioneer while the update hasn't confirmed.
		multiSigUpdateFee := btcutil.Amount(153)
		if version == accountT.VersionTaprootEnabled {
			multiSigUpdateFee -= taprootMultiSigSpendSizeDelta
		}

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
	// expiration (432 blocks) as an absolute height.
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
	t.t.Run("version 0", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runAccountSubscriptionTest(ht, accountT.VersionInitialNoVersion)
	})
	t.t.Run("version 1", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runAccountSubscriptionTest(ht, accountT.VersionTaprootEnabled)
	})
}

// runAccountSubscriptionTest tests that a trader registers for an account of
// the given version after opening one and that the reconnection mechanism works
// if the server is stopped for maintenance.
func runAccountSubscriptionTest(t *harnessTest, version accountT.Version) {
	// Create an account over 2M sats that is valid for the next 1000 blocks
	// and validate its confirmation on-chain.
	acct := openAccountAndAssert(t, t.trader, &poolrpc.InitAccountRequest{
		AccountValue: 2000000,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: 1_000,
		},
		Version: rpcVersion(version),
	})
	assertTraderSubscribed(t, t.trader, acct, 1)

	// Now that the trader is connected, let's shut down the auctioneer
	// server to simulate maintenance and see if the trader reconnects after
	// a while.
	t.restartServer()
	assertTraderSubscribed(t, t.trader, acct, 1)

	// And let's do it again, just to make sure the shutdown and re-connect
	// can happen multiple times in a row.
	t.restartServer()
	assertTraderSubscribed(t, t.trader, acct, 1)
}

// testServerAssistedAccountRecovery tests that a trader can recover all
// accounts with the help of the auctioneer in case they lose their local data
// directory. This assumes that the connected lnd instance still runs with the
// same seed the accounts originally were created with.
func testServerAssistedAccountRecovery(t *harnessTest) {
	ctx := context.Background()

	t.t.Run("version 0", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runServerAssistedAccountRecoveryTest(
			ctx, ht, accountT.VersionInitialNoVersion,
		)
	})
	t.t.Run("version 1", func(tt *testing.T) {
		ht := newHarnessTest(tt, t.lndHarness, t.auctioneer, t.trader)
		runServerAssistedAccountRecoveryTest(
			ctx, ht, accountT.VersionTaprootEnabled,
		)
	})
}

// runServerAssistedAccountRecoveryTest tests that a trader can recover all
// accounts of the given version with the help of the auctioneer in case they
// lose their local data directory. This assumes that the connected lnd instance
// still runs with the same seed the accounts originally were created with.
func runServerAssistedAccountRecoveryTest(ctx context.Context, t *harnessTest,
	version accountT.Version) {

	const defaultRelativeExpiration uint32 = 1_000

	// We need a third lnd node, Charlie that is used for the account
	// recover stuff, so we create a completely new trader for each run.
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndDefaultArgs)
	trader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, trader)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)
	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	// We create three full accounts. One that is closed again, one
	// that remains open and one that is pending open, waiting for on-chain
	// confirmation.
	closed := openAccountAndAssert(t, trader, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: defaultRelativeExpiration,
		},
		Version: rpcVersion(version),
	})
	closeAccountAndAssert(t, trader, &poolrpc.CloseAccountRequest{
		TraderKey: closed.TraderKey,
	})
	open := openAccountAndAssert(t, trader, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: defaultRelativeExpiration,
		},
		Version: rpcVersion(version),
	})

	// We also create an open account of the base version, just to make sure
	// we can have mixed versions in the recovery.
	open2 := openAccountAndAssert(t, trader, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: defaultRelativeExpiration,
		},
		Version: rpcVersion(accountT.VersionInitialNoVersion),
	})
	pending, err := trader.InitAccount(ctx, &poolrpc.InitAccountRequest{
		AccountValue: defaultAccountValue,
		AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
			RelativeHeight: defaultRelativeExpiration,
		},
		Fees:    &poolrpc.InitAccountRequest_ConfTarget{ConfTarget: 6},
		Version: rpcVersion(version),
	})
	require.NoError(t.t, err)
	_, err = waitForNTxsInMempool(
		t.lndHarness.Miner.Client, 1, minerMempoolTimeout,
	)
	require.NoError(t.t, err)

	// Now that we've opened the account(s), we should also have an LSAT.
	tokenID, err := trader.server.GetIdentity()
	require.NoError(t.t, err)
	idCtx := getTokenContext(tokenID)

	// Also create an order for the open account so we can make sure it'll
	// be canceled on recovery. We need to fetch the nonce of it so we can
	// query it directly.
	_, err = trader.SubmitOrder(ctx, &poolrpc.SubmitOrderRequest{
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
					orderT.VersionChannelType,
				),
			},
		},
	})
	require.NoError(t.t, err)
	list, err := trader.ListOrders(ctx, &poolrpc.ListOrdersRequest{})
	require.NoError(t.t, err)
	require.Len(t.t, list.Asks, 1)
	askNonce := list.Asks[0].Details.OrderNonce

	// Now we also create two reservations. One we send funds to, the other
	// we don't. The trader won't know of any of them but when recovering
	// will still try to recover them. We need to use a dummy token for the
	// first one, otherwise we couldn't register the second one.
	_, minerHeight, err := t.lndHarness.Miner.Client.GetBestBlock()
	require.NoError(t.t, err)

	var randToken lsat.TokenID
	_, _ = rand.Read(randToken[16:])
	resRecoveryFailed := addReservation(
		getTokenContext(&randToken), t, charlie,
		defaultAccountValue, uint32(minerHeight)+defaultRelativeExpiration,
		false, version,
	)
	resRecoveryOk := addReservation(
		idCtx, t, charlie, defaultAccountValue,
		uint32(minerHeight)+defaultRelativeExpiration, true, version,
	)

	// Now we simulate data loss by shutting down the trader and removing
	// its data directory completely.
	err = trader.stop(true)
	require.NoError(t.t, err)

	// Now we just create a new trader, connected to the same lnd instance.
	trader = setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)

	// Make sure the trader doesn't remember any accounts anymore.
	accounts, err := trader.ListAccounts(
		ctx, &poolrpc.ListAccountsRequest{},
	)
	require.NoError(t.t, err)
	require.Len(t.t, accounts.Accounts, 0)

	// Start the recovery process. We expect four accounts to be recovered
	// even though there were 5 accounts. One of them isn't counted because
	// it should be in the database marked with the state of recovery
	// failure.
	recovery, err := trader.RecoverAccounts(
		ctx, &poolrpc.RecoverAccountsRequest{},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, uint32(5), recovery.NumRecoveredAccounts)

	// Now make sure the accounts are all in the correct state.
	accounts, err = trader.ListAccounts(
		ctx, &poolrpc.ListAccountsRequest{},
	)
	require.NoError(t.t, err)
	require.Len(t.t, accounts.Accounts, 6)

	assertTraderAccountState(
		t.t, trader, resRecoveryFailed,
		poolrpc.AccountState_RECOVERY_FAILED, versionCheck(version),
	)
	assertTraderAccountState(
		t.t, trader, resRecoveryOk, poolrpc.AccountState_PENDING_OPEN,
		versionCheck(version),
	)
	assertTraderAccountState(
		t.t, trader, closed.TraderKey, poolrpc.AccountState_CLOSED,
		versionCheck(version),
	)
	assertTraderAccountState(
		t.t, trader, closed.TraderKey, poolrpc.AccountState_CLOSED,
		versionCheck(version),
	)
	assertTraderAccountState(
		t.t, trader, open.TraderKey, poolrpc.AccountState_OPEN,
		versionCheck(version),
	)
	assertTraderAccountState(
		t.t, trader, open2.TraderKey, poolrpc.AccountState_OPEN,
		versionCheck(accountT.VersionInitialNoVersion),
	)
	assertTraderAccountState(
		t.t, trader, pending.TraderKey,
		poolrpc.AccountState_PENDING_OPEN,
		versionCheck(version),
	)

	// Mine the rest of the blocks to make the pending accounts fully
	// confirmed. Then check their state again.
	_ = mineBlocks(t, t.lndHarness, 5, 0)
	assertTraderAccountState(
		t.t, trader, pending.TraderKey, poolrpc.AccountState_OPEN,
	)
	assertTraderAccountState(
		t.t, trader, resRecoveryOk, poolrpc.AccountState_OPEN,
	)

	// Finally, make sure we can close out all open accounts.
	closeAccountAndAssert(t, trader, &poolrpc.CloseAccountRequest{
		TraderKey: open.TraderKey,
	})
	closeAccountAndAssert(t, trader, &poolrpc.CloseAccountRequest{
		TraderKey: open2.TraderKey,
	})
	closeAccountAndAssert(t, trader, &poolrpc.CloseAccountRequest{
		TraderKey: pending.TraderKey,
	})
	closeAccountAndAssert(t, trader, &poolrpc.CloseAccountRequest{
		TraderKey: resRecoveryOk,
	})

	// Query the auctioneer directly about the status of the ask we
	// submitted earlier.
	resp, err := t.auctioneer.OrderState(
		idCtx, &auctioneerrpc.ServerOrderStateRequest{
			OrderNonce: askNonce,
		},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, auctioneerrpc.OrderState_ORDER_CANCELED, resp.State)
}

func addReservation(lsatCtx context.Context, t *harnessTest,
	node *lntest.HarnessNode, value uint64, expiry uint32, sendFunds bool,
	accountVersion accountT.Version) []byte {

	ctxb := context.Background()

	// Derive a new key for the reserved account so the trader will try to
	// recover with it.
	keyDesc, err := node.WalletKitClient.DeriveNextKey(
		ctxb, &walletrpc.KeyReq{
			KeyFamily: int32(poolscript.AccountKeyFamily),
		},
	)
	require.NoError(t.t, err)

	// Reserve the account with the auctioneer now and parse the returned
	// keys so we can derive the account script later.
	res, err := t.auctioneer.ReserveAccount(
		lsatCtx, &auctioneerrpc.ReserveAccountRequest{
			AccountValue:  value,
			TraderKey:     keyDesc.RawKeyBytes,
			AccountExpiry: expiry,
			Version:       uint32(accountVersion),
		},
	)
	require.NoError(t.t, err)
	traderKey, err := btcec.ParsePubKey(keyDesc.RawKeyBytes)
	require.NoError(t.t, err)
	auctioneerKey, err := btcec.ParsePubKey(res.AuctioneerKey)
	require.NoError(t.t, err)
	batchKey, err := btcec.ParsePubKey(res.InitialBatchKey)
	require.NoError(t.t, err)

	// To know the script we need to get the derived secret.
	keyRes, err := node.SignerClient.DeriveSharedKey(
		ctxb, &signrpc.SharedKeyRequest{
			EphemeralPubkey: res.AuctioneerKey,
			KeyLoc:          keyDesc.KeyLoc,
		},
	)
	require.NoError(t.t, err)

	var sharedKey [32]byte
	copy(sharedKey[:], keyRes.SharedKey)
	script, err := poolscript.AccountScript(
		accountVersion.ScriptVersion(), expiry, traderKey,
		auctioneerKey, batchKey, sharedKey,
	)
	require.NoError(t.t, err)

	if !sendFunds {
		return keyDesc.RawKeyBytes
	}

	_, err = node.WalletKitClient.SendOutputs(
		ctxb, &walletrpc.SendOutputsRequest{
			Outputs: []*signrpc.TxOut{{
				Value:    int64(value),
				PkScript: script,
			}},
			SatPerKw: 300,
		},
	)
	require.NoError(t.t, err)

	return keyDesc.RawKeyBytes
}

func getTokenContext(token *lsat.TokenID) context.Context {
	return metadata.AppendToOutgoingContext(
		context.Background(), lsat.HeaderAuthorization,
		fmt.Sprintf("LSATID %x", token[:]),
	)
}

// sendAllCoinsToAddrType sweeps all coins from the wallet and sends them to a
// new address of the given type.
func sendAllCoinsToAddrType(ctx context.Context, t *harnessTest,
	net *lntest.NetworkHarness, node *lntest.HarnessNode,
	addrType lnrpc.AddressType) {

	resp, err := node.NewAddress(ctx, &lnrpc.NewAddressRequest{
		Type: addrType,
	})
	require.NoError(t.t, err)

	_, err = node.SendCoins(ctx, &lnrpc.SendCoinsRequest{
		Addr:    resp.Address,
		SendAll: true,
	})
	require.NoError(t.t, err)

	_ = mineBlocks(t, net, 1, 1)[0]

	err = wait.NoError(func() error {
		unspentResp, err := node.WalletKitClient.ListUnspent(
			ctx, &walletrpc.ListUnspentRequest{
				MinConfs: 1,
				MaxConfs: 99,
			},
		)
		if err != nil {
			return err
		}

		if len(unspentResp.Utxos) != 1 {
			return fmt.Errorf("expected one unspent output to be "+
				"confirmed, got %d", len(unspentResp.Utxos))
		}

		return nil
	}, defaultTimeout)
	require.NoError(t.t, err)
}
