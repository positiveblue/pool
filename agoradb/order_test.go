package agoradb

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcutil"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/agora/order"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/lnwire"
)

// TestSubmitOrder tests that orders can be stored and retrieved correctly.
func TestSubmitOrder(t *testing.T) {
	ctxb := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// Create a dummy order and make sure it does not yet exist in the DB.
	bid := &order.Bid{
		Bid: orderT.Bid{
			Kit:         *dummyClientOrder(t, 500000),
			MinDuration: 1337,
		},
		Kit: *dummyOrder(t),
	}
	_, err := store.GetOrder(ctxb, bid.Nonce())
	if err != ErrNoOrder {
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
	assertJSONDeepEqual(t, bid, storedOrder)

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
	assertJSONDeepEqual(t, bid, allOrders[0])

	if allOrders[0].Type() != orderT.TypeBid {
		t.Fatalf("unexpected order type. got %d expected %d",
			allOrders[0].Type(), orderT.TypeBid)
	}

	// Finally, make sure we cannot submit the same order again.
	err = store.SubmitOrder(ctxb, bid)
	if err != ErrOrderExists {
		t.Fatalf("unexpected error. got %v expected %v", err,
			ErrOrderExists)
	}
}

// TestUpdateOrders tests that orders can be updated correctly.
func TestUpdateOrders(t *testing.T) {
	ctxb := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// Store two dummy orders that we are going to update later.
	o1 := &order.Bid{
		Bid: orderT.Bid{
			Kit:         *dummyClientOrder(t, 500000),
			MinDuration: 1337,
		},
		Kit: *dummyOrder(t),
	}
	err := store.SubmitOrder(ctxb, o1)
	if err != nil {
		t.Fatalf("unable to store order: %v", err)
	}
	o2 := &order.Ask{
		Ask: orderT.Ask{
			Kit:         *dummyClientOrder(t, 500000),
			MaxDuration: 1337,
		},
		Kit: *dummyOrder(t),
	}
	err = store.SubmitOrder(ctxb, o2)
	if err != nil {
		t.Fatalf("unable to store order: %v", err)
	}

	// Make sure they are both stored to the non-archived branch of orders.
	keyOrderPrefix := keyPrefix + "order/"
	orderMap, err := store.readOrderKeys(ctxb, keyOrderPrefix)
	if err != nil {
		t.Fatalf("unable to read order keys: %v", err)
	}
	key1 := keyPrefix + "order/false/" + o1.Nonce().String()
	if _, ok := orderMap[key1]; !ok {
		t.Fatalf("order 1 was not found with expected key '%s'", key1)
	}
	key2 := keyPrefix + "order/false/" + o2.Nonce().String()
	if _, ok := orderMap[key2]; !ok {
		t.Fatalf("order 2 was not found with expected key '%s'", key2)
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
	err = store.UpdateOrders(
		ctxb, []orderT.Nonce{o1.Nonce(), o2.Nonce()},
		[][]order.Modifier{{stateModifier}, {stateModifier}},
	)
	if err != nil {
		t.Fatalf("unable to update orders: %v", err)
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

	// Make sure the keys reflect the change as well.
	orderMap, err = store.readOrderKeys(ctxb, keyOrderPrefix)
	if err != nil {
		t.Fatalf("unable to read order keys: %v", err)
	}
	key1 = keyPrefix + "order/true/" + o1.Nonce().String()
	if _, ok := orderMap[key1]; !ok {
		t.Fatalf("order 1 was not found with expected key '%s'", key1)
	}
	key2 = keyPrefix + "order/true/" + o2.Nonce().String()
	if _, ok := orderMap[key2]; !ok {
		t.Fatalf("order 2 was not found with expected key '%s'", key2)
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
			Kit:         *dummyClientOrder(t, 12345),
			MinDuration: 1337,
		},
		Kit: *dummyOrder(t),
	}
	err = store.UpdateOrder(
		ctxb, o3.Nonce(), order.StateModifier(orderT.StateExecuted),
	)
	if err != ErrNoOrder {
		t.Fatalf("unexpected error. got %v wanted %v", err, ErrNoOrder)
	}
}

// assertJSONDeepEqual deep compares two items for equality by both serializing
// them to JSON and then comparing them as text. This can be used for nested
// structures that are not compatible with reflect.DeepEqual, for example
// anything that contains net.Addr fields.
func assertJSONDeepEqual(t *testing.T, o1, o2 interface{}) {
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

func dummyClientOrder(t *testing.T, amt btcutil.Amount) *orderT.Kit {
	var testPreimage lntypes.Preimage
	if _, err := rand.Read(testPreimage[:]); err != nil {
		t.Fatalf("could not create private key: %v", err)
	}
	kit := orderT.NewKitWithPreimage(testPreimage)
	kit.State = orderT.StatePartiallyFilled
	kit.FixedRate = 21
	kit.Amt = amt
	kit.MultiSigKeyLocator = keychain.KeyLocator{Index: 1, Family: 2}
	kit.FundingFeeRate = chainfee.FeePerKwFloor
	copy(kit.AcctKey[:], testTraderKey.SerializeCompressed())
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
	kit.ChanType = 7
	kit.Lsat = lsat.TokenID{9, 8, 7, 6, 5}
	return kit
}

func randomPubKey(t *testing.T) *btcec.PublicKey {
	var testPriv [32]byte
	if _, err := rand.Read(testPriv[:]); err != nil {
		t.Fatalf("could not create private key: %v", err)
	}

	_, pub := btcec.PrivKeyFromBytes(btcec.S256(), testPriv[:])
	return pub
}
