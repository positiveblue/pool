package itest

import (
	"context"
	"math"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
)

// testBatchIO tests that the autcioneer can add extra inputs and outputs to
// the batch transaction, where the delta goes to the master account.
func testBatchIO(t *harnessTest) {
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader.
	lndArgs := []string{"--maxpendingchannels=2"}
	charlie, err := t.lndHarness.NewNode("charlie", lndArgs)
	if err != nil {
		t.Fatalf("unable to set up charlie: %v", err)
	}
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)

	err = t.lndHarness.SendCoins(ctx, 5_000_000, charlie)
	if err != nil {
		t.Fatalf("unable to send coins to carol: %v", err)
	}

	// Send some extra coins to the auctioneers LND node that we can use
	// for batch IO.
	const numInputs = 3
	for i := 0; i < numInputs; i++ {
		err = t.lndHarness.SendCoins(
			ctx, 1*btcutil.SatoshiPerBitcoin,
			t.auctioneer.cfg.LndNode,
		)
		if err != nil {
			t.Fatalf("unable to send coins to carol: %v", err)
		}
	}

	// Create an account over 2M sats that is valid for the next 1000 blocks
	// for both traders.
	account1 := openAccountAndAssert(
		t, t.trader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)
	account2 := openAccountAndAssert(
		t, secondTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	// Now that the accounts are confirmed, submit an ask order from our
	// default trader, selling 15 units (1.5M sats) of liquidity.
	const orderFixedRate = 100
	askAmt := btcutil.Amount(1_500_000)
	_, err = submitAskOrder(
		t.trader, account1.TraderKey, orderFixedRate, askAmt,
	)
	if err != nil {
		t.Fatalf("could not submit ask order: %v", err)
	}

	// Our second trader, connected to Charlie, wants to buy 8 units of
	// liquidity. So let's submit an order for that.
	bidAmt := btcutil.Amount(800_000)
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate, bidAmt,
	)
	if err != nil {
		t.Fatalf("could not submit bid order: %v", err)
	}

	// Now that we have some orders submitted, make the auctioneer request
	// adding extra inputs and outputs that will piggyback on the next
	// batch. We start by getting some UTXOs to spend from the backing
	// wallet.
	unspents, err := t.auctioneer.cfg.LndNode.WalletKitClient.ListUnspent(
		ctx, &walletrpc.ListUnspentRequest{
			MinConfs: 1,
			MaxConfs: math.MaxInt32,
		},
	)
	if err != nil {
		t.Fatalf("could not list unspent: %v", err)
	}

	// We also get the current wallet balance and account balance for the
	// auctioneer.
	startingWalletBalance, err := t.auctioneer.cfg.LndNode.WalletBalance(
		ctx, &lnrpc.WalletBalanceRequest{},
	)
	if err != nil {
		t.Fatalf("could not get balance: %v", err)
	}

	startingMasterAcct, err := t.auctioneer.AuctionAdminClient.MasterAccount(
		ctx, &adminrpc.EmptyRequest{},
	)
	if err != nil {
		t.Fatalf("could not get balance: %v", err)
	}

	// Add three new inputs of at least 1 BTC each to the batch.
	fundsReq := &adminrpc.MoveFundsRequest{}
	var (
		firstVal int64
		totalVal int64
	)
	j := 0
	for i := 0; i < len(unspents.Utxos); i++ {
		utxo := unspents.Utxos[i]

		// Skip small UTXOs.
		if utxo.AmountSat < btcutil.SatoshiPerBitcoin {
			continue
		}

		fundsReq.Inputs = append(fundsReq.Inputs, &adminrpc.Input{
			Value: uint64(utxo.AmountSat),
			Outpoint: &adminrpc.OutPoint{
				Txid:        utxo.Outpoint.TxidBytes,
				OutputIndex: utxo.Outpoint.OutputIndex,
			},
			PkScript: utxo.PkScript,
		})

		if j == 0 {
			firstVal = utxo.AmountSat
		}

		totalVal += utxo.AmountSat
		j++

		if j == numInputs {
			break
		}
	}

	if j < numInputs {
		t.Fatalf("could not find %d UTXOs", numInputs)
	}

	// We'll also get a new address that we will use for an extra output we
	// add.
	newAddrResp, err := t.auctioneer.cfg.LndNode.NewAddress(
		ctx, &lnrpc.NewAddressRequest{
			Type: lnrpc.AddressType_UNUSED_WITNESS_PUBKEY_HASH,
		},
	)
	if err != nil {
		t.Fatalf("could not get new address: %v", err)
	}

	// We'll send back the amount from the first UTXO found earlier.
	fundsReq.Outputs = append(fundsReq.Outputs, &adminrpc.Output{
		Address: newAddrResp.Address,
		Value:   uint64(firstVal),
	})

	// Let the auctioneer add some inputs and outputs to the batch.
	_, err = t.auctioneer.AuctionAdminClient.MoveFunds(ctx, fundsReq)
	if err != nil {
		t.Fatalf("could not trigger batch tick: %v", err)
	}

	// Let's kick the auctioneer once again to try and create a batch.
	_, batchTXIDs := executeBatch(t, 1)
	batchTXID := batchTXIDs[0]

	// We'll now mine a few block to confirm the transaction and fully
	// confirm the channel.
	_ = mineBlocks(t, t.lndHarness, 3, 1)

	// Now that the bath has been confirmed, ensure the channel was opened
	// as expected.
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt), *batchTXID,
		charlie.PubKey, defaultOrderDuration,
	)

	assertActiveChannel(
		t, charlie, int64(bidAmt), *batchTXID,
		t.trader.cfg.LndNode.PubKey, defaultOrderDuration,
	)

	// Finally, make sure the auctioneer's wallet balance decreased by
	// exactly two of the UTXOs spent in the batch (the third UTXOs value
	// was sent back into the wallet), and that the rest went into the
	// auctioneer account.
	finalWalletBalance, err := t.auctioneer.cfg.LndNode.WalletBalance(
		ctx, &lnrpc.WalletBalanceRequest{},
	)
	if err != nil {
		t.Fatalf("could not get balance: %v", err)
	}

	expWalletBalance := startingWalletBalance.TotalBalance + firstVal - totalVal
	if expWalletBalance != finalWalletBalance.TotalBalance {
		t.Fatalf("expected wallet balance %v, got %v",
			expWalletBalance, finalWalletBalance.TotalBalance)

	}

	finalMasterAcct, err := t.auctioneer.AuctionAdminClient.MasterAccount(
		ctx, &adminrpc.EmptyRequest{},
	)
	if err != nil {
		t.Fatalf("could not get balance: %v", err)
	}

	// Since the auctioneer gets a few sats from the batch execution fee,
	// and pays a few sats to chain fees (both below 100 000 sats), we
	// compare the final account balance in order of 100_000s.
	expAccountBalance := startingMasterAcct.Balance + totalVal - firstVal
	expAccountBalance = int64(
		math.Round(float64(expAccountBalance) / 100_000),
	)

	finalAccountBalance := int64(
		math.Round(float64(finalMasterAcct.Balance) / 100_000),
	)
	if expAccountBalance != finalAccountBalance {
		t.Fatalf("expected account balance %v, got %v",
			expAccountBalance, finalAccountBalance)

	}

	closeAllChannels(ctx, t, charlie)
}
