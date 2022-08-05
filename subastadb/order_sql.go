package subastadb

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb/postgres"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/tor"
)

type NodeIDSlice [][33]byte

// SubmitOrder submits an order to the store. If an order with the given
// nonce already exists in the store, ErrOrderExists is returned.
func (s *SQLStore) SubmitOrder(ctx context.Context,
	newOrder order.ServerOrder) error {

	nonce := newOrder.Nonce()
	txBody := func(txQueries *postgres.Queries) error {
		rows, err := txQueries.GetOrders(ctx, [][]byte{nonce[:]})
		switch {
		case err != nil:
			return err

		case len(rows) != 0:
			return ErrOrderExists
		}

		// Timestamp the order creation.
		newOrder.ServerDetails().CreatedAt = time.Now().UTC().Truncate(
			time.Microsecond,
		)

		// Create related row for the order in the orders table.
		err = upsertOrderWithTx(ctx, txQueries, newOrder)
		if err != nil {
			return err
		}

		// Create related row for the order in the orders_bid table.
		if newOrder.Type() == orderT.TypeBid {
			newBidOrder, ok := newOrder.(*order.Bid)
			if !ok {
				return nil
			}

			err = createOrderBidWithTx(ctx, txQueries, newBidOrder)
			if err != nil {
				return err
			}
		}

		// Create related row for the order in the
		// order_node_network_addresses table.
		err = createOrderNetworkAddressWithTx(ctx, txQueries, newOrder)
		if err != nil {
			return err
		}

		// Create related row for the order in the
		// order_allowed_node_ids table.
		return createOrderAllowedNodeIDsWithTx(ctx, txQueries, newOrder)
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to create order: %w", err)
	}
	return nil
}

// UpdateOrder updates an order in the database according to the given
// modifiers.
func (s *SQLStore) UpdateOrder(ctx context.Context, nonce orderT.Nonce,
	modifiers ...order.Modifier) error {

	txBody := func(txQueries *postgres.Queries) error {
		return modifyOrderWithTx(ctx, txQueries, nonce, modifiers...)
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to update order %x: %w", nonce, err)
	}
	return nil
}

// GetOrder returns an order by looking up the nonce. If no order with
// that nonce exists in the store, ErrNoOrder is returned.
func (s *SQLStore) GetOrder(ctx context.Context,
	nonce orderT.Nonce) (order.ServerOrder, error) {

	var o order.ServerOrder
	txBody := func(txQueries *postgres.Queries) error {
		var err error
		o, err = getOrderWithTx(ctx, txQueries, nonce)
		return err
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return nil, fmt.Errorf("unable to get order %x: %w", nonce,
			err)
	}
	return o, nil
}

// GetOrders returns all non-archived orders that are currently known to
// the store.
func (s *SQLStore) GetOrders(ctx context.Context) ([]order.ServerOrder,
	error) {

	var orders []order.ServerOrder
	txBody := func(txQueries *postgres.Queries) error {
		archived := false
		nonces, err := getNoncesWithTx(ctx, txQueries, archived)
		if err != nil {
			return err
		}
		orders, err = getOrdersWithTx(ctx, txQueries, nonces)
		if err != nil {
			return err
		}
		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return nil, fmt.Errorf("unable to get active orders: %x", err)
	}
	return orders, nil
}

// GetArchivedOrders returns all archived orders that are currently
// known to the store.
func (s *SQLStore) GetArchivedOrders(ctx context.Context) ([]order.ServerOrder,
	error) {

	var orders []order.ServerOrder
	txBody := func(txQueries *postgres.Queries) error {
		archived := true
		nonces, err := getNoncesWithTx(ctx, txQueries, archived)
		if err != nil {
			return err
		}
		orders, err = getOrdersWithTx(ctx, txQueries, nonces)
		if err != nil {
			return err
		}
		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return nil, fmt.Errorf("unable to fetch archived orders: %v",
			err)
	}
	return orders, nil
}

// upsertOrderWithTx inserts/updates a new order in the db using the provided
// queries struct.
func upsertOrderWithTx(ctx context.Context, txQueries *postgres.Queries,
	o order.ServerOrder) error {

	nonce := o.Nonce()
	details := o.Details()
	serverDetails := o.ServerDetails()
	params := postgres.UpsertOrderParams{
		Nonce: nonce[:],

		Type:             int16(o.Type()),
		TraderKey:        details.AcctKey[:],
		Version:          int64(details.Version),
		State:            int16(details.State),
		FixedRate:        int64(details.FixedRate),
		Amount:           int64(details.Amt),
		Units:            int64(details.Units),
		UnitsUnfulfilled: int64(details.UnitsUnfulfilled),
		MinUnitsMatch:    int64(details.MinUnitsMatch),
		MaxBatchFeeRate:  int64(details.MaxBatchFeeRate),
		LeaseDuration:    int64(details.LeaseDuration),
		ChannelType:      int16(details.ChannelType),

		Signature:   serverDetails.Sig[:],
		MultisigKey: serverDetails.MultiSigKey[:],
		NodeKey:     serverDetails.NodeKey[:],
		TokenID:     serverDetails.Lsat[:],
		UserAgent:   serverDetails.UserAgent,

		Archived: details.State.Archived(),

		CreatedAt:  marshalSQLNullTime(serverDetails.CreatedAt),
		ArchivedAt: marshalSQLNullTime(serverDetails.ArchivedAt),
	}
	return txQueries.UpsertOrder(ctx, params)
}

// createOrderBidWithTx inserts the order data specific to bids using the
// provided queries struct.
func createOrderBidWithTx(ctx context.Context, txQueries *postgres.Queries,
	bid *order.Bid) error {

	nonce := bid.Nonce()
	params := postgres.CreateOrderBidParams{
		Nonce:           nonce[:],
		MinNodeTier:     int64(bid.MinNodeTier),
		SelfChanBalance: int64(bid.SelfChanBalance),
		IsSidecar:       bid.IsSidecar,
	}
	return txQueries.CreateOrderBid(ctx, params)
}

// createOrderNetworkAddressWithTx creates all the network address related
// to an order using the provided queries struct.
func createOrderNetworkAddressWithTx(ctx context.Context,
	txQueries *postgres.Queries, o order.ServerOrder) error {

	paramList := make(
		[]postgres.CreateOrderNetworkAddressParams, 0,
		len(o.ServerDetails().NodeAddrs),
	)

	nonce := o.Nonce()
	for _, addr := range o.ServerDetails().NodeAddrs {
		params := postgres.CreateOrderNetworkAddressParams{
			Nonce:   nonce[:],
			Network: addr.Network(),
			Address: addr.String(),
		}
		paramList = append(paramList, params)
	}

	_, err := txQueries.CreateOrderNetworkAddress(ctx, paramList)
	return err
}

// createOrderAllowedNodeIDsWithTx creates all the allowed/not allowed data
// related to an order using the provided queries struct.
func createOrderAllowedNodeIDsWithTx(ctx context.Context,
	txQueries *postgres.Queries, o order.ServerOrder) error {

	nonce := o.Nonce()
	details := o.Details()
	nRows := len(details.AllowedNodeIDs) + len(details.NotAllowedNodeIDs)
	paramList := make(
		[]postgres.CreateOrderAllowedNodeIdsParams, 0, nRows,
	)

	for _, nodeID := range details.AllowedNodeIDs {
		params := postgres.CreateOrderAllowedNodeIdsParams{
			Nonce:   nonce[:],
			NodeKey: nodeID[:],
			Allowed: true,
		}
		paramList = append(paramList, params)
	}

	for _, nodeID := range details.NotAllowedNodeIDs {
		params := postgres.CreateOrderAllowedNodeIdsParams{
			Nonce:   nonce[:],
			NodeKey: nodeID[:],
			Allowed: false,
		}
		paramList = append(paramList, params)
	}

	_, err := txQueries.CreateOrderAllowedNodeIds(ctx, paramList)
	return err
}

// getNoncesWithTx returns the nonces for archived/not archived orders.
func getNoncesWithTx(ctx context.Context, txQueries *postgres.Queries,
	archived bool) ([][]byte, error) {

	params := postgres.GetOrderNoncesParams{
		Archived: archived,
	}
	return txQueries.GetOrderNonces(ctx, params)
}

// getOrdersWithTx fetches all the data related to orders given a flat slice
// of nonces and returns the constructed server orders.
func getOrdersWithTx(ctx context.Context, txQueries *postgres.Queries,
	nonces [][]byte) ([]order.ServerOrder, error) {

	orderRows, err := txQueries.GetOrders(ctx, nonces)
	if err != nil {
		return nil, err
	}

	allowedNodeIDs, notAllowedNodeIDs, err := getAllowedNodeIDsData(
		ctx, txQueries, nonces,
	)
	if err != nil {
		return nil, err
	}

	netwrokAddressSet, err := getNetworkAddressData(ctx, txQueries, nonces)
	if err != nil {
		return nil, err
	}

	orders := make([]order.ServerOrder, 0, len(orderRows))
	for _, row := range orderRows {
		o, err := unmarshalOrder(row)
		if err != nil {
			return nil, err
		}

		// Populate constrains for matching node ids.
		if len(allowedNodeIDs[o.Nonce()]) > 0 {
			o.Details().AllowedNodeIDs = allowedNodeIDs[o.Nonce()]
		}
		if len(notAllowedNodeIDs[o.Nonce()]) > 0 {
			o.Details().NotAllowedNodeIDs =
				notAllowedNodeIDs[o.Nonce()]
		}

		// Populate network adresses.
		o.ServerDetails().NodeAddrs = netwrokAddressSet[o.Nonce()]

		orders = append(orders, o)
	}

	return orders, nil
}

// getAllowedNodeIDsData fetches all the data related to allowed/not allowed
// node ids for the orders with the provided nonces.
func getAllowedNodeIDsData(ctx context.Context, txQueries *postgres.Queries,
	nonces [][]byte) (map[orderT.Nonce]NodeIDSlice,
	map[orderT.Nonce]NodeIDSlice, error) {

	allowedNodeIDs := make(map[orderT.Nonce]NodeIDSlice)
	notAllowedNodeIDs := make(map[orderT.Nonce]NodeIDSlice)

	rows, err := txQueries.GetOrderAllowedNodeIds(ctx, nonces)
	if err != nil {
		return nil, nil, err
	}

	for _, row := range rows {
		var (
			nonce  orderT.Nonce
			nodeID [33]byte
		)

		copy(nonce[:], row.Nonce)
		copy(nodeID[:], row.NodeKey)

		if row.Allowed {
			if _, ok := allowedNodeIDs[nonce]; !ok {
				allowedNodeIDs[nonce] = make(NodeIDSlice, 0, 10)
			}
			allowedNodeIDs[nonce] = append(
				allowedNodeIDs[nonce], nodeID,
			)
		} else {
			if _, ok := notAllowedNodeIDs[nonce]; !ok {
				notAllowedNodeIDs[nonce] = make(
					NodeIDSlice, 0, 10,
				)
			}
			notAllowedNodeIDs[nonce] = append(
				notAllowedNodeIDs[nonce], nodeID,
			)
		}
	}

	return allowedNodeIDs, notAllowedNodeIDs, nil
}

// getNetworkAddressData fetches all the data related to network addresses
// for the orders with the provided nonces.
func getNetworkAddressData(ctx context.Context, txQueries *postgres.Queries,
	nonces [][]byte) (map[orderT.Nonce][]net.Addr, error) {

	addresses := make(map[orderT.Nonce][]net.Addr)

	rows, err := txQueries.GetOrderNetworkAddresses(ctx, nonces)
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		var nonce orderT.Nonce
		copy(nonce[:], row.Nonce)

		if _, ok := addresses[nonce]; !ok {
			addresses[nonce] = make([]net.Addr, 0, 3)
		}

		host, port, err := net.SplitHostPort(row.Address)
		if err != nil {
			return nil, err
		}

		portNum, err := strconv.Atoi(port)
		if err != nil {
			return nil, err
		}

		switch {
		case tor.IsOnionHost(host):
			addresses[nonce] = append(
				addresses[nonce], &tor.OnionAddr{
					OnionService: host,
					Port:         portNum,
				},
			)

		case row.Network == "tcp":
			hostPort := net.JoinHostPort(host, port)
			addr, err := net.ResolveTCPAddr("tcp", hostPort)
			if err != nil {
				return nil, err
			}
			addresses[nonce] = append(addresses[nonce], addr)

		default:
			return nil, fmt.Errorf("unknown network: %v",
				row.Network)
		}
	}

	return addresses, nil
}

// getOrderWithNx fetches all the data related to an order in a single
// db tx using the provided queries struct.
func getOrderWithTx(ctx context.Context, txQueries *postgres.Queries,
	nonce orderT.Nonce) (order.ServerOrder, error) {

	orders, err := getOrdersWithTx(ctx, txQueries, [][]byte{nonce[:]})
	switch {
	case err != nil:
		return nil, err

	case len(orders) == 0:
		return nil, ErrNoOrder

	case len(orders) > 1:
		return nil, fmt.Errorf("unexpected number of orders returned: %d",
			len(orders))
	}

	return orders[0], nil
}

// unmarshalOrder deserializes an order.ServerOrder.
func unmarshalOrder(row postgres.GetOrdersRow) (order.ServerOrder, error) {
	var nonce orderT.Nonce
	copy(nonce[:], row.Nonce)

	clientKit := orderT.NewKit(nonce)

	var accKey [33]byte
	copy(accKey[:], row.TraderKey)
	clientKit.AcctKey = accKey

	clientKit.Version = orderT.Version(row.Version)
	clientKit.State = orderT.State(row.State)
	clientKit.FixedRate = uint32(row.FixedRate)
	clientKit.Amt = btcutil.Amount(row.Amount)
	clientKit.Units = orderT.SupplyUnit(row.Units)
	clientKit.UnitsUnfulfilled = orderT.SupplyUnit(row.UnitsUnfulfilled)
	clientKit.MinUnitsMatch = orderT.SupplyUnit(row.MinUnitsMatch)
	clientKit.MaxBatchFeeRate = chainfee.SatPerKWeight(row.MaxBatchFeeRate)
	clientKit.LeaseDuration = uint32(row.LeaseDuration)
	clientKit.ChannelType = orderT.ChannelType(row.ChannelType)

	serverKit := order.Kit{}

	copy(serverKit.Sig[:], row.Signature)
	copy(serverKit.MultiSigKey[:], row.MultisigKey)
	copy(serverKit.NodeKey[:], row.NodeKey)
	copy(serverKit.Lsat[:], row.TokenID)

	serverKit.UserAgent = row.UserAgent

	serverKit.CreatedAt = unmarshalSQLNullTime(row.CreatedAt)
	serverKit.ArchivedAt = unmarshalSQLNullTime(row.ArchivedAt)

	var serverOrder order.ServerOrder
	switch {
	case orderT.Type(row.Type) == orderT.TypeAsk:
		serverOrder = &order.Ask{
			Ask: orderT.Ask{
				Kit: *clientKit,
			},
			Kit: serverKit,
		}
	case row.Type == int16(orderT.TypeBid):
		serverOrder = &order.Bid{
			Bid: orderT.Bid{
				Kit: *clientKit,
			},
			Kit: serverKit,
		}
	default:
		err := fmt.Errorf("unable to unmarshal order: unknown type")
		return nil, err
	}

	bidO, ok := serverOrder.(*order.Bid)
	if ok {
		bidO.MinNodeTier = orderT.NodeTier(row.MinNodeTier.Int64)
		bidO.SelfChanBalance = btcutil.Amount(row.SelfChanBalance.Int64)
		bidO.IsSidecar = row.IsSidecar.Bool
	}

	return serverOrder, nil
}

// modifyOrderWithTx updates the data of an order in the db with the provided
// modifiers.
func modifyOrderWithTx(ctx context.Context, txQueries *postgres.Queries,
	nonce orderT.Nonce, modifiers ...order.Modifier) error {

	rows, err := txQueries.GetOrders(ctx, [][]byte{nonce[:]})
	switch {
	case err != nil:
		return err

	case len(rows) == 0:
		return ErrNoOrder

	case len(rows) != 1:
		return fmt.Errorf("get order(%x) returned an unexpected "+
			"number of orders: %d", nonce, len(rows))
	}

	dbOrder, err := unmarshalOrder(rows[0])
	if err != nil {
		return err
	}

	for _, modifier := range modifiers {
		modifier(dbOrder)
	}

	// If the order is getting archived record the current timestamp.
	if !rows[0].Archived && dbOrder.Details().State.Archived() {
		dbOrder.ServerDetails().ArchivedAt = time.Now().UTC().Truncate(
			time.Microsecond,
		)
	}

	return upsertOrderWithTx(ctx, txQueries, dbOrder)
}

// marshalSQLNullTime converts a time.Time to sql.NullTime. If the provided
// time was the zero value, a null sql.NullType is returned.
func marshalSQLNullTime(t time.Time) sql.NullTime {
	var res sql.NullTime
	if !t.IsZero() {
		res.Time, res.Valid = t, true
	}
	return res
}

// unmarshalSQLNullTime converts a sql.NullTime to time.Time. If it was null,
// the zero type for time.Time is returned.
func unmarshalSQLNullTime(t sql.NullTime) time.Time {
	var res time.Time
	if t.Valid {
		res = t.Time.UTC()
	}
	return res
}
