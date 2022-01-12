package itest

import (
	"context"
	"math"

	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
)

// testBatchIO tests that the autcioneer can add extra inputs and outputs to
// the batch transaction, where the delta goes to the master account.
func testBatchIO(t *harnessTest) { // nolint:gocyclo
	ctx := context.Background()

	// We need a third lnd node, Charlie that is used for the second trader.
	lndArgs := append([]string{"--maxpendingchannels=2"}, lndDefaultArgs...)
	charlie := t.lndHarness.NewNode(t.t, "charlie", lndArgs)
	secondTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, charlie, t.auctioneer,
	)
	defer shutdownAndAssert(t, charlie, secondTrader)

	t.lndHarness.SendCoins(t.t, 5_000_000, charlie)

	// Send some extra coins to the auctioneers LND node that we can use
	// for batch IO.
	const numInputs = 3
	for i := 0; i < numInputs; i++ {
		t.lndHarness.SendCoins(
			t.t, 1*btcutil.SatoshiPerBitcoin,
			t.auctioneer.cfg.LndNode,
		)
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
	_, err := submitAskOrder(
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

	// We start by requesting movement of funds, but immediately reset it.
	_, err = t.auctioneer.AuctionAdminClient.MoveFunds(ctx, fundsReq)
	if err != nil {
		t.Fatalf("could not trigger move funds: %v", err)
	}

	emptyReq := &adminrpc.MoveFundsRequest{}
	_, err = t.auctioneer.AuctionAdminClient.MoveFunds(ctx, emptyReq)
	if err != nil {
		t.Fatalf("could not trigger move funds: %v", err)
	}

	// Execute the batch, making sure no extra inputs/outputs were added.
	batchTxs, batchTXIDs := executeBatch(t, 1)
	batchTx := batchTxs[0]
	batchTXID := batchTXIDs[0]
	_ = mineBlocks(t, t.lndHarness, 3, 1)

	// Two account and one auctioneer inputs, 2 account, one auctioneer and
	// 1 channel output.
	if len(batchTx.TxIn) != 3 || len(batchTx.TxOut) != 4 {
		t.Fatalf("Unexpected number of inputs/outputs: %v",
			spew.Sdump(batchTx))
	}

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

	// Submit another bid so we also get a match in the next batch.
	bidAmt2 := bidAmt / 2
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate, bidAmt2,
	)
	if err != nil {
		t.Fatalf("could not submit bid order: %v", err)
	}

	// Let the auctioneer add some inputs and outputs to the batch.
	_, err = t.auctioneer.AuctionAdminClient.MoveFunds(ctx, fundsReq)
	if err != nil {
		t.Fatalf("could not trigger move funds: %v", err)
	}

	// Let's kick the auctioneer once again to try and create a batch.
	batchTxs, batchTXIDs = executeBatch(t, 1)
	batchTXID = batchTXIDs[0]
	batchTx = batchTxs[0]

	// Two account and one auctioneer + 3 extra inputs. 2 account, one
	// auctioneer and 1 channel + 1 extra output.
	if len(batchTx.TxIn) != 6 || len(batchTx.TxOut) != 5 {
		t.Fatalf("Unexpected number of inputs/outputs: %v",
			spew.Sdump(batchTx))
	}

	// We'll now mine a few block to confirm the transaction and fully
	// confirm the channel.
	_ = mineBlocks(t, t.lndHarness, 3, 1)

	// Now that the bath has been confirmed, ensure the channel was opened
	// as expected.
	assertActiveChannel(
		t, t.trader.cfg.LndNode, int64(bidAmt2), *batchTXID,
		charlie.PubKey, defaultOrderDuration,
	)

	assertActiveChannel(
		t, charlie, int64(bidAmt2), *batchTXID,
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

	// We'll test the case where the admin tries to spend more out of the
	// master account than what is left. This should lead to the batch
	// being executed without the extra IO.
	_, err = submitBidOrder(
		secondTrader, account2.TraderKey, orderFixedRate, bidAmt2,
	)
	if err != nil {
		t.Fatalf("could not submit bid order: %v", err)
	}

	// We'll request the whole balance going to a new output. Obviously
	// this will take the master account either into dust or negative,
	// which we won't allow.
	fundsReq = &adminrpc.MoveFundsRequest{}
	fundsReq.Outputs = append(fundsReq.Outputs, &adminrpc.Output{
		Address: newAddrResp.Address,
		Value:   uint64(finalMasterAcct.Balance),
	})

	_, err = t.auctioneer.AuctionAdminClient.MoveFunds(ctx, fundsReq)
	if err != nil {
		t.Fatalf("could not trigger move funds: %v", err)
	}

	// Let's kick the auctioneer once again to try and create a batch.
	batchTxs, _ = executeBatch(t, 1)
	batchTx = batchTxs[0]

	// Two account and one auctioneer inputs. 2 account, one
	// auctioneer and 1 channel output expected.
	if len(batchTx.TxIn) != 3 || len(batchTx.TxOut) != 4 {
		t.Fatalf("Unexpected number of inputs/outputs: %v",
			spew.Sdump(batchTx))
	}

	// Confirm channels and close them before we exit.
	_ = mineBlocks(t, t.lndHarness, 3, 1)
	closeAllChannels(ctx, t, charlie)
}
