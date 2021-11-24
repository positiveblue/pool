package venue

import (
	"bytes"
	"context"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/feebump"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/keychain"
)

var (
	batchPriv, startBatchKey    = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x01})
	acctBigPriv, acctKeyBig     = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x02})
	acctSmallPriv, acctKeySmall = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x03})
	acctMedPriv, acctKeyMed     = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x04})
	oldMasterOutHash            = chainhash.Hash{0x01}
	newMasterOutHash            = chainhash.Hash{0x02}

	batchID     = orderT.NewBatchID(startBatchKey)
	acctIDBig   = matching.NewAccountID(acctKeyBig)
	acctIDSmall = matching.NewAccountID(acctKeySmall)
	acctIDMed   = matching.NewAccountID(acctKeyMed)

	acctIDToPriv = map[matching.AccountID]*btcec.PrivateKey{
		acctIDBig:   acctBigPriv,
		acctIDSmall: acctSmallPriv,
		acctIDMed:   acctMedPriv,
	}

	testLeaseDuration = orderT.LegacyLeaseDurationBucket
)

// As set up for all tests in this package, we'll create two accounts: A
// smaller one that has one ask for 4 units that will be completely used up.
// Then a larger account that has two bids that are both matched to the ask.
// This account is large enough to be recreated. We assume here that no
// maker/taker fees are applied and only the matched units are paid for.
var (
	bigAcct = &account.Account{
		TraderKeyRaw: acctIDBig,
		AuctioneerKey: &keychain.KeyDescriptor{
			PubKey: startBatchKey,
		},
		Value:    1_000_000,
		Expiry:   144,
		State:    account.StateOpen,
		BatchKey: startBatchKey,
		OutPoint: wire.OutPoint{
			Hash: chainhash.Hash{0x01, 0x01},
		},
		LatestTx: wire.NewMsgTx(2),
	}

	bigTrader = matching.NewTraderFromAccount(bigAcct)

	smallAcct = &account.Account{
		TraderKeyRaw: acctIDSmall,
		AuctioneerKey: &keychain.KeyDescriptor{
			PubKey: startBatchKey,
		},
		Value:    400_000,
		Expiry:   144,
		State:    account.StateOpen,
		BatchKey: startBatchKey,
		OutPoint: wire.OutPoint{
			Hash: chainhash.Hash{0x01, 0x09},
		},
		LatestTx: wire.NewMsgTx(2),
	}

	smallTrader = matching.NewTraderFromAccount(smallAcct)

	medAcct = &account.Account{
		TraderKeyRaw: acctIDMed,
		AuctioneerKey: &keychain.KeyDescriptor{
			PubKey: startBatchKey,
		},
		Value:    700_000,
		Expiry:   144,
		State:    account.StateOpen,
		BatchKey: startBatchKey,
		OutPoint: wire.OutPoint{
			Hash: chainhash.Hash{0x01, 0x07},
		},
		LatestTx: wire.NewMsgTx(2),
	}

	medTrader = matching.NewTraderFromAccount(medAcct)

	ask = &order.Ask{
		Ask: orderT.Ask{
			Kit: newClientKit(orderT.Nonce{0x01}, 4, 1),
		},
		Kit: order.Kit{
			MultiSigKey: batchID,
		},
	}

	bid1 = &order.Bid{
		Bid: orderT.Bid{
			Kit: newClientKit(orderT.Nonce{0x02}, 3, 1),
		},
		Kit: order.Kit{
			MultiSigKey: acctIDSmall,
		},
	}

	bid2 = &order.Bid{
		Bid: orderT.Bid{
			Kit: newClientKit(orderT.Nonce{0x03}, 3, 2),
		},
		Kit: order.Kit{
			MultiSigKey: acctIDBig,
		},
	}

	batchTx = &wire.MsgTx{
		Version: 2,
		TxOut: []*wire.TxOut{{
			Value:    600_000,
			PkScript: []byte{77, 88, 99},
		}},
	}

	accountDiffs = map[matching.AccountID]matching.AccountDiff{
		bigAcct.TraderKeyRaw: {
			StartingState:   &bigTrader,
			RecreatedOutput: batchTx.TxOut[0],
			AccountTally: &orderT.AccountTally{
				EndingBalance: 600_000,
			},
		},
		smallAcct.TraderKeyRaw: {
			StartingState: &smallTrader,
			AccountTally: &orderT.AccountTally{
				EndingBalance: 434,
			},
		},
	}

	oldMasterAccount = &account.Auctioneer{
		AuctioneerKey: &keychain.KeyDescriptor{
			PubKey: startBatchKey,
		},
		OutPoint: wire.OutPoint{Hash: oldMasterOutHash},
		Balance:  1337,
		BatchKey: batchID,
	}

	orders = []matching.MatchedOrder{{
		Details: matching.OrderPair{
			Ask: ask,
			Bid: bid1,
			Quote: matching.PriceQuote{
				UnitsMatched:     2,
				TotalSatsCleared: 2,
			},
		},
		Asker:  smallTrader,
		Bidder: bigTrader,
	}, {
		Details: matching.OrderPair{
			Ask: ask,
			Bid: bid2,
			Quote: matching.PriceQuote{
				UnitsMatched:     2,
				TotalSatsCleared: 2,
			},
		},
		Asker:  smallTrader,
		Bidder: bigTrader,
	}}
	feeReport = matching.TradingFeeReport{
		AccountDiffs: accountDiffs,
	}
	orderBatch = matching.NewBatch(
		map[uint32][]matching.MatchedOrder{
			testLeaseDuration: orders,
		}, feeReport, map[uint32]orderT.FixedRatePremium{
			testLeaseDuration: 1234,
		},
		CurrentServerBatchVersion,
	)
)

// TestBatchStorer makes sure a batch is prepared correctly for serialization by
// the batch storer.
func TestBatchStorer(t *testing.T) {
	var (
		storeMock = subastadb.NewStoreMock(t)
		storer    = &ExeBatchStorer{store: storeMock}
	)

	batchResult := &ExecutionResult{
		Batch: orderBatch,
		MasterAccountDiff: &batchtx.MasterAccountState{
			PriorPoint:     oldMasterAccount.OutPoint,
			OutPoint:       &wire.OutPoint{Hash: newMasterOutHash},
			AccountBalance: 999,
		},
		BatchID: batchID,
		BatchTx: batchTx,
		FeeInfo: &feebump.TxFeeInfo{
			Fee:    123,
			Weight: 5000,
		},
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
	if bid1.State != orderT.StatePartiallyFilled {
		t.Fatalf("invalid order state, got %v wanted %v",
			bid1.State, orderT.StatePartiallyFilled)
	}
	if bid1.UnitsUnfulfilled != 1 {
		t.Fatalf("invalid units unfulfilled, got %d wanted %d",
			bid1.UnitsUnfulfilled, 1)
	}
	if bid2.State != orderT.StateExecuted {
		t.Fatalf("invalid order state, got %d wanted %d",
			bid2.State, orderT.StateExecuted)
	}
	if bid2.UnitsUnfulfilled != 1 {
		t.Fatalf("invalid units unfulfilled, got %d wanted %d",
			bid2.UnitsUnfulfilled, 1)
	}

	// Check the account states next.
	if smallAcct.State != account.StateClosed {
		t.Fatalf("invalid account state, got %d wanted %d",
			smallAcct.State, account.StateClosed)
	}
	if smallAcct.Value != 434 {
		t.Fatalf("invalid account balance, got %d wanted %d",
			smallAcct.Value, 500)
	}
	if smallAcct.Expiry != 144 {
		t.Fatalf("invalid account expiry, got %d wanted %d",
			smallAcct.Value, 144)
	}

	if bigAcct.State != account.StatePendingBatch {
		t.Fatalf("invalid account state, got %d wanted %d",
			bigAcct.State, account.StatePendingBatch)
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
	newBatchKey := poolscript.IncrementKey(startBatchKey)
	keysEqual := bytes.Equal(
		oldMasterAccount.BatchKey[:], newBatchKey.SerializeCompressed(),
	)
	if !keysEqual {
		t.Fatalf("invalid batch key, got %x wanted %x",
			oldMasterAccount.BatchKey,
			newBatchKey.SerializeCompressed())
	}
}

func newClientKit(nonce orderT.Nonce, units, minUnitsMatch orderT.SupplyUnit) orderT.Kit {
	kit := orderT.NewKit(nonce)
	kit.Units = units
	kit.UnitsUnfulfilled = units
	kit.MinUnitsMatch = minUnitsMatch
	kit.State = orderT.StateSubmitted
	return *kit
}

func init() {
	ask.UnitsUnfulfilled = 0
	bid1.UnitsUnfulfilled = 1
	bid2.UnitsUnfulfilled = 1
}
