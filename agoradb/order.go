package agoradb

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/etcd/clientv3"
	"github.com/lightninglabs/agora/client/clientdb"
	clientorder "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
)

var (
	// ErrNoOrder is the error returned if no order with the given nonce
	// exists in the store.
	ErrNoOrder = errors.New("no order found")

	// ErrOrderExists is returned if an order is submitted that is already
	// known to the store.
	ErrOrderExists = errors.New("order with this nonce already exists")

	// numOrderKeyParts is the number of parts that a full order key can be
	// split into when using the / character as delimiter. A full path looks
	// like this:
	// bitcoin/clm/agora/<network>/order/<nonce>.
	numOrderKeyParts = 6

	// nonceKeyIndex is the index of the nonce in a key split by the key
	// delimiter.
	nonceKeyIndex = 5

	// orderPrefix is the prefix that we'll use to store all order specific
	// order data. From the top level directory, this path is:
	// bitcoin/clm/agora/<network>/order.
	orderPrefix = "order"
)

// SubmitOrder submits an order to the store. If an order with the given nonce
// already exists in the store, ErrOrderExists is returned.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) SubmitOrder(mainCtx context.Context,
	order order.ServerOrder) error {

	if !s.initialized {
		return errNotInitialized
	}

	key := s.getKeyOrderPrefix(order.Nonce())
	_, err := s.getSingleValue(mainCtx, key, ErrNoOrder)
	switch err {
	// No error means there is an order with that nonce in the DB already.
	case nil:
		return ErrOrderExists

	// This is what we want, no order with that nonce should be known.
	case ErrNoOrder:

	// Surface any other error.
	default:
		return err
	}

	serialized, err := serializeOrder(order)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(mainCtx, etcdTimeout)
	defer cancel()
	_, err = s.client.Put(ctx, key, string(serialized))
	return err
}

// UpdateOrder updates an order in the database according to the given
// modifiers.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) UpdateOrder(mainCtx context.Context,
	nonce clientorder.Nonce, modifiers ...order.Modifier) error {

	if !s.initialized {
		return errNotInitialized
	}

	// Retrieve the order stored in the database.
	dbOrder, err := s.GetOrder(mainCtx, nonce)
	if err != nil {
		return err
	}

	// Apply the given modifications to it and store it back.
	for _, modifier := range modifiers {
		modifier(dbOrder)
	}
	serialized, err := serializeOrder(dbOrder)
	if err != nil {
		return err
	}
	key := s.getKeyOrderPrefix(nonce)
	ctx, cancel := context.WithTimeout(mainCtx, etcdTimeout)
	defer cancel()
	_, err = s.client.Put(ctx, key, string(serialized))
	return err
}

// UpdateOrders atomically updates a list of orders in the database
// according to the given modifiers.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) UpdateOrders(mainCtx context.Context,
	nonces []clientorder.Nonce, modifiers [][]order.Modifier) error {

	if !s.initialized {
		return errNotInitialized
	}

	if len(nonces) != len(modifiers) {
		return fmt.Errorf("invalid number of modifiers")
	}

	dbOperations := make([]clientv3.Op, len(nonces))
	for idx, nonce := range nonces {
		// Read the current version from the DB and apply the
		// modifications to it.
		dbOrder, err := s.GetOrder(mainCtx, nonce)
		if err != nil {
			return err
		}
		for _, modifier := range modifiers[idx] {
			modifier(dbOrder)
		}

		// Serialize it back into binary form.
		serialized, err := serializeOrder(dbOrder)
		if err != nil {
			return err
		}
		key := s.getKeyOrderPrefix(nonce)
		dbOperations[idx] = clientv3.OpPut(key, string(serialized))
	}

	// Write the orders in one single transaction that they are updated
	// atomically
	ctx, cancel := context.WithTimeout(mainCtx, etcdTimeout)
	defer cancel()
	_, err := s.client.Txn(ctx).
		If().
		Then(dbOperations...).
		Commit()
	return err
}

// GetOrder returns an order by looking up the nonce. If no order with that
// nonce exists in the store, ErrNoOrder is returned.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) GetOrder(ctx context.Context, nonce clientorder.Nonce) (
	order.ServerOrder, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	key := s.getKeyOrderPrefix(nonce)
	resp, err := s.getSingleValue(ctx, key, ErrNoOrder)
	if err != nil {
		return nil, err
	}
	return deserializeOrder(resp.Kvs[0].Value, nonce)
}

// GetOrders returns all orders that are currently known to the store.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) GetOrders(mainCtx context.Context) ([]order.ServerOrder, error) {
	if !s.initialized {
		return nil, errNotInitialized
	}

	key := s.getKeyPrefix(orderPrefix)
	resultMap, err := s.readOrderKeys(mainCtx, key)
	if err != nil {
		return nil, err
	}
	orders := make([]order.ServerOrder, 0, len(resultMap))
	for key, value := range resultMap {
		nonce, err := nonceFromKey(key)
		if err != nil {
			return nil, err
		}
		o, err := deserializeOrder(value, nonce)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}

	return orders, nil
}

// getKeyOrderPrefix returns the key prefix path for the given order.
func (s *EtcdStore) getKeyOrderPrefix(nonce clientorder.Nonce) string {
	// bitcoin/clm/agora/<network>/order/<nonce>.
	return strings.Join(
		[]string{s.getKeyPrefix(orderPrefix), nonce.String()},
		keyDelimiter,
	)
}

// readOrderKeys reads multiple orders from the etcd database and returns its
// content as a map of byte slices, keyed by the storage key.
func (s *EtcdStore) readOrderKeys(mainCtx context.Context,
	key string) (map[string][]byte, error) {

	ctx, cancel := context.WithTimeout(mainCtx, etcdTimeout)
	defer cancel()

	resp, err := s.client.Get(ctx, key, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte)
	for _, kv := range resp.Kvs {
		result[string(kv.Key)] = kv.Value
	}
	return result, nil
}

// nonceFromKey parses a whole order key and tries to extract the nonce from
// the last part of it. This function also checks that the key has the expected
// length and number of key parts.
func nonceFromKey(key string) (clientorder.Nonce, error) {
	var nonce clientorder.Nonce
	if len(key) == 0 {
		return nonce, fmt.Errorf("key cannot be empty")
	}
	keyParts := strings.Split(key, keyDelimiter)
	if len(keyParts) != numOrderKeyParts {
		return nonce, fmt.Errorf("invalid order key: %s", key)
	}
	nonceBytes, err := hex.DecodeString(keyParts[nonceKeyIndex])
	if err != nil {
		return nonce, fmt.Errorf("could not decode nonce: %v", err)
	}
	copy(nonce[:], nonceBytes)
	return nonce, nil
}

// serializeOrder binary serializes an order by using the LN wire format.
func serializeOrder(o order.ServerOrder) ([]byte, error) {
	var w bytes.Buffer

	// Serialize the client part first.
	switch t := o.(type) {
	case *order.Ask:
		err := clientdb.SerializeOrder(&t.Ask, &w)
		if err != nil {
			return nil, err
		}

	case *order.Bid:
		err := clientdb.SerializeOrder(&t.Bid, &w)
		if err != nil {
			return nil, err
		}
	}

	// We don't have to deserialize the nonce as it's part of the etcd key.
	kit := o.ServerDetails()
	err := WriteElements(
		&w, kit.Sig, kit.NodeKey, kit.NodeAddrs, kit.ChanType,
		kit.Lsat, kit.MultiSigKey,
	)
	if err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// deserializeOrder reconstructs an order from binary data in the LN wire
// format.
func deserializeOrder(content []byte, nonce clientorder.Nonce) (
	order.ServerOrder, error) {

	var (
		r   = bytes.NewReader(content)
		kit = &order.Kit{}
	)

	// Deserialize the client part first.
	clientOrder, err := clientdb.DeserializeOrder(nonce, r)
	if err != nil {
		return nil, err
	}

	// We don't serialize the nonce as it's part of the etcd key already.
	err = ReadElements(
		r, &kit.Sig, &kit.NodeKey, &kit.NodeAddrs, &kit.ChanType,
		&kit.Lsat, &kit.MultiSigKey,
	)
	if err != nil {
		return nil, err
	}

	// Finally read the order type specific fields.
	switch t := clientOrder.(type) {
	case *clientorder.Ask:
		ask := &order.Ask{Ask: *t, Kit: *kit}
		return ask, nil

	case *clientorder.Bid:
		bid := &order.Bid{Bid: *t, Kit: *kit}
		return bid, nil

	default:
		return nil, fmt.Errorf("unknown order type: %d",
			clientOrder.Type())
	}
}
