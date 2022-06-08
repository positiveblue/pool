package subastadb

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/stretchr/testify/require"
)

var (
	batchTx = &wire.MsgTx{
		Version: 2,
		TxIn: []*wire.TxIn{
			{
				PreviousOutPoint: wire.OutPoint{
					Index: 5,
				},
				SignatureScript: []byte("aaa"),
			},
		},
		TxOut: []*wire.TxOut{
			{
				Value:    4444,
				PkScript: []byte("ddd"),
			},
		},
	}

	// testEmptyBatch is a pre-multi-lease-duration buckets snapshot from
	// testnet that was empty due to fee bumping.
	testEmptyBatch = []byte{
		0x02, 0x00, 0x00, 0x00, 0x00, 0x01, 0x01, 0x92, 0xbd, 0x8f,
		0x4d, 0x9e, 0x08, 0x1f, 0x0b, 0x02, 0x98, 0xc9, 0xba, 0x76,
		0xe8, 0x1c, 0x63, 0x77, 0xdc, 0x8d, 0x13, 0xc6, 0x13, 0x42,
		0x52, 0xcf, 0x54, 0x57, 0x1c, 0x07, 0xd6, 0xcd, 0x1b, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0xfb,
		0x4f, 0x0f, 0x00, 0x00, 0x00, 0x00, 0x00, 0x22, 0x00, 0x20,
		0x04, 0xfe, 0xc1, 0x63, 0xed, 0xe4, 0xdf, 0xef, 0x21, 0x93,
		0xe8, 0xc1, 0x11, 0xb5, 0xe4, 0xbb, 0x83, 0xb3, 0x2e, 0xaa,
		0xf0, 0x2d, 0x5a, 0x4a, 0x36, 0x0c, 0x4c, 0x8c, 0x67, 0x1c,
		0x33, 0x62, 0x02, 0x48, 0x30, 0x45, 0x02, 0x21, 0x00, 0xb0,
		0xf6, 0x98, 0xb9, 0xea, 0x33, 0x62, 0x48, 0x03, 0x88, 0x2a,
		0x8d, 0x0b, 0x27, 0x29, 0x37, 0xac, 0xf7, 0x73, 0xa4, 0x97,
		0xe7, 0x0e, 0x84, 0x3c, 0x10, 0xcb, 0x24, 0x6a, 0x08, 0x18,
		0x74, 0x02, 0x20, 0x42, 0x2d, 0xdc, 0x73, 0x4a, 0xb4, 0x6b,
		0x26, 0xd2, 0x55, 0x54, 0x3c, 0x66, 0xbf, 0xc4, 0x28, 0xa5,
		0xe0, 0xb0, 0x51, 0xc1, 0x12, 0x44, 0x98, 0xe9, 0x22, 0xaf,
		0xd0, 0xaa, 0xbf, 0x19, 0xd7, 0x01, 0x23, 0x21, 0x02, 0x95,
		0x68, 0x51, 0x11, 0x55, 0x9a, 0xeb, 0x16, 0x45, 0x94, 0xb2,
		0x6d, 0x8f, 0xd1, 0x2f, 0x69, 0x1f, 0xaa, 0xb7, 0xe2, 0x93,
		0x11, 0xbc, 0xe2, 0x5c, 0xdf, 0xf5, 0xb1, 0x33, 0x12, 0x17,
		0xe4, 0xac, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x14, 0x89, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
)

// TestDecodeEmptyBatch tests that an empty batch (which has clearingPrice of 0)
// can still be correctly decoded.
func TestDecodeEmptyBatch(t *testing.T) {
	b, err := deserializeBatchSnapshot(bytes.NewReader(testEmptyBatch))
	require.NoError(t, err)

	// The empty batch didn't have a version or creation timestamp, so we
	// expect those fields to be set to the default values.
	require.Equal(t, b.OrderBatch.Version, orderT.BatchVersion(0))
	require.Equal(
		t, uint64(b.OrderBatch.CreationTimestamp.UnixNano()), uint64(0),
	)
}

// TestPersistBatchResult tests the different database operations that are
// performed during the persisting phase of a batch.
func TestPersistBatchResult(t *testing.T) {
	t.Parallel()

	var (
		ctx     = context.Background()
		batchID = orderT.BatchID{1, 2, 3}
	)
	store, cleanup := newTestStore(t)
	defer cleanup()

	// The store should be initialized with the initial batch key.
	batchKey, err := store.BatchKey(ctx)
	if err != nil {
		t.Fatalf("unable to retrieve initial batch key: %v", err)
	}
	if !batchKey.IsEqual(InitialBatchKey) {
		t.Fatalf("expected initial batch key %x, got %x",
			InitialBatchKey.SerializeCompressed(),
			batchKey.SerializeCompressed())
	}

	// First test the basic sanity tests that are performed.
	err = store.PersistBatchResult(
		ctx, []orderT.Nonce{{}}, nil, nil, nil, nil, batchID, nil, nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "order modifier") {
		t.Fatalf("expected order modifier length mismatch, got %v", err)
	}
	err = store.PersistBatchResult(
		ctx, nil, nil, []*btcec.PublicKey{{}}, nil, nil, batchID, nil,
		nil, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "account modifier") {
		t.Fatalf("expected account modifier length mismatch, got %v",
			err)
	}

	// Now prepare some test data that we are going to operate on. Begin
	// with an account.
	addDummyAccount(t, store)

	// Now store a test order.
	o1 := &order.Bid{
		Bid: orderT.Bid{
			Kit: *dummyClientOrder(t, 500000, 1337),
		},
		Kit: *dummyOrder(t),
	}
	err = store.SubmitOrder(ctx, o1)
	if err != nil {
		t.Fatalf("unable to store order: %v", err)
	}

	// And then last, store a dummy master account.
	ma1 := &account.Auctioneer{
		OutPoint: wire.OutPoint{
			Index: 5,
		},
		Balance:       1_000_000,
		AuctioneerKey: testAuctioneerKeyDesc,
	}
	copy(ma1.BatchKey[:], InitialBatchKey.SerializeCompressed())
	err = store.UpdateAuctioneerAccount(ctx, ma1)
	if err != nil {
		t.Fatalf("unable to update auctioneer account: %v", err)
	}

	// Now prepare the updates we are going to apply.
	orderModifiers := [][]order.Modifier{
		{order.StateModifier(orderT.StatePartiallyFilled)},
	}
	accountModifiers := [][]account.Modifier{{
		account.StateModifier(account.StateClosed),
		account.LatestTxModifier(&wire.MsgTx{
			TxIn: []*wire.TxIn{{
				Witness: wire.TxWitness{
					nil,
					[]byte("trader sig"),
					[]byte("witness script"),
				},
			}},
		}),
	}}
	ma1.Balance = 500_000

	// A channel lifetime package should exist for each matched order.
	lifetimePkg := &chanenforcement.LifetimePackage{
		ChannelPoint:        wire.OutPoint{Index: 1},
		ChannelScript:       []byte{0x1, 0x2, 0x3},
		HeightHint:          1,
		MaturityHeight:      100,
		Version:             0,
		AskAccountKey:       randomPubKey(t),
		BidAccountKey:       randomPubKey(t),
		AskNodeKey:          randomPubKey(t),
		BidNodeKey:          randomPubKey(t),
		AskPaymentBasePoint: randomPubKey(t),
		BidPaymentBasePoint: randomPubKey(t),
	}
	lifetimePkgs := []*chanenforcement.LifetimePackage{lifetimePkg}

	nextBatchKey := poolscript.IncrementKey(batchKey)

	// Then call the actual persist method on the store.
	err = store.PersistBatchResult(
		ctx, []orderT.Nonce{o1.Nonce()}, orderModifiers,
		[]*btcec.PublicKey{testTraderKey}, accountModifiers,
		ma1, batchID, &BatchSnapshot{
			batchTx, 0,
			matching.EmptyBatch(orderT.DefaultBatchVersion),
		}, nextBatchKey, lifetimePkgs,
	)
	if err != nil {
		t.Fatalf("error persisting batch result: %v", err)
	}

	// Check batch key was updated.
	batchKey, err = store.BatchKey(ctx)
	if err != nil {
		t.Fatalf("unable to retrieve current batch key: %v", err)
	}
	if !batchKey.IsEqual(nextBatchKey) {
		t.Fatalf("expected updated batch key %x, got %x",
			nextBatchKey.SerializeCompressed(),
			batchKey.SerializeCompressed())
	}

	// And finally validate that all changes have been written correctly.
	// Start with the account.
	a2, err := store.Account(ctx, testTraderKey, false)
	if err != nil {
		t.Fatalf("error getting account: %v", err)
	}
	if a2.State != account.StateClosed {
		t.Fatalf("unexpected account state, got %d wanted %d",
			a2.State, account.StateClosed)
	}

	// Next check the order.
	o2, err := store.GetOrder(ctx, o1.Nonce())
	if err != nil {
		t.Fatalf("error getting order: %v", err)
	}
	if o2.Details().State != orderT.StatePartiallyFilled {
		t.Fatalf("unexpected order state, got %d wanted %d",
			o2.Details().State, orderT.StatePartiallyFilled)
	}

	// Check the lifetime package.
	lifetimePkgs2, err := store.LifetimePackages(ctx)
	if err != nil {
		t.Fatalf("unable to retrieve lifetime packages: %v", err)
	}
	require.Equal(t, lifetimePkgs, lifetimePkgs2)

	// And finally the auctioneer/master account and the batch key.
	ma2, err := store.FetchAuctioneerAccount(ctx)
	if err != nil {
		t.Fatalf("error getting master account: %v", err)
	}
	if ma2.Balance != 500_000 {
		t.Fatalf("unexpected master account balance, got %d wanted %d",
			ma2.Balance, 500_000)
	}
	if !bytes.Equal(ma2.BatchKey[:], batchKey.SerializeCompressed()) {
		t.Fatalf("unexpected batch key, got %x wanted %x", ma2.BatchKey,
			testTraderKey.SerializeCompressed())
	}

	// If we query for the batch as is now, we should find that it isn't
	// marked as "confirmed".
	isConf, err := store.BatchConfirmed(ctx, batchID)
	if err != nil {
		t.Fatalf("unable to look up batch confirmation status: %v", err)
	}

	if isConf {
		t.Fatalf("batch shouldn't be marked as confirmed yet")
	}

	// We'll now mark the batch as confirmed, then requery for its status,
	// we should find that the batch has been marked as confirmed.
	if err := store.ConfirmBatch(ctx, batchID); err != nil {
		t.Fatalf("unable to mark batch as confirmed: %v", err)
	}
	isConf, err = store.BatchConfirmed(ctx, batchID)
	if err != nil {
		t.Fatalf("unable to look up batch confirmation status: %v", err)
	}
	if !isConf {
		t.Fatalf("batch should be marked as confirmed")
	}
}

// TestPersistBatchResultRollback tests that the persisting operation of a batch
// is executed atomically and everything is rolled back if one operation fails.
func TestPersistBatchResultRollback(t *testing.T) {
	t.Parallel()

	var (
		ctx     = context.Background()
		batchID = orderT.BatchID{1, 2, 3}
	)
	store, cleanup := newTestStore(t)
	defer cleanup()

	// Create a test order that we are going to try to modify.
	addDummyAccount(t, store)
	o1 := &order.Bid{
		Bid: orderT.Bid{
			Kit: *dummyClientOrder(t, 500000, 1337),
		},
		Kit: *dummyOrder(t),
	}
	err := store.SubmitOrder(ctx, o1)
	if err != nil {
		t.Fatalf("unable to store order: %v", err)
	}

	// We also need a master account, for completion's sake.
	ma1 := &account.Auctioneer{
		OutPoint: wire.OutPoint{
			Index: 5,
		},
		Balance:       1_000_000,
		AuctioneerKey: testAuctioneerKeyDesc,
	}
	copy(ma1.BatchKey[:], InitialBatchKey.SerializeCompressed())
	err = store.UpdateAuctioneerAccount(ctx, ma1)
	if err != nil {
		t.Fatalf("unable to update auctioneer account: %v", err)
	}

	// Now prepare the updates we are going to try to apply.
	orderModifiers := [][]order.Modifier{
		{order.StateModifier(orderT.StatePartiallyFilled)},
	}
	accountModifiers := [][]account.Modifier{{
		account.StateModifier(account.StateClosed),
		account.LatestTxModifier(batchTx),
	}}
	ma1.Balance = 500_000

	// Then call the actual persist method on the store. We try to apply
	// account modifications to an account that does not exist. This should
	// result in an error and roll back all order modifications.
	invalidAccountKey := testAuctioneerKey
	err = store.PersistBatchResult(
		ctx, []orderT.Nonce{o1.Nonce()}, orderModifiers,
		[]*btcec.PublicKey{invalidAccountKey}, accountModifiers,
		ma1, batchID, &BatchSnapshot{
			batchTx, 0,
			matching.EmptyBatch(orderT.DefaultBatchVersion),
		},
		testTraderKey, nil,
	)
	if err == nil {
		t.Fatal("expected error persisting batch result, got nil")
	}

	// Validate that the order has not in fact been modified.
	o2, err := store.GetOrder(ctx, o1.Nonce())
	if err != nil {
		t.Fatalf("error getting order: %v", err)
	}
	if o2.Details().State != orderT.StatePartiallyFilled {
		t.Fatalf("unexpected order state, got %d wanted %d",
			o2.Details().State, orderT.StatePartiallyFilled)
	}

	// And finally the auctioneer/master account and the batch key.
	ma2, err := store.FetchAuctioneerAccount(ctx)
	if err != nil {
		t.Fatalf("error getting master account: %v", err)
	}
	if ma2.Balance != 1_000_000 {
		t.Fatalf("unexpected master account balance, got %d wanted %d",
			ma2.Balance, 1_000_000)
	}
	if !bytes.Equal(ma2.BatchKey[:], InitialBatchKey.SerializeCompressed()) {
		t.Fatalf("unexpected batch key, got %x wanted %x", ma2.BatchKey,
			InitialBatchKey.SerializeCompressed())
	}
}

func makeTestOrderBatches(ctx context.Context, t *testing.T,
	store AdminStore) []*matching.OrderBatch {

	// Create an order batch that contains dummy data.
	askLegacyClientKit := dummyClientOrder(
		t, 123_456, orderT.LegacyLeaseDurationBucket,
	)
	bidLegacyClientKit := dummyClientOrder(
		t, 123_456, orderT.LegacyLeaseDurationBucket,
	)
	askNewClientKit := dummyClientOrder(t, 123_456, 12345)
	bidNewClientKit := dummyClientOrder(t, 123_456, 12345)
	serverKit := dummyOrder(t)
	trader1 := matching.Trader{
		AccountKey: matching.AccountID{
			1, 2, 3, 4, 5,
		},
		BatchKey: toRawKey(testAuctioneerKey),
		NextBatchKey: toRawKey(
			poolscript.IncrementKey(testAuctioneerKey),
		),
		VenueSecret:   [32]byte{88, 99},
		AccountExpiry: 10,
		AccountOutPoint: wire.OutPoint{
			Hash: chainhash.Hash{
				11, 12, 13, 14, 15,
			},
			Index: 16,
		},
		AccountBalance: 17,
	}
	trader2 := matching.Trader{
		AccountKey: matching.AccountID{
			2, 3, 4, 5, 6,
		},
		BatchKey: toRawKey(testTraderKey),
		NextBatchKey: toRawKey(
			poolscript.IncrementKey(testTraderKey),
		),
		VenueSecret:   [32]byte{99, 10},
		AccountExpiry: 11,
		AccountOutPoint: wire.OutPoint{
			Hash: chainhash.Hash{
				12, 13, 14, 15, 16,
			},
			Index: 17,
		},
		AccountBalance: 18,
	}
	legacyOrders := []matching.MatchedOrder{{
		Asker:  trader1,
		Bidder: trader2,
		Details: matching.OrderPair{
			Ask: &order.Ask{
				Ask: orderT.Ask{
					Kit: *askLegacyClientKit,
				},
				Kit: *serverKit,
			},
			Bid: &order.Bid{
				Bid: orderT.Bid{
					Kit:         *bidLegacyClientKit,
					MinNodeTier: 10,
				},
				Kit: *serverKit,
			},
			Quote: matching.PriceQuote{
				MatchingRate:     9,
				TotalSatsCleared: 8,
				UnitsMatched:     7,
				UnitsUnmatched:   6,
				Type:             5,
			},
		},
	}}
	newOrders := []matching.MatchedOrder{{
		Asker:  trader1,
		Bidder: trader2,
		Details: matching.OrderPair{
			Ask: &order.Ask{
				Ask: orderT.Ask{
					Kit: *askNewClientKit,
				},
				Kit: *serverKit,
			},
			Bid: &order.Bid{
				Bid: orderT.Bid{
					Kit:         *bidNewClientKit,
					MinNodeTier: 10,
				},
				Kit: *serverKit,
			},
			Quote: matching.PriceQuote{
				MatchingRate:     9,
				TotalSatsCleared: 8,
				UnitsMatched:     7,
				UnitsUnmatched:   6,
				Type:             5,
			},
		},
	}}
	feeReport := matching.TradingFeeReport{
		AccountDiffs: map[matching.AccountID]*matching.AccountDiff{
			trader2.AccountKey: {
				AccountTally: &orderT.AccountTally{
					EndingBalance:          123,
					TotalExecutionFeesPaid: 234,
					TotalTakerFeesPaid:     345,
					TotalMakerFeesAccrued:  456,
					NumChansCreated:        567,
				},
				StartingState:   &trader2,
				RecreatedOutput: nil,
			},
			trader1.AccountKey: {
				AccountTally: &orderT.AccountTally{
					EndingBalance:          99,
					TotalExecutionFeesPaid: 88,
					TotalTakerFeesPaid:     77,
					TotalMakerFeesAccrued:  66,
					NumChansCreated:        55,
				},
				StartingState: &trader1,
				RecreatedOutput: &wire.TxOut{
					Value:    987654,
					PkScript: []byte{77, 88, 99},
				},
			},
		},
		AuctioneerFeesAccrued: 1337,
	}

	if store != nil {
		// All the orders above also need to be inserted as normal
		// orders to ensure we're able to retrieve all the supplemental
		// data we need.
		for _, o := range legacyOrders {
			err := store.SubmitOrder(ctx, o.Details.Ask)
			require.NoError(t, err)

			err = store.SubmitOrder(ctx, o.Details.Bid)
			require.NoError(t, err)
		}
		for _, o := range newOrders {
			err := store.SubmitOrder(ctx, o.Details.Ask)
			require.NoError(t, err)

			err = store.SubmitOrder(ctx, o.Details.Bid)
			require.NoError(t, err)
		}
	}

	batchV0 := matching.NewBatch(
		map[uint32][]matching.MatchedOrder{
			orderT.LegacyLeaseDurationBucket: legacyOrders,
		}, feeReport, map[uint32]orderT.FixedRatePremium{
			orderT.LegacyLeaseDurationBucket: 123,
		},
		orderT.DefaultBatchVersion,
	)

	batchV1 := matching.NewBatch(
		map[uint32][]matching.MatchedOrder{
			orderT.LegacyLeaseDurationBucket: legacyOrders,
			12345:                            newOrders,
		}, feeReport, map[uint32]orderT.FixedRatePremium{
			orderT.LegacyLeaseDurationBucket: 123,
			12345:                            321,
		},
		orderT.DefaultBatchVersion,
	)

	return []*matching.OrderBatch{batchV0, batchV1}
}

// TestPersistBatchSnapshot makes sure a batch snapshot can be stored and
// retrieved again correctly.
func TestPersistBatchSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, cleanup := newTestStore(t)
	defer cleanup()

	batches := makeTestOrderBatches(ctx, t, store)
	assertBatchSerialization(t, store, batches[0])
	assertBatchSerialization(t, store, batches[1])
}

func assertBatchSerialization(t *testing.T, store AdminStore,
	batch *matching.OrderBatch) {

	t.Helper()

	ctx := context.Background()
	txFee := btcutil.Amount(911)
	batchSnapshot := &BatchSnapshot{
		BatchTx:    batchTx,
		BatchTxFee: txFee,
		OrderBatch: batch,
	}

	var batchID orderT.BatchID
	copy(batchID[:], initialBatchKeyBytes)

	nextBatchKey := poolscript.IncrementKey(InitialBatchKey)
	ma1 := &account.Auctioneer{
		OutPoint: wire.OutPoint{
			Index: 5,
		},
		Balance:       1_000_000,
		AuctioneerKey: testAuctioneerKeyDesc,
	}
	copy(ma1.BatchKey[:], nextBatchKey.SerializeCompressed())

	// Store the batch and then read the snapshot back again immediately.
	err := store.PersistBatchResult(
		ctx, nil, nil, nil, nil, ma1, batchID, batchSnapshot,
		nextBatchKey, nil,
	)
	require.NoError(t, err)

	snapshot, err := store.GetBatchSnapshot(ctx, batchID)
	require.NoError(t, err)

	dbBatch := snapshot.OrderBatch
	dbBatchTx := snapshot.BatchTx
	dbFee := snapshot.BatchTxFee

	// Both snapshots must be identical. We use the special assert function
	// here because our batch contains orders which have net.Addr fields
	// that aren't reflect.DeepEqual compatible.
	assertJSONDeepEqual(t, batch, dbBatch)

	// We'll also ensure that we get the exact same batch transaction as
	// well.
	require.Equal(t, batchTx, dbBatchTx)
	require.Equal(t, txFee, dbFee)
}

func toRawKey(pubkey *btcec.PublicKey) [33]byte {
	var result [33]byte
	copy(result[:], pubkey.SerializeCompressed())
	return result
}
