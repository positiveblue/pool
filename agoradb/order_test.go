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
	"github.com/davecgh/go-spew/spew"
	clientorder "github.com/lightninglabs/agora/client/order"
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

	// Store a dummy order and see if we can retrieve it again.
	bid := &order.Bid{
		Bid: clientorder.Bid{
			Kit:         *dummyClientOrder(t, 500000),
			MinDuration: 1337,
		},
		Kit: *dummyOrder(t),
	}
	err := store.SubmitOrder(ctxb, bid)
	if err != nil {
		t.Fatalf("unable to store order: %v", err)
	}
	storedOrder, err := store.GetOrder(ctxb, bid.Nonce())
	if err != nil {
		t.Fatalf("unable to retrieve order: %v", err)
	}
	assertOrdersEqual(t, bid, storedOrder)

	// Check that we got the correct type back.
	if storedOrder.Type() != clientorder.TypeBid {
		t.Fatalf("unexpected order type. got %d expected %d",
			storedOrder.Type(), clientorder.TypeBid)
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
	assertOrdersEqual(t, bid, allOrders[0])

	if allOrders[0].Type() != clientorder.TypeBid {
		t.Fatalf("unexpected order type. got %d expected %d",
			allOrders[0].Type(), clientorder.TypeBid)
	}
}

// TestUpdateOrders tests that orders can be updated correctly.
func TestUpdateOrders(t *testing.T) {
	ctxb := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// Store two dummy orders that we are going to update later.
	o1 := &order.Bid{
		Bid: clientorder.Bid{
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
		Ask: clientorder.Ask{
			Kit:         *dummyClientOrder(t, 500000),
			MaxDuration: 1337,
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
		order.StateModifier(clientorder.StatePartiallyFilled),
	)
	if err != nil {
		t.Fatalf("unable to update order: %v", err)
	}
	storedOrder, err := store.GetOrder(ctxb, o1.Nonce())
	if err != nil {
		t.Fatalf("unable to retrieve order: %v", err)
	}
	if storedOrder.Details().State != clientorder.StatePartiallyFilled {
		t.Fatalf("unexpected order state. got %d expected %d",
			storedOrder.Details().State,
			clientorder.StatePartiallyFilled)
	}

	// Bulk update the state of both orders and check that they are
	// persisted correctly.
	stateModifier := order.StateModifier(clientorder.StateCleared)
	err = store.UpdateOrders(
		ctxb, []clientorder.Nonce{o1.Nonce(), o2.Nonce()},
		[][]order.Modifier{{stateModifier}, {stateModifier}},
	)
	if err != nil {
		t.Fatalf("unable to update orders: %v", err)
	}
	allOrders, err := store.GetOrders(ctxb)
	if err != nil {
		t.Fatalf("unable to get all orders: %v", err)
	}
	if len(allOrders) != 2 {
		t.Fatalf("unexpected number of orders. got %d expected %d",
			len(allOrders), 2)
	}
	for _, o := range allOrders {
		if o.Details().State != clientorder.StateCleared {
			t.Fatalf("unexpected order state. got %d expected %d",
				o.Details().State,
				clientorder.StateCleared)
		}
	}
}

// assertOrdersEqual deep compares two orders for equality. We cannot use
// reflect.DeepEqual because that doesn't seem to work for net.Addr.
func assertOrdersEqual(t *testing.T, o1, o2 order.ServerOrder) {
	expected, err := json.Marshal(o1)
	if err != nil {
		t.Fatalf("cannot marshal: %v", err)
	}
	actual, err := json.Marshal(o2)
	if err != nil {
		t.Fatalf("cannot marshal: %v", err)
	}
	if !bytes.Equal(expected, actual) {
		t.Fatalf("expected order: %v\ngot: %v", spew.Sdump(o1),
			spew.Sdump(o2))
	}
}

func dummyClientOrder(t *testing.T, amt btcutil.Amount) *clientorder.Kit {
	var testPreimage lntypes.Preimage
	if _, err := rand.Read(testPreimage[:]); err != nil {
		t.Fatalf("could not create private key: %v", err)
	}
	kit := clientorder.NewKitWithPreimage(testPreimage)
	kit.State = clientorder.StateExecuted
	kit.FixedRate = 21
	kit.Amt = amt
	kit.MultiSigKeyLocator = keychain.KeyLocator{Index: 1, Family: 2}
	kit.FundingFeeRate = chainfee.FeePerKwFloor
	kit.AcctKey = testTraderKey
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
