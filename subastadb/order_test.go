package subastadb

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightninglabs/aperture/lsat"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/stretchr/testify/require"
)

// TestSubmitOrder tests that orders can be stored and retrieved correctly.
func TestSubmitOrder(t *testing.T) {
	t.Parallel()

	ctxb := context.Background()
	store, cleanup := newTestStore(t)
	defer cleanup()

	// Create a dummy order and make sure it does not yet exist in the DB.
	addDummyAccount(t, store)
	bid := &order.Bid{
		Bid: orderT.Bid{
			Kit:             *dummyClientOrder(t, 500000, 1337),
			MinNodeTier:     9,
			SelfChanBalance: 12345,
		},
		Kit:       *dummyOrder(t),
		IsSidecar: true,
	}
	bid.AllowedNodeIDs = [][33]byte{{1, 2, 3}}
	_, err := store.GetOrder(ctxb, bid.Nonce())
	if !errors.Is(err, ErrNoOrder) {
		t.Fatalf("unexpected error. got %v expected %v", err,
			ErrNoOrder)
	}

	// Store the dummy order now.
	err = store.SubmitOrder(ctxb, bid)
	if err != nil {
		t.Fatalf("unable to store order: %v", err)
	}
	storedOrder, err := store.GetOrder(ctxb, bid.Nonce())
	if err != nil {
		t.Fatalf("unable to retrieve order: %v", err)
	}

	assertEqualStoredOrder(t, bid, storedOrder)

	// Check that we got the correct type back.
	if storedOrder.Type() != orderT.TypeBid {
		t.Fatalf("unexpected order type. got %d expected %d",
			storedOrder.Type(), orderT.TypeBid)
	}

	// Get all orders and check that we get the same as when querying a
	// specific one.
	allOrders, err := store.GetOrders(ctxb)
	if err != nil {
		t.Fatalf("unable to get all orders: %v", err)
	}
	if len(allOrders) != 1 {
		t.Fatalf("unexpected number of orders. got %d expected %d",
			len(allOrders), 1)
	}
	assertEqualStoredOrder(t, bid, allOrders[0])

	if allOrders[0].Type() != orderT.TypeBid {
		t.Fatalf("unexpected order type. got %d expected %d",
			allOrders[0].Type(), orderT.TypeBid)
	}

	// Finally, make sure we cannot submit the same order again.
	err = store.SubmitOrder(ctxb, bid)
	if !errors.Is(err, ErrOrderExists) {
		t.Fatalf("unexpected error. got %v expected %v", err,
			ErrOrderExists)
	}

	// Check that the order that we get from the db (now in the cache)
	// matches the expected values.
	storedOrder, err = store.GetOrder(ctxb, bid.Nonce())
	if err != nil {
		t.Fatalf("unable to retrieve order: %v", err)
	}
	assertEqualStoredOrder(t, bid, storedOrder)
}

// TestUpdateOrders tests that orders can be updated correctly.
func TestUpdateOrders(t *testing.T) {
	t.Parallel()

	ctxb := context.Background()
	store, cleanup := newTestStore(t)
	defer cleanup()

	// Store two dummy orders that we are going to update later.
	addDummyAccount(t, store)
	o1 := &order.Bid{
		Bid: orderT.Bid{
			Kit:         *dummyClientOrder(t, 500000, 1337),
			MinNodeTier: 99,
		},
		Kit: *dummyOrder(t),
	}
	err := store.SubmitOrder(ctxb, o1)
	if err != nil {
		t.Fatalf("unable to store order: %v", err)
	}
	o2 := &order.Ask{
		Ask: orderT.Ask{
			Kit: *dummyClientOrder(t, 500000, 1337),
		},
		Kit: *dummyOrder(t),
	}
	err = store.SubmitOrder(ctxb, o2)
	if err != nil {
		t.Fatalf("unable to store order: %v", err)
	}

	// Update the state of the first order and check that it is persisted.
	err = store.UpdateOrder(
		ctxb, o1.Nonce(),
		order.StateModifier(orderT.StateCleared),
	)
	if err != nil {
		t.Fatalf("unable to update order: %v", err)
	}
	storedOrder, err := store.GetOrder(ctxb, o1.Nonce())
	if err != nil {
		t.Fatalf("unable to retrieve order: %v", err)
	}
	if storedOrder.Details().State != orderT.StateCleared {
		t.Fatalf("unexpected order state. got %d expected %d",
			storedOrder.Details().State,
			orderT.StateCleared)
	}

	// Bulk update the state of both orders and check that they are
	// persisted correctly and moved out of the active bucket into the
	// archive.
	stateModifier := order.StateModifier(orderT.StateExecuted)
	for _, nonce := range []orderT.Nonce{o1.Nonce(), o2.Nonce()} {
		err := store.UpdateOrder(ctxb, nonce, stateModifier)
		if err != nil {
			require.NoError(t, err)
		}
	}

	allOrders, err := store.GetOrders(ctxb)
	if err != nil {
		t.Fatalf("unable to get all orders: %v", err)
	}
	if len(allOrders) != 0 {
		t.Fatalf("unexpected number of orders. got %d expected %d",
			len(allOrders), 0)
	}

	// Both orders should now be in the archived path.
	allOrders, err = store.GetArchivedOrders(ctxb)
	if err != nil {
		t.Fatalf("unable to get all active orders: %v", err)
	}
	if len(allOrders) != 2 {
		t.Fatalf("unexpected number of orders. got %d expected %d",
			len(allOrders), 2)
	}
	for _, o := range allOrders {
		if o.Details().State != orderT.StateExecuted {
			t.Fatalf("unexpected order state. got %d expected %d",
				o.Details().State,
				orderT.StateExecuted)
		}
	}

	// We should still be able to look up an order by its nonce, even if
	// it's archived.
	storedOrder, err = store.GetOrder(ctxb, o2.Nonce())
	if err != nil {
		t.Fatalf("unable to retrieve order: %v", err)
	}
	if !storedOrder.Details().State.Archived() {
		t.Fatalf("expected stored order to be archived but was not")
	}

	// Finally make sure we can't update an order that does not exist.
	o3 := &order.Bid{
		Bid: orderT.Bid{
			Kit:         *dummyClientOrder(t, 12345, 1337),
			MinNodeTier: 9,
		},
		Kit: *dummyOrder(t),
	}
	err = store.UpdateOrder(
		ctxb, o3.Nonce(), order.StateModifier(orderT.StateExecuted),
	)
	if !errors.Is(err, ErrNoOrder) {
		t.Fatalf("unexpected error. got %v wanted %v", err, ErrNoOrder)
	}
}

// assertEqualStoredOrder asserts that two server orders are equal removing
// the orderT information that is not present in the server.
func assertEqualStoredOrder(t *testing.T, expected, got order.ServerOrder) {
	t.Helper()

	preimage := expected.Details().Preimage
	keyLocator := expected.Details().MultiSigKeyLocator

	expected.Details().Preimage = got.Details().Preimage
	expected.Details().MultiSigKeyLocator = got.Details().MultiSigKeyLocator
	assertJSONDeepEqual(t, expected, got)
	expected.Details().Preimage = preimage
	expected.Details().MultiSigKeyLocator = keyLocator
}

// assertJSONDeepEqual deep compares two items for equality by both serializing
// them to JSON and then comparing them as text. This can be used for nested
// structures that are not compatible with reflect.DeepEqual, for example
// anything that contains net.Addr fields.
func assertJSONDeepEqual(t *testing.T, o1, o2 interface{}) {
	t.Helper()

	expected, err := json.Marshal(o1)
	if err != nil {
		t.Fatalf("cannot marshal: %v", err)
	}
	actual, err := json.Marshal(o2)
	if err != nil {
		t.Fatalf("cannot marshal: %v", err)
	}
	if !bytes.Equal(expected, actual) {
		t.Fatalf("expected elem: %s\ngot: %s", string(expected),
			string(actual))
	}
}

func dummyClientOrder(t *testing.T, amt btcutil.Amount,
	leaseDuration uint32) *orderT.Kit {

	var testPreimage lntypes.Preimage
	if _, err := rand.Read(testPreimage[:]); err != nil {
		t.Fatalf("could not create private key: %v", err)
	}
	kit := orderT.NewKitWithPreimage(testPreimage)
	kit.State = orderT.StatePartiallyFilled
	kit.FixedRate = 21
	kit.Amt = amt
	kit.Units = orderT.NewSupplyFromSats(amt)
	kit.UnitsUnfulfilled = kit.Units
	kit.MinUnitsMatch = 1
	kit.MultiSigKeyLocator = keychain.KeyLocator{Index: 1, Family: 2}
	kit.MaxBatchFeeRate = chainfee.FeePerKwFloor
	kit.LeaseDuration = leaseDuration
	copy(kit.AcctKey[:], testTraderKey.SerializeCompressed())
	kit.ChannelType = orderT.ChannelTypeScriptEnforced
	return kit
}

func dummyOrder(t *testing.T) *order.Kit {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:9735")
	if err != nil {
		t.Fatalf("could not parse IP addr: %v", err)
	}
	kit := &order.Kit{}
	kit.Sig = lnwire.Sig{99, 99, 99}
	copy(kit.NodeKey[:], randomPubKey(t).SerializeCompressed())
	copy(kit.MultiSigKey[:], randomPubKey(t).SerializeCompressed())
	kit.NodeAddrs = []net.Addr{addr}
	kit.Lsat = lsat.TokenID{9, 8, 7, 6, 5}
	kit.UserAgent = "poold/v0.4.3-alpha/commit=test"
	return kit
}

func randomPubKey(t *testing.T) *btcec.PublicKey {
	var testPriv [32]byte
	if _, err := rand.Read(testPriv[:]); err != nil {
		t.Fatalf("could not create private key: %v", err)
	}

	_, pub := btcec.PrivKeyFromBytes(testPriv[:])
	return pub
}

func addDummyAccount(t *testing.T, store Store) {
	t.Helper()

	ctx := context.Background()
	err := store.ReserveAccount(ctx, testTokenID, &testReservation)
	if err != nil {
		t.Fatalf("unable to reserve account: %v", err)
	}
	acct := testAccount
	err = store.CompleteReservation(ctx, &acct)
	if err != nil {
		t.Fatalf("unable to complete account reservation: %v", err)
	}
}
