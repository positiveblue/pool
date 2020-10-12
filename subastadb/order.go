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

	// orderNodeTierPrefix is the key that we'll use to store the node tier
	// for an order, which is nested within the main order prefix. From the
	// top-level directory, this path is:
	//  * bitcoin/clm/subasta/<network>/order/<archive>/<nonce>/node_tier
	orderNodeTierKey = "node_tier"
)

// SubmitOrder submits an order to the store. If an order with the given nonce
// already exists in the store, ErrOrderExists is returned.
//
// NOTE: This is part of the Store interface.
func (s *EtcdStore) SubmitOrder(ctx context.Context,
	newOrder order.ServerOrder) error {

	if !s.initialized {
		return errNotInitialized
	}

	// In order to guarantee consistency between the cache and what gets
	// submitted to the DB, we obtain a mutex exclusive to this nonce.
	s.nonceMtx.lock(newOrder.Nonce())
	defer s.nonceMtx.unlock(newOrder.Nonce())

	// Read and update the order in an isolated STM transaction to make sure
	// the same order cannot be created concurrently.
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		// First, we need to make sure no order exists for the given
		// nonce. In STM this is signaled by an empty string being
		// returned.
		key := s.getKeyOrderPrefix(newOrder.Nonce())
		existing := stm.Get(key)
		if existing != "" {
			return ErrOrderExists
		}

		// Now that we know it doesn't yet exist, serialize and store
		// the new order.
		var buf bytes.Buffer
		err := serializeOrder(&buf, newOrder)
		if err != nil {
			return err
		}
		stm.Put(key, buf.String())

		// Next, we'll write to the nested key under the main namespace
		// that stores the node tier of an order. However, for now, we
		// only need to write to this keyspace if this order is a Bid.
		newBidOrder, ok := newOrder.(*order.Bid)
		if !ok {
			return nil
		}

		var orderTierBuf bytes.Buffer
		err = WriteElements(
			&orderTierBuf, newBidOrder.MinNodeTier,
		)
		if err != nil {
			return err
		}

		orderTierKey := s.getOrderTierKey(newOrder.Nonce())
		stm.Put(orderTierKey, orderTierBuf.String())

		return nil
	})
	if err != nil {
		return err
	}

	// Order was successfully submitted, update cache.
	s.activeOrdersCacheMtx.Lock()
	s.activeOrdersCache[newOrder.Nonce()] = newOrder
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

		// If this is a Bid, then we'll need to grab some additional
		// auxiliary data. Note that this data might not be explicitly
		// there for some earlier order.
		orderNodeTierBuf := &bytes.Reader{}
		nodeTierKey := s.getOrderTierKey(nonce)
		orderNodeTierResp := stm.Get(nodeTierKey)
		if orderNodeTierResp != "" {
			orderNodeTierBuf = bytes.NewReader(
				[]byte(orderNodeTierResp),
			)
		}

		dbOrder, err := deserializeOrder(
			bytes.NewReader([]byte(resp)), orderNodeTierBuf, nonce,
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

		if orderNodeTierResp != "" {
			// We'll also update the node tier as well, though this
			// is considered to be immutable data.
			stm.Put(nodeTierKey, orderNodeTierResp)
		}

		cacheUpdates[nonce] = dbOrder
	}

	// Return a function that will update the cache when called.
	updateCache := func() {
		s.activeOrdersCacheMtx.Lock()
		defer s.activeOrdersCacheMtx.Unlock()

		for nonce, order := range cacheUpdates {
			// If the order now is archived, delete it from the
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

	// In addition to the base order, we'll also need to obtain the node
	// tier for this order. Note that this key won't exist if it wasn't
	// populated for the order, or if it's an Ask.
	nodeTierBytes, err := fetchNodeTierBytes(ctx, s, nonce, true)
	if err != nil {
		return nil, err
	}

	return deserializeOrder(
		bytes.NewReader(resp.Kvs[0].Value),
		bytes.NewReader(nodeTierBytes),
		nonce,
	)
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
	for key, baseOrderBytes := range resultMap {
		// If this isn't just a plain order, then it'll be additional
		// data that we stored outside the main order key. Due to the
		// storing, we'll always get the base order key, followed by
		// any additional columns that were added later.
		if !isBaseOrderBytes(key) {
			// In this case, we'll skip it given that we'll
			// manually query the map below for the column data we
			// need.
			continue
		}

		nonce, err := nonceFromKey(key)
		if err != nil {
			return err
		}

		// Next, we'll need to read out any of the extra data that'll
		// be stored using suffix of the main order key.
		nodeTierBytes, ok := resultMap[strings.Join(
			[]string{key, orderNodeTierKey}, keyDelimiter,
		)]
		if !ok {
			// If we can't find this data, then it'll just default
			// to what the default node tier is, and we'll pass in
			// a set of empty bytes below to trigger that behavior.
			log.Warnf("unable to find node tier bytes for "+
				"order_nonce=%v", nonce)
		}

		o, err := deserializeOrder(
			bytes.NewReader(baseOrderBytes),
			bytes.NewReader(nodeTierBytes),
			nonce,
		)
		if err != nil {
			return err
		}

		s.activeOrdersCache[nonce] = o
	}

	return nil
}

// isBaseOrderBytes returns true if the target DB key properly matches what
// we'd expect from a key that only stores the base order bytes. This is used
// typically in range statements.
func isBaseOrderBytes(dbKey string) bool {
	keySplits := strings.Split(dbKey, keyDelimiter)

	// We know it's a pure order base bytes key, if there're no extra paths
	// in the full key.
	return len(keySplits) == numOrderKeyParts
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
	for key, baseOrderBytes := range resultMap {
		// Skip this key if it doesn't store a set of base order bytes.
		if !isBaseOrderBytes(key) {
			continue
		}

		// Now that we know this key stores the base border bytes,
		// we'll extract the nonce from the key.
		nonce, err := nonceFromKey(key)
		if err != nil {
			return nil, err
		}

		// Next, we'll need to read out any of the extra data that'll
		// be stored using suffix of the main order key.
		nodeTierBytes, ok := resultMap[strings.Join(
			[]string{key, orderNodeTierKey}, keyDelimiter,
		)]
		if !ok {
			// If we can't find this data, then it'll just default
			// to what the default node tier is, and we'll pass in
			// a set of empty bytes below to trigger that behavior.
			log.Warnf("unable to find node tier bytes for "+
				"order_nonce=%v", nonce)
		}

		o, err := deserializeOrder(
			bytes.NewReader(baseOrderBytes),
			bytes.NewReader(nodeTierBytes),
			nonce,
		)
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

// getOrderTierKey returns the key used to store the tier of an order.
// Currently, this key will only be populated for Bid orders.
func (s *EtcdStore) getOrderTierKey(nonce orderT.Nonce) string {
	// bitcoin/clm/subasta/<network>/order/<archiveFalse>/<nonce>/order_tier.
	return strings.Join([]string{
		s.getKeyOrderPrefix(nonce), orderNodeTierKey,
	}, keyDelimiter)
}

// getOrderTierKeyArchive returns the key used to store the tier of an order.
// Currently, this key will only be populated for Bid orders. This version
// should be used to read the node tier of archived orders.
func (s *EtcdStore) getOrderTierKeyArchive(nonce orderT.Nonce) string {
	// bitcoin/clm/subasta/<network>/order/<archiveTrue>/<nonce>/order_tier.
	return strings.Join([]string{
		s.getKeyOrderPrefixArchive(nonce), orderNodeTierKey,
	}, keyDelimiter)
}

// fetchNodeTierBytes fetches the raw bytes of a stored node tier for a given
// order nonce and archive status.
func fetchNodeTierBytes(ctx context.Context, s *EtcdStore, nonce orderT.Nonce,
	archive bool) ([]byte, error) {

	var nodeTierKey string
	if archive {
		nodeTierKey = s.getOrderTierKeyArchive(nonce)
	} else {
		nodeTierKey = s.getOrderTierKey(nonce)
	}

	orderNodeTierResp, err := s.getSingleValue(
		ctx, nodeTierKey, nil,
	)
	if err != nil {
		return nil, err
	}

	if orderNodeTierResp == nil {
		return nil, nil
	}

	return orderNodeTierResp.Kvs[0].Value, nil
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
func deserializeOrder(
	baseOrderBytes io.Reader, orderTierBytes io.Reader,
	nonce orderT.Nonce) (order.ServerOrder, error) {

	kit := &order.Kit{}

	// Deserialize the client part first.
	clientOrder, err := clientdb.DeserializeOrder(nonce, baseOrderBytes)
	if err != nil {
		return nil, err
	}

	// We don't serialize the nonce as it's part of the etcd key already.
	err = ReadElements(
		baseOrderBytes, &kit.Sig, &kit.NodeKey, &kit.NodeAddrs,
		&kit.ChanType, &kit.Lsat, &kit.MultiSigKey,
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

		if orderTierBytes == nil {
			return bid, nil
		}

		// For bid orders, we'll also now attempt to read the extra
		// state of the order from the orderTierBytes buffer.
		//
		// Existing orders may not have this value, in which case,
		// we'll just assume the default order tier.
		var orderNodeTier uint32
		err := ReadElements(orderTierBytes, &orderNodeTier)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, err
		}

		// If this wasn't found in the database, then we'll assume the
		// default value.
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			bid.MinNodeTier = orderT.NodeTierDefault
		} else {
			bid.MinNodeTier = orderT.NodeTier(orderNodeTier)
		}

		return bid, nil

	default:
		return nil, fmt.Errorf("unknown order type: %d",
			clientOrder.Type())
	}
}
