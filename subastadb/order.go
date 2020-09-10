package subastadb

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lightninglabs/pool/clientdb"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	conc "go.etcd.io/etcd/clientv3/concurrency"
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
	// bitcoin/clm/subasta/<network>/order/<archive>/<nonce>.
	numOrderKeyParts = 7

	// nonceKeyIndex is the index of the nonce in a key split by the key
	// delimiter.
	nonceKeyIndex = 6

	// orderPrefix is the prefix that we'll use to store all order specific
	// order data. From the top level directory, this path is:
	// bitcoin/clm/subasta/<network>/order.
	orderPrefix = "order"
)

// SubmitOrder submits an order to the store. If an order with the given nonce
// already exists in the store, ErrOrderExists is returned.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) SubmitOrder(ctx context.Context,
	order order.ServerOrder) error {

	if !s.initialized {
		return errNotInitialized
	}

	// In order to guarantee consistency between the cache and what gets
	// submitted to the DB, we obtain a mutex exclusive to this nonce.
	s.nonceMtx.lock(order.Nonce())
	defer s.nonceMtx.unlock(order.Nonce())

	// Read and update the order in an isolated STM transaction to make sure
	// the same order cannot be created concurrently.
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		// First, we need to make sure no order exists for the given
		// nonce. In STM this is signaled by an empty string being
		// returned.
		key := s.getKeyOrderPrefix(order.Nonce())
		existing := stm.Get(key)
		if existing != "" {
			return ErrOrderExists
		}

		// Now that we know it doesn't yet exist, serialize and store
		// the new order.
		var buf bytes.Buffer
		err := serializeOrder(&buf, order)
		if err != nil {
			return err
		}
		stm.Put(key, buf.String())
		return nil
	})
	if err != nil {
		return err
	}

	// Order was successfully submitted, update cache.
	s.activeOrdersCacheMtx.Lock()
	s.activeOrdersCache[order.Nonce()] = order
	s.activeOrdersCacheMtx.Unlock()

	return nil
}

// UpdateOrder updates an order in the database according to the given
// modifiers.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) UpdateOrder(ctx context.Context,
	nonce orderT.Nonce, modifiers ...order.Modifier) error {

	if !s.initialized {
		return errNotInitialized
	}

	// In order to guarantee consistency between the cache and the DB
	// update, we obtain a mutex exclusive to this nonce.
	s.nonceMtx.lock(nonce)
	defer s.nonceMtx.unlock(nonce)

	// Read and update the order in one single isolated STM transaction.
	var updateCache func()
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		var err error
		updateCache, err = s.updateOrdersSTM(
			stm, []orderT.Nonce{nonce},
			[][]order.Modifier{modifiers},
		)
		return err
	})
	if err != nil {
		return err
	}

	// Now that the DB was successfully updated, also update the cache.
	updateCache()
	return nil
}

// updateOrdersSTM adds all operations necessary to update multiple orders to
// the given STM transaction. If any of the orders does not yet exist, the whole
// STM transaction will fail. In case everything went through, a function that
// updates the activeOrdersCache is returned that should be called after the
// transactions successfully completes.
func (s *EtcdStore) updateOrdersSTM(stm conc.STM, nonces []orderT.Nonce,
	modifiers [][]order.Modifier) (func(), error) {

	if len(nonces) != len(modifiers) {
		return nil, fmt.Errorf("invalid number of modifiers")
	}

	cacheUpdates := make(map[orderT.Nonce]order.ServerOrder)
	for idx, nonce := range nonces {
		// Read the current version from the DB and apply the
		// modifications to it. If the order to be modified does not
		// exist, this will be signaled with an empty string by STM.
		// Archived orders can't be updated so we only look in the
		// default path.
		key := s.getKeyOrderPrefix(nonce)
		resp := stm.Get(key)
		if resp == "" {
			return nil, ErrNoOrder
		}
		dbOrder, err := deserializeOrder(
			bytes.NewReader([]byte(resp)), nonce,
		)

		if err != nil {
			return nil, err
		}
		for _, modifier := range modifiers[idx] {
			modifier(dbOrder)
		}

		// Serialize it back into binary form.
		var buf bytes.Buffer
		err = serializeOrder(&buf, dbOrder)
		if err != nil {
			return nil, err
		}

		// If the state has been modified to it being archived now, we
		// have to move it to the archive bucket.
		if dbOrder.Details().State.Archived() {
			stm.Del(key)
			key = s.getKeyOrderPrefixArchive(nonce)
		}
		stm.Put(key, buf.String())

		cacheUpdates[nonce] = dbOrder
	}

	// Return a function that will update the cache when called.
	updateCache := func() {
		s.activeOrdersCacheMtx.Lock()
		defer s.activeOrdersCacheMtx.Unlock()

		for nonce, order := range cacheUpdates {
			// If the order now is archvied, delete it from the
			// cache of active orders.
			if order.Details().State.Archived() {
				delete(s.activeOrdersCache, nonce)
			} else {
				s.activeOrdersCache[nonce] = order
			}
		}
	}

	return updateCache, nil
}

// GetOrder returns an order by looking up the nonce. If no order with that
// nonce exists in the store, ErrNoOrder is returned.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) GetOrder(ctx context.Context, nonce orderT.Nonce) (
	order.ServerOrder, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	// By default, we assume an order that is queried here is an active,
	// non-archived order, so we'll quickly check the activeOrdersCache to
	// begin with. If not found in the cache, it can still be an archived
	// orders, so we'll fall back to fetching from the DB.
	s.activeOrdersCacheMtx.RLock()
	o, ok := s.activeOrdersCache[nonce]
	s.activeOrdersCacheMtx.RUnlock()
	if ok {
		return o, nil
	}

	// Because a trader might be querying for an order that we have already
	// archived, we look it up in the archive branch too, as it won't be in
	// the active orders cache in that case.
	key := s.getKeyOrderPrefixArchive(nonce)
	resp, err := s.getSingleValue(ctx, key, ErrNoOrder)
	if err != nil {
		return nil, err
	}
	return deserializeOrder(bytes.NewReader(resp.Kvs[0].Value), nonce)
}

// GetOrders returns all non-archived orders that are currently known to the
// store.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) GetOrders(ctx context.Context) ([]order.ServerOrder, error) {
	if !s.initialized {
		return nil, errNotInitialized
	}

	s.activeOrdersCacheMtx.RLock()
	defer s.activeOrdersCacheMtx.RUnlock()

	// We have the results cached, return them directly.
	cached := make([]order.ServerOrder, 0, len(s.activeOrdersCache))
	for _, v := range s.activeOrdersCache {
		cached = append(cached, v)
	}
	return cached, nil
}

func (s *EtcdStore) fillActiveOrdersCache(ctx context.Context) error {
	s.activeOrdersCacheMtx.Lock()
	defer s.activeOrdersCacheMtx.Unlock()

	// Fetch all active orders from the database and add them to the cache.
	key := s.getKeyOrderArchivePrefix(false)
	resultMap, err := s.getAllValuesByPrefix(ctx, key)
	if err != nil {
		return err
	}
	for key, value := range resultMap {
		nonce, err := nonceFromKey(key)
		if err != nil {
			return err
		}
		o, err := deserializeOrder(bytes.NewReader(value), nonce)
		if err != nil {
			return err
		}
		s.activeOrdersCache[nonce] = o
	}

	return nil
}

// GetArchivedOrders returns all archived orders that are currently known to the
// store.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) GetArchivedOrders(ctx context.Context) ([]order.ServerOrder,
	error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	key := s.getKeyOrderArchivePrefix(true)
	resultMap, err := s.getAllValuesByPrefix(ctx, key)
	if err != nil {
		return nil, err
	}
	orders := make([]order.ServerOrder, 0, len(resultMap))
	for key, value := range resultMap {
		nonce, err := nonceFromKey(key)
		if err != nil {
			return nil, err
		}
		o, err := deserializeOrder(bytes.NewReader(value), nonce)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}

	return orders, nil
}

// getKeyOrderPrefix returns the key prefix path for the given order.
func (s *EtcdStore) getKeyOrderPrefix(nonce orderT.Nonce) string {
	// bitcoin/clm/subasta/<network>/order/<archive>/<nonce>.
	return strings.Join(
		[]string{s.getKeyOrderArchivePrefix(false), nonce.String()},
		keyDelimiter,
	)
}

// getKeyOrderPrefixArchive returns the key prefix path for the given order.
func (s *EtcdStore) getKeyOrderPrefixArchive(nonce orderT.Nonce) string {
	// bitcoin/clm/subasta/<network>/order/<archive>/<nonce>.
	return strings.Join(
		[]string{s.getKeyOrderArchivePrefix(true), nonce.String()},
		keyDelimiter,
	)
}

// getKeyOrderArchivePrefix returns the key prefix path for active/inactive
// orders.
func (s *EtcdStore) getKeyOrderArchivePrefix(archive bool) string {
	// bitcoin/clm/subasta/<network>/order/<archive>.
	return strings.Join([]string{
		s.getKeyPrefix(orderPrefix), strconv.FormatBool(archive),
	}, keyDelimiter)
}

// nonceFromKey parses a whole order key and tries to extract the nonce from
// the last part of it. This function also checks that the key has the expected
// length and number of key parts.
func nonceFromKey(key string) (orderT.Nonce, error) {
	var nonce orderT.Nonce
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
func serializeOrder(w io.Writer, o order.ServerOrder) error {
	// Serialize the client part first.
	switch t := o.(type) {
	case *order.Ask:
		err := clientdb.SerializeOrder(&t.Ask, w)
		if err != nil {
			return err
		}

	case *order.Bid:
		err := clientdb.SerializeOrder(&t.Bid, w)
		if err != nil {
			return err
		}
	}

	// We don't have to deserialize the nonce as it's part of the etcd key.
	kit := o.ServerDetails()
	return WriteElements(
		w, kit.Sig, kit.NodeKey, kit.NodeAddrs, kit.ChanType,
		kit.Lsat, kit.MultiSigKey,
	)
}

// deserializeOrder reconstructs an order from binary data in the LN wire
// format.
func deserializeOrder(r io.Reader, nonce orderT.Nonce) (order.ServerOrder,
	error) {

	kit := &order.Kit{}

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
	case *orderT.Ask:
		ask := &order.Ask{Ask: *t, Kit: *kit}
		return ask, nil

	case *orderT.Bid:
		bid := &order.Bid{Bid: *t, Kit: *kit}
		return bid, nil

	default:
		return nil, fmt.Errorf("unknown order type: %d",
			clientOrder.Type())
	}
}
