package venue

import (
	"bytes"
	"context"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	accountT "github.com/lightninglabs/agora/client/account"
	"github.com/lightninglabs/agora/client/clmscript"
	orderT "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
	"github.com/lightninglabs/agora/venue/batchtx"
	"github.com/lightninglabs/agora/venue/matching"
)

var (
	_, startBatchKey = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x01})
	_, acctKeyBig    = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x02})
	_, acctKeySmall  = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x03})
	oldMasterOutHash = chainhash.Hash{0x01}
	newMasterOutHash = chainhash.Hash{0x02}
)

// TestBatchStorer makes sure a batch is prepared correctly for serialization by
// the batch storer.
func TestBatchStorer(t *testing.T) {
	t.Parallel()

	var (
		storeMock   = agoradb.NewStoreMock(t)
		storer      = &batchStorer{store: storeMock}
		batchID     orderT.BatchID
		acctIDBig   matching.AccountID
		acctIDSmall matching.AccountID
	)
	copy(batchID[:], startBatchKey.SerializeCompressed())
	copy(acctIDBig[:], acctKeyBig.SerializeCompressed())
	copy(acctIDSmall[:], acctKeySmall.SerializeCompressed())

	// We'll create two accounts: A smaller one that has one ask for 4 units
	// that will be completely used up. Then a larger account that has two
	// bids that are both matched to the ask. This account is large enough
	// to be recreated. We assume here that no maker/taker fees are applied
	// and only the matched units are paid for.
	bigAcct := &account.Account{
		TraderKeyRaw: acctIDBig,
		Value:        1_000_000,
		Expiry:       144,
		State:        account.StateOpen,
		BatchKey:     startBatchKey,
	}
	bigTrader := matching.NewTraderFromAccount(bigAcct)
	smallAcct := &account.Account{
		TraderKeyRaw: acctIDSmall,
		Value:        400_000,
		Expiry:       144,
		State:        account.StateOpen,
		BatchKey:     startBatchKey,
	}
	smallTrader := matching.NewTraderFromAccount(smallAcct)
	ask := &order.Ask{
		Ask: orderT.Ask{
			Kit: newClientKit(orderT.Nonce{0x01}, 4),
		},
	}
	bid1 := &order.Bid{
		Bid: orderT.Bid{
			Kit: newClientKit(orderT.Nonce{0x02}, 2),
		},
	}
	bid2 := &order.Bid{
		Bid: orderT.Bid{
			Kit: newClientKit(orderT.Nonce{0x03}, 8),
		},
	}
	batchTx := &wire.MsgTx{
		Version: 2,
		TxOut: []*wire.TxOut{{
			Value:    600_000,
			PkScript: []byte{77, 88, 99},
		}},
	}
	accountDiffs := map[matching.AccountID]matching.AccountDiff{
		bigAcct.TraderKeyRaw: {
			StartingState:   &bigTrader,
			RecreatedOutput: batchTx.TxOut[0],
			AccountTally: &orderT.AccountTally{
				EndingBalance: 600_000,
				Account: &accountT.Account{
					Expiry: bigAcct.Expiry,
				},
			},
		},
		smallAcct.TraderKeyRaw: {
			StartingState: &smallTrader,
			AccountTally: &orderT.AccountTally{
				EndingBalance: 0,
				Account: &accountT.Account{
					Expiry: smallAcct.Expiry,
				},
			},
		},
	}
	oldMasterAccount := &account.Auctioneer{
		OutPoint: wire.OutPoint{Hash: oldMasterOutHash},
		Balance:  1337,
		BatchKey: batchID,
	}
	batchResult := &ExecutionResult{
		Batch: &matching.OrderBatch{
			Orders: []matching.MatchedOrder{
				{
					Details: matching.OrderPair{
						Ask: ask,
						Bid: bid1,
						Quote: matching.PriceQuote{
							UnitsMatched: 2,
						},
					},
				},
				{
					Details: matching.OrderPair{
						Ask: ask,
						Bid: bid2,
						Quote: matching.PriceQuote{
							UnitsMatched: 2,
						},
					},
				},
			},
			FeeReport: matching.TradingFeeReport{
				AccountDiffs: accountDiffs,
			},
		},
		MasterAccountDiff: &batchtx.MasterAccountState{
			PriorPoint:     oldMasterAccount.OutPoint,
			OutPoint:       &wire.OutPoint{Hash: newMasterOutHash},
			AccountBalance: 999,
		},
		BatchID: batchID,
		BatchTx: batchTx,
	}

	// Create the starting database state now.
	storeMock.Accs = map[[33]byte]*account.Account{
		bigAcct.TraderKeyRaw:   bigAcct,
		smallAcct.TraderKeyRaw: smallAcct,
	}
	storeMock.Orders = map[orderT.Nonce]order.ServerOrder{
		ask.Nonce():  ask,
		bid1.Nonce(): bid1,
		bid2.Nonce(): bid2,
	}
	storeMock.MasterAcct = oldMasterAccount

	// Pass the assembled batch to the storer now.
	err := storer.Store(context.Background(), batchResult)
	if err != nil {
		t.Fatalf("error storing batch: %v", err)
	}

	// Because the store backend is an in-memory mock, all modifications are
	// performed on the actual instances, which makes it easy to check.
	// Check the order states first.
	if ask.State != orderT.StateExecuted {
		t.Fatalf("invalid order state, got %d wanted %d",
			ask.State, orderT.StateExecuted)
	}
	if ask.UnitsUnfulfilled != 0 {
		t.Fatalf("invalid units unfulfilled, got %d wanted %d",
			ask.UnitsUnfulfilled, 0)
	}
	if bid1.State != orderT.StateExecuted {
		t.Fatalf("invalid order state, got %d wanted %d",
			bid1.State, orderT.StateExecuted)
	}
	if bid1.UnitsUnfulfilled != 0 {
		t.Fatalf("invalid units unfulfilled, got %d wanted %d",
			bid1.UnitsUnfulfilled, 0)
	}
	if bid2.State != orderT.StatePartiallyFilled {
		t.Fatalf("invalid order state, got %d wanted %d",
			bid2.State, orderT.StatePartiallyFilled)
	}
	if bid2.UnitsUnfulfilled != 6 {
		t.Fatalf("invalid units unfulfilled, got %d wanted %d",
			bid2.UnitsUnfulfilled, 6)
	}

	// Check the account states next.
	if smallAcct.State != account.StateClosed {
		t.Fatalf("invalid account state, got %d wanted %d",
			smallAcct.State, account.StateClosed)
	}
	if smallAcct.Value != 0 {
		t.Fatalf("invalid account balance, got %d wanted %d",
			smallAcct.Value, 0)
	}
	if smallAcct.Expiry != 144 {
		t.Fatalf("invalid account expiry, got %d wanted %d",
			smallAcct.Value, 144)
	}

	if bigAcct.State != account.StatePendingOpen {
		t.Fatalf("invalid account state, got %d wanted %d",
			bigAcct.State, account.StatePendingOpen)
	}
	if bigAcct.Value != 600_000 {
		t.Fatalf("invalid account balance, got %d wanted %d",
			bigAcct.Value, 600_000)
	}
	if bigAcct.Expiry != 144 {
		t.Fatalf("invalid account expiry, got %d wanted %d",
			bigAcct.Value, 144)
	}

	// Finally, check the auctioneer/master account and batch key.
	if oldMasterAccount.OutPoint.Hash != newMasterOutHash {
		t.Fatalf("invalid master account outpoint hash, got %x "+
			"wanted %s", oldMasterAccount.OutPoint.Hash,
			newMasterOutHash)
	}
	if oldMasterAccount.Balance != 999 {
		t.Fatalf("invalid master account balance, got %d wanted %d",
			oldMasterAccount.Balance, 999)
	}
	newBatchKey := clmscript.IncrementKey(startBatchKey)
	keysEqual := bytes.Equal(
		oldMasterAccount.BatchKey[:], newBatchKey.SerializeCompressed(),
	)
	if !keysEqual {
		t.Fatalf("invalid batch key, got %x wanted %x",
			oldMasterAccount.BatchKey,
			newBatchKey.SerializeCompressed())
	}
}

func newClientKit(nonce orderT.Nonce, units orderT.SupplyUnit) orderT.Kit {
	kit := orderT.NewKit(nonce)
	kit.Units = units
	kit.UnitsUnfulfilled = units
	kit.State = orderT.StateSubmitted
	return *kit
}
