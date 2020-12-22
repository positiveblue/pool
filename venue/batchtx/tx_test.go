package batchtx

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

var (
	auctioneerKey, _ = btcec.NewPrivateKey(btcec.S256())
)

type mockFeeSchedule struct {
	baseFee    btcutil.Amount
	exeFeeRate orderT.FixedRatePremium
}

func (m *mockFeeSchedule) BaseFee() btcutil.Amount {
	return m.baseFee
}

func (m *mockFeeSchedule) ExecutionFee(amt btcutil.Amount) btcutil.Amount {
	return btcutil.Amount(orderT.PerBlockPremium(amt, uint32(m.exeFeeRate)))
}

var _ terms.FeeSchedule = (*mockFeeSchedule)(nil)

// TestBatchTransactionAssembly tests that given a valid set of parameters,
// we're able to construct a complete batch transaction. All relevant outputs
// should be present in the transaction, and all our indexes should be
// populated and point to the correct outputs.
func TestBatchTransactionAssembly(t *testing.T) { // nolint:gocyclo
	t.Parallel()

	// For simplicity, we'll use the same clearing price of 1% for the
	// entire batch.
	const clearingPrice = orderT.FixedRatePremium(10000)
	feeSchedule := mockFeeSchedule{
		baseFee:    1,
		exeFeeRate: orderT.FixedRatePremium(10000),
	}

	acctValue := btcutil.SatoshiPerBitcoin
	numRandTraders := 10

	orderBatch := matching.EmptyBatch()

	// First, we'll generate a series of random traders. Each trader will
	// have the same account size to make our calculations below much
	// easier.
	traders := make([]*account.Account, numRandTraders)
	for i := 0; i < numRandTraders; i++ {
		traderKey, err := btcec.NewPrivateKey(btcec.S256())
		if err != nil {
			t.Fatalf("unable to generate trader key: %v", err)
		}

		acct := &account.Account{
			Value:    btcutil.Amount(acctValue),
			Expiry:   2016,
			BatchKey: traderKey.PubKey(),
		}
		traderKeyBytes := traderKey.PubKey().SerializeCompressed()
		copy(acct.TraderKeyRaw[:], traderKeyBytes)
		copy(acct.Secret[:], traderKeyBytes)
		copy(acct.OutPoint.Hash[:], traderKeyBytes)

		traders[i] = acct
	}

	// Next, we'll create a set of random bids and asks.
	orderSize := btcutil.Amount(1_000_000)
	const numAsks = 20
	asks := make([]*order.Ask, 0, numAsks)
	for i := 0; i < numAsks; i++ {
		var (
			askKey   [33]byte
			askNonce [32]byte
		)
		askerFundingKey, err := btcec.NewPrivateKey(btcec.S256())
		if err != nil {
			t.Fatalf("unable to generate funding key: %v", err)
		}
		copy(askKey[:], askerFundingKey.PubKey().SerializeCompressed())
		copy(askNonce[:], askKey[:])
		asks = append(asks, &order.Ask{
			Ask: orderT.Ask{
				Kit: *orderT.NewKit(askNonce),
			},
			Kit: order.Kit{
				MultiSigKey: askKey,
			},
		})
	}

	const numBids = numAsks / 2
	bids := make([]*order.Bid, 0, numBids)
	for i := 0; i < numBids; i++ {
		var (
			bidKey   [33]byte
			bidNonce [32]byte
		)
		bidderFundingKey, err := btcec.NewPrivateKey(btcec.S256())
		if err != nil {
			t.Fatalf("unable to generate funding key: %v", err)
		}
		copy(bidKey[:], bidderFundingKey.PubKey().SerializeCompressed())
		copy(bidNonce[:], bidKey[:])
		bids = append(bids, &order.Bid{
			Bid: orderT.Bid{
				Kit: *orderT.NewKit(bidNonce),
			},
			Kit: order.Kit{
				MultiSigKey: bidKey,
			},
		})
	}

	// Each bid will be matched with two asks from the same trader.
	ordersForAskers := make(map[matching.AccountID]map[orderT.Nonce]struct{})
	ordersForBidders := make(map[matching.AccountID]map[orderT.Nonce]struct{})
	for i, bid := range bids {
		// Since there are 10 traders in total, we'll assign asks to the
		// next trader of the first five, and bids to the next trader of
		// the last five.
		askerIdx := i % (numRandTraders / 2)
		asker := traders[askerIdx]
		bidderIdx := askerIdx + (numRandTraders / 2)
		bidder := traders[bidderIdx]

		ask1 := asks[i*2]
		ask2 := asks[i*2+1]

		orderBatch.Orders = append(orderBatch.Orders, matching.MatchedOrder{
			Asker:  matching.NewTraderFromAccount(asker),
			Bidder: matching.NewTraderFromAccount(bidder),
			Details: matching.OrderPair{
				Bid: bid,
				Ask: ask1,
				Quote: matching.PriceQuote{
					TotalSatsCleared: orderSize / 2,
				},
			},
		})
		orderBatch.Orders = append(orderBatch.Orders, matching.MatchedOrder{
			Asker:  matching.NewTraderFromAccount(asker),
			Bidder: matching.NewTraderFromAccount(bidder),
			Details: matching.OrderPair{
				Bid: bid,
				Ask: ask2,
				Quote: matching.PriceQuote{
					TotalSatsCleared: orderSize / 2,
				},
			},
		})

		if _, ok := ordersForAskers[asker.TraderKeyRaw]; !ok {
			ordersForAskers[asker.TraderKeyRaw] = make(map[orderT.Nonce]struct{})
		}
		ordersForAskers[asker.TraderKeyRaw][ask1.Nonce()] = struct{}{}
		ordersForAskers[asker.TraderKeyRaw][ask2.Nonce()] = struct{}{}

		if _, ok := ordersForBidders[bidder.TraderKeyRaw]; !ok {
			ordersForBidders[bidder.TraderKeyRaw] = make(map[orderT.Nonce]struct{})
		}
		ordersForBidders[bidder.TraderKeyRaw][bid.Nonce()] = struct{}{}
	}

	// To complete our test batch, we'll generate an actual trading report,
	// and also supply the clearing price of 1% that we use in our tests to
	// make things easy.
	orderBatch.FeeReport = matching.NewTradingFeeReport(
		orderBatch.Orders, &feeSchedule, clearingPrice,
	)
	orderBatch.ClearingPrice = clearingPrice

	// With all our set up done, we'll now create our master account diff,
	// then construct the batch transaction.
	priorAccountPoint := wire.OutPoint{}
	auctPubKey := auctioneerKey.PubKey().SerializeCompressed()
	copy(priorAccountPoint.Hash[:], auctPubKey)
	masterAcct := &account.Auctioneer{
		OutPoint: priorAccountPoint,
		Balance:  btcutil.Amount(acctValue * 10),
		AuctioneerKey: &keychain.KeyDescriptor{
			PubKey: auctioneerKey.PubKey(),
		},
	}
	batchKey := auctioneerKey.PubKey()

	// Now we can being the real meat of our tests: ensuring the batch
	// transaction and all the relevant indexes ere constructed properly.
	feeRate := chainfee.SatPerKWeight(200)
	batchTxCtx, err := NewExecutionContext(
		batchKey, orderBatch, masterAcct, feeRate, &feeSchedule,
	)
	if err != nil {
		t.Fatalf("unable to construct batch tx: %v", err)
	}

	if batchTxCtx.FeeInfoEstimate.FeeRate() != feeRate {
		t.Fatalf("assembled tx had wrong feerate %v, wanted %v",
			batchTxCtx.FeeInfoEstimate.FeeRate(), feeRate)
	}

	batchTx := batchTxCtx.ExeTx

	// Every trader should be able to find their account output in the
	// batch transaction.
	for _, trader := range traders {
		// An entry in the index for this trader should be present.
		_, ok := batchTxCtx.AcctOutputForTrader(
			trader.TraderKeyRaw,
		)
		if !ok {
			t.Fatalf("unable to find acct output for: %x",
				trader.TraderKeyRaw[:])
		}
	}

	// Next, for each order within the batch, we should be able to find an
	// output in the context that actually executes the order (creates the
	// channel).
	for _, order := range orderBatch.Orders {
		ask := order.Details.Ask
		bid := order.Details.Bid

		askOutputs, ok := batchTxCtx.OutputsForOrder(ask.Nonce())
		if !ok {
			t.Fatalf("unable to find output for ask in batch " +
				"ctx")
		}
		if len(askOutputs) != 1 {
			t.Fatal("expected one output per ask")
		}
		askOutput := askOutputs[0]
		bidOutputs, ok := batchTxCtx.OutputsForOrder(bid.Nonce())
		if !ok {
			t.Fatalf("unable to find output for bid in batch " +
				"ctx")
		}
		if len(bidOutputs) != 2 {
			t.Fatal("expected two outputs per bid")
		}

		// Each ask output should have a matching bid output.
		foundMatchingBid := false
		for i := range bidOutputs {
			bidOutput := bidOutputs[i]

			// The output for the bid and the ask should actually be
			// pointing to the exact same output.
			if askOutput.OutPoint != bidOutput.OutPoint {
				continue
			}
			if !reflect.DeepEqual(askOutput.TxOut, bidOutput.TxOut) {
				continue
			}

			// The output as found in batch transaction should also
			// match what is in the index.
			realAskOutput := batchTx.TxOut[askOutput.OutPoint.Index]
			realBidOutput := batchTx.TxOut[bidOutput.OutPoint.Index]
			if !reflect.DeepEqual(askOutput.TxOut, realAskOutput) {
				continue
			}
			if !reflect.DeepEqual(askOutput.TxOut, realBidOutput) {
				continue
			}

			foundMatchingBid = true
		}
		if !foundMatchingBid {
			t.Fatalf("did not find matching bid output for ask "+
				"output of order %v", askOutput.Order.Nonce())
		}
	}

	// Continuing, for each trader, we should be able to easily locate all
	// the orders that pertain to that trader. We'll start with askers.
	for trader, orderNonces := range ordersForAskers {
		traderOutputs, ok := batchTxCtx.ChanOutputsForTrader(trader)
		if !ok {
			t.Fatalf("unable to find output for trader: %x", trader)
		}

		// Now that we know this trader has an entry, ensure that all
		// the expected order outputs are found in the index.
		if len(traderOutputs) != len(orderNonces) {
			t.Fatalf("expected %v outputs for trader, instead got %v",
				len(orderNonces), len(traderOutputs))
		}
		for _, chanOutput := range traderOutputs {
			if _, ok := orderNonces[chanOutput.Order.Nonce()]; !ok {
				t.Fatalf("unexpected order output found: %v",
					chanOutput.Order.Nonce())
			}
		}
	}
	// We'll apply the same checks for bidders, but bidders have double the
	// amount of outputs since each bid is matched to two asks.
	for trader, orderNonces := range ordersForBidders {
		traderOutputs, ok := batchTxCtx.ChanOutputsForTrader(trader)
		if !ok {
			t.Fatalf("unable to find output for trader: %x", trader)
		}

		// Now that we know this trader has an entry, ensure that all
		// the expected order outputs are found in the index.
		if len(traderOutputs) != len(orderNonces)*2 {
			t.Fatalf("expected %v outputs for trader, instead got %v",
				len(orderNonces)*2, len(traderOutputs))
		}
		for _, chanOutput := range traderOutputs {
			nonce := chanOutput.Order.Nonce()
			if _, ok := orderNonces[nonce]; !ok {
				t.Fatalf("unexpected order output found: %v",
					nonce)
			}
		}
	}

	// Next, we'll ensure that each trader has an entry in the chain fee
	// index and account input index.
	for _, trader := range traders {
		if _, ok := batchTxCtx.AcctInputForTrader(trader.TraderKeyRaw); !ok {
			t.Fatalf("acct input entry for %x found",
				trader.TraderKeyRaw[:])
		}
	}

	// Finally we'll make sure the master account output diff matches
	// what's present in the batch transaction.
	masterOutputIndex := batchTxCtx.MasterAccountDiff.OutPoint.Index
	realMasterOutput := batchTx.TxOut[masterOutputIndex]
	if realMasterOutput.Value != int64(batchTxCtx.MasterAccountDiff.AccountBalance) {
		t.Fatalf("master account output balances off: expected "+
			"%v got %v", realMasterOutput.Value,
			int64(batchTxCtx.MasterAccountDiff.AccountBalance))
	}
}

func newMatch(orderSize btcutil.Amount) (*matching.OrderPair, error) {
	var (
		askKey   [33]byte
		askNonce [32]byte
		bidKey   [33]byte
		bidNonce [32]byte
	)
	askerFundingKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, fmt.Errorf("unable to generate funding key: %v", err)
	}
	copy(askKey[:], askerFundingKey.PubKey().SerializeCompressed())
	copy(askNonce[:], askKey[:])
	ask := &order.Ask{
		Ask: orderT.Ask{
			Kit: *orderT.NewKit(askNonce),
		},
		Kit: order.Kit{
			MultiSigKey: askKey,
		},
	}

	bidderFundingKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, fmt.Errorf("unable to generate funding key: %v", err)
	}
	copy(bidKey[:], bidderFundingKey.PubKey().SerializeCompressed())
	copy(bidNonce[:], bidKey[:])
	bid := &order.Bid{
		Bid: orderT.Bid{
			Kit: *orderT.NewKit(bidNonce),
		},
		Kit: order.Kit{
			MultiSigKey: bidKey,
		},
	}

	return &matching.OrderPair{
		Bid: bid,
		Ask: ask,
		Quote: matching.PriceQuote{
			TotalSatsCleared: orderSize,
		},
	}, nil

}

// TestBatchTransactionDustAccounts assures that account outputs that are below
// the dust limit does no materialize on the batch transaction.
func TestBatchTransactionDustAccounts(t *testing.T) {
	t.Parallel()

	// For simplicity, we'll use the same clearing price of 1% for the
	// entire batch.
	const clearingPrice = orderT.FixedRatePremium(10000)
	feeSchedule := mockFeeSchedule{
		baseFee:    1,
		exeFeeRate: orderT.FixedRatePremium(10000),
	}

	acctValue := btcutil.Amount(btcutil.SatoshiPerBitcoin)
	numRandTraders := 6

	// First, we'll generate a series of random traders. Each trader will
	// have the same account size to make our calculations below much
	// easier.
	traders := make([]*account.Account, numRandTraders)
	for i := 0; i < numRandTraders; i++ {
		traderKey, err := btcec.NewPrivateKey(btcec.S256())
		if err != nil {
			t.Fatalf("unable to generate trader key: %v", err)
		}

		acct := &account.Account{
			Value:    acctValue,
			Expiry:   2016,
			BatchKey: traderKey.PubKey(),
		}
		traderKeyBytes := traderKey.PubKey().SerializeCompressed()
		copy(acct.TraderKeyRaw[:], traderKeyBytes)
		copy(acct.Secret[:], traderKeyBytes)
		copy(acct.OutPoint.Hash[:], traderKeyBytes)

		traders[i] = acct
	}

	// We'll create orders of a size that will make an account dust if it
	// gets two asks matched.
	orderSize := (acctValue - 1500) / 2

	// We let one trader get two asks filled, and one trader get one fill.
	orderBatch := matching.EmptyBatch()
	for i := 0; i < numRandTraders/2; i++ {
		numChans := uint32(i)
		asker := traders[i]
		bidder := traders[numRandTraders-1-i]

		for j := 0; j < int(numChans); j++ {
			match, err := newMatch(orderSize)
			if err != nil {
				t.Fatalf("unable to create match: %v", err)
			}

			orderBatch.Orders = append(orderBatch.Orders,
				matching.MatchedOrder{
					Asker:   matching.NewTraderFromAccount(asker),
					Bidder:  matching.NewTraderFromAccount(bidder),
					Details: *match,
				})
		}
	}

	// To complete our test batch, we'll generate an actual trading report,
	// and also supply the clearing price of 1% that we use in our tests to
	// make things easy.
	orderBatch.FeeReport = matching.NewTradingFeeReport(
		orderBatch.Orders, &feeSchedule, clearingPrice,
	)
	orderBatch.ClearingPrice = clearingPrice

	// With all our set up done, we'll now create our master account diff,
	// then construct the batch transaction.
	priorAccountPoint := wire.OutPoint{}
	auctPubKey := auctioneerKey.PubKey().SerializeCompressed()
	copy(priorAccountPoint.Hash[:], auctPubKey)
	masterAcct := &account.Auctioneer{
		OutPoint: priorAccountPoint,
		Balance:  acctValue * 10,
		AuctioneerKey: &keychain.KeyDescriptor{
			PubKey: auctioneerKey.PubKey(),
		},
	}
	batchKey := auctioneerKey.PubKey()

	// Now we can being the real meat of our tests: ensuring the batch
	// transaction and all the relevant indexes ere constructed properly.
	feeRate := chainfee.SatPerKWeight(200)
	batchTxCtx, err := NewExecutionContext(
		batchKey, orderBatch, masterAcct, feeRate, &feeSchedule,
	)
	if err != nil {
		t.Fatalf("unable to construct batch tx: %v", err)
	}

	if batchTxCtx.FeeInfoEstimate.FeeRate() != feeRate {
		t.Fatalf("assembled tx had wrong feerate %v, wanted %v",
			batchTxCtx.FeeInfoEstimate.FeeRate(), feeRate)
	}

	// Check that the batch tx has the expected outputs and inputs.
	batchTx := batchTxCtx.ExeTx
	for i := 0; i < numRandTraders; i++ {
		trader := traders[i]

		// There are three traders that should not have an account
		// output on the resulting tx. The first and last trader was
		// not part of any match, the third trader got two asks filled
		// and got a dust balance as a result.
		expOut := i != 0 && i != numRandTraders-1 && i != 2
		_, ok := batchTxCtx.AcctOutputForTrader(
			trader.TraderKeyRaw,
		)

		if expOut && !ok {
			t.Fatalf("unable to find acct output for: %x",
				trader.TraderKeyRaw[:])
		}
		if !expOut && ok {
			t.Fatalf("did not expect acct output for: %x",
				trader.TraderKeyRaw[:])
		}

		// Next, we'll ensure that each trader that was involved in a
		// match has an entry in the account input index.
		expIn := i != 0 && i != numRandTraders-1
		_, ok = batchTxCtx.AcctInputForTrader(trader.TraderKeyRaw)
		if expIn && !ok {
			t.Fatalf("acct input entry for %x not found",
				trader.TraderKeyRaw[:])
		}

		if !expIn && ok {
			t.Fatalf("acct input entry for %x found",
				trader.TraderKeyRaw[:])
		}
	}

	// Next, for each order within the batch, we should be able to find an
	// output in the context that actually executes the order (creates the
	// channel).
	for _, order := range orderBatch.Orders {
		ask := order.Details.Ask
		bid := order.Details.Bid

		askOutputs, ok := batchTxCtx.OutputsForOrder(ask.Nonce())
		if !ok {
			t.Fatalf("unable to find output for ask in batch " +
				"ctx")
		}
		if len(askOutputs) != 1 {
			t.Fatal("expected one output per ask")
		}
		askOutput := askOutputs[0]
		bidOutputs, ok := batchTxCtx.OutputsForOrder(bid.Nonce())
		if !ok {
			t.Fatalf("unable to find output for bid in batch " +
				"ctx")
		}
		if len(bidOutputs) != 1 {
			t.Fatal("expected one output per bid")
		}
		bidOutput := bidOutputs[0]

		// Each ask output should have a matching bid output.

		// The output for the bid and the ask should actually be
		// pointing to the exact same output.
		if askOutput.OutPoint != bidOutput.OutPoint {
			t.Fatalf("outpoint mismatch")
		}
		if !reflect.DeepEqual(askOutput.TxOut, bidOutput.TxOut) {
			t.Fatalf("txOut mismatch")
		}

		// The output as found in batch transaction should also
		// match what is in the index.
		realAskOutput := batchTx.TxOut[askOutput.OutPoint.Index]
		realBidOutput := batchTx.TxOut[bidOutput.OutPoint.Index]
		if !reflect.DeepEqual(askOutput.TxOut, realAskOutput) {
			t.Fatalf("real ask txout mismatch")
		}
		if !reflect.DeepEqual(askOutput.TxOut, realBidOutput) {
			t.Fatalf("real bid txout mismatch")
		}
	}

	// Finally we'll make sure the master account output diff matches
	// what's present in the batch transaction.
	masterOutputIndex := batchTxCtx.MasterAccountDiff.OutPoint.Index
	realMasterOutput := batchTx.TxOut[masterOutputIndex]
	if realMasterOutput.Value != int64(batchTxCtx.MasterAccountDiff.AccountBalance) {
		t.Fatalf("master account output balances off: expected "+
			"%v got %v", realMasterOutput.Value,
			int64(batchTxCtx.MasterAccountDiff.AccountBalance))
	}
}

// TestBatchTxPoorTrader checks we get back ErrPoorTrader if a trader cannot
// pay its chain fees.
func TestBatchTxPoorTrader(t *testing.T) {
	t.Parallel()

	const clearingPrice = orderT.FixedRatePremium(10000)
	feeSchedule := mockFeeSchedule{
		baseFee:    1,
		exeFeeRate: orderT.FixedRatePremium(10000),
	}

	// We'll just do a simple batch with two traders, one match.
	acctValue := btcutil.Amount(btcutil.SatoshiPerBitcoin)
	numRandTraders := 2

	orderBatch := matching.EmptyBatch()

	// Genarate the two traders.
	traders := make([]*account.Account, numRandTraders)
	for i := 0; i < numRandTraders; i++ {
		traderKey, err := btcec.NewPrivateKey(btcec.S256())
		if err != nil {
			t.Fatalf("unable to generate trader key: %v", err)
		}

		acct := &account.Account{
			Value:    acctValue,
			Expiry:   2016,
			BatchKey: traderKey.PubKey(),
		}
		traderKeyBytes := traderKey.PubKey().SerializeCompressed()
		copy(acct.TraderKeyRaw[:], traderKeyBytes)
		copy(acct.Secret[:], traderKeyBytes)
		copy(acct.OutPoint.Hash[:], traderKeyBytes)

		traders[i] = acct
	}

	asker := traders[0]
	bidder := traders[1]

	// Next, create a bid and an ask.
	orderSize := acctValue

	var (
		askKey   [33]byte
		askNonce [32]byte
	)
	askerFundingKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatalf("unable to generate funding key: %v", err)
	}
	copy(askKey[:], askerFundingKey.PubKey().SerializeCompressed())
	copy(askNonce[:], askKey[:])
	ask := &order.Ask{
		Ask: orderT.Ask{
			Kit: *orderT.NewKit(askNonce),
		},
		Kit: order.Kit{
			MultiSigKey: askKey,
		},
	}

	var (
		bidKey   [33]byte
		bidNonce [32]byte
	)
	bidderFundingKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatalf("unable to generate funding key: %v", err)
	}
	copy(bidKey[:], bidderFundingKey.PubKey().SerializeCompressed())
	copy(bidNonce[:], bidKey[:])
	bid := &order.Bid{
		Bid: orderT.Bid{
			Kit: *orderT.NewKit(bidNonce),
		},
		Kit: order.Kit{
			MultiSigKey: bidKey,
		},
	}

	orderBatch.Orders = append(orderBatch.Orders, matching.MatchedOrder{
		Asker:  matching.NewTraderFromAccount(asker),
		Bidder: matching.NewTraderFromAccount(bidder),
		Details: matching.OrderPair{
			Bid: bid,
			Ask: ask,
			Quote: matching.PriceQuote{
				TotalSatsCleared: orderSize,
			},
		},
	})

	// To complete our test batch, we'll generate an actual trading report,
	// and also supply the clearing price of 1% that we use in our tests to
	// make things easy.
	orderBatch.FeeReport = matching.NewTradingFeeReport(
		orderBatch.Orders, &feeSchedule, clearingPrice,
	)
	orderBatch.ClearingPrice = clearingPrice

	// With all our set up done, we'll now create our master account diff,
	// then construct the batch transaction.
	priorAccountPoint := wire.OutPoint{}
	auctPubKey := auctioneerKey.PubKey().SerializeCompressed()
	copy(priorAccountPoint.Hash[:], auctPubKey)
	masterAcct := &account.Auctioneer{
		OutPoint: priorAccountPoint,
		Balance:  acctValue * 10,
		AuctioneerKey: &keychain.KeyDescriptor{
			PubKey: auctioneerKey.PubKey(),
		},
	}
	batchKey := auctioneerKey.PubKey()

	// Attempt to assemble a batch TX, this should fail! (since the order
	// size is the same as the account size).
	feeRate := chainfee.SatPerKWeight(200)
	_, err = NewExecutionContext(
		batchKey, orderBatch, masterAcct, feeRate, &feeSchedule,
	)
	if err == nil {
		t.Fatalf("expected error")
	}

	// Check we get the error we expect, with the correct trader (the
	// asker) detected.
	feeErr, ok := err.(*ErrPoorTrader)
	if !ok {
		t.Fatalf("Expected ErrPoorTrader, got %v", err)
	}

	if feeErr.Account != asker.TraderKeyRaw {
		t.Fatalf("expected %x got %x", asker.TraderKeyRaw, feeErr.Account)
	}
}
