package batchtx

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
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

var _ orderT.FeeSchedule = (*mockFeeSchedule)(nil)

// TestBatchTransactionAssembly tests that given a valid set of parameters,
// we're able to construct a complete batch transaction. All relevant outputs
// should be present in the transaction, and all our indexes should be
// populated and point to the correct outputs.
func TestBatchTransactionAssembly(t *testing.T) {
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

	orderBatch := &matching.OrderBatch{}

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

	// Next, we'll create a set of random orders, picking two of the
	// traders at random to match an order with.
	numOrders := 50
	orderSize := btcutil.Amount(1_000_000)
	orderBatch.Orders = make([]matching.MatchedOrder, numOrders)
	ordersForTraders := make(map[matching.AccountID]map[orderT.Nonce]struct{})
	for i := 0; i < numOrders; i++ {
		asker := traders[rand.Intn(numRandTraders)]
		bidder := traders[rand.Intn(numRandTraders)]

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

		orderBatch.Orders[i] = matching.MatchedOrder{
			Asker:  matching.NewTraderFromAccount(asker),
			Bidder: matching.NewTraderFromAccount(bidder),
			Details: matching.OrderPair{
				Bid: &order.Bid{
					Bid: orderT.Bid{
						Kit: *orderT.NewKit(bidNonce),
					},
					Kit: order.Kit{
						MultiSigKey: bidKey,
					},
				},
				Ask: &order.Ask{
					Ask: orderT.Ask{
						Kit: *orderT.NewKit(askNonce),
					},
					Kit: order.Kit{
						MultiSigKey: askKey,
					},
				},
				Quote: matching.PriceQuote{
					TotalSatsCleared: orderSize,
				},
			},
		}

		if _, ok := ordersForTraders[asker.TraderKeyRaw]; !ok {
			ordersForTraders[asker.TraderKeyRaw] = make(map[orderT.Nonce]struct{})
		}
		if _, ok := ordersForTraders[bidder.TraderKeyRaw]; !ok {
			ordersForTraders[bidder.TraderKeyRaw] = make(map[orderT.Nonce]struct{})
		}
		ordersForTraders[asker.TraderKeyRaw][askNonce] = struct{}{}
		ordersForTraders[bidder.TraderKeyRaw][bidNonce] = struct{}{}
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
	mad := &MasterAccountState{
		PriorPoint:     priorAccountPoint,
		AccountBalance: btcutil.Amount(acctValue * 10),
	}
	copy(mad.BatchKey[:], auctPubKey)
	copy(mad.AuctioneerKey[:], auctPubKey)

	// Now we can being the real meat of our tests: ensuring the batch
	// transaction and all the relevant indexes ere constructed properly.
	batchTxCtx, err := New(orderBatch, mad, chainfee.SatPerKWeight(200))
	if err != nil {
		t.Fatalf("unable to construct batch tx: %v", err)
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

		askOutput, ok := batchTxCtx.OutputForOrder(ask.Nonce())
		if !ok {
			t.Fatalf("unable to find output for ask in batch " +
				"ctx")
		}
		bidOutput, ok := batchTxCtx.OutputForOrder(bid.Nonce())
		if !ok {
			t.Fatalf("unable to find output for bid in batch " +
				"ctx")
		}

		// The output for the bid and the ask should actually be
		// pointing to the exact same output.
		if askOutput.OutPoint != bidOutput.OutPoint {
			t.Fatalf("outpoint for matched ask+bid don't "+
				"match: %v s %v",
				spew.Sdump(askOutput.OutPoint),
				spew.Sdump(bidOutput.OutPoint))
		}
		if !reflect.DeepEqual(askOutput.TxOut, bidOutput.TxOut) {
			t.Fatalf("outputs for matched ask+bid don't "+
				"match: %v s %v",
				spew.Sdump(askOutput.TxOut),
				spew.Sdump(bidOutput.TxOut))
		}

		// The output as found in batch transaction should also match
		// what is in the index.
		realAskOutput := batchTx.TxOut[askOutput.OutPoint.Index]
		realBidOutput := batchTx.TxOut[bidOutput.OutPoint.Index]
		if !reflect.DeepEqual(askOutput.TxOut, realAskOutput) {
			t.Fatalf("outputs for matched ask+bid don't "+
				"match: %v s %v",
				spew.Sdump(askOutput.TxOut),
				spew.Sdump(realAskOutput))
		}
		if !reflect.DeepEqual(askOutput.TxOut, realBidOutput) {
			t.Fatalf("outputs for matched ask+bid don't "+
				"match: %v s %v",
				spew.Sdump(askOutput.TxOut),
				spew.Sdump(realBidOutput))
		}
	}

	// Continuing, for each trader, we should be able to easily locate all
	// the orders that pertain to that trader.
	for trader, orderNonces := range ordersForTraders {
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
				t.Fatalf("unexpected order output found: %x",
					chanOutput.Order.Nonce())
			}
		}
	}

	// Next, we'll ensure that each trader has an entry in the chain fee
	// index and account input index.
	for _, trader := range traders {
		if _, ok := batchTxCtx.ChainFeeForTrader(trader.TraderKeyRaw); !ok {
			t.Fatalf("no chain fee entry for %x found",
				trader.TraderKeyRaw[:])
		}
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
