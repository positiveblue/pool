package subasta

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/llm/clmrpc"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"google.golang.org/grpc"
)

// adminRPCServer is a server that implements the admin server RPC interface and
// serves administrative and super user content.
type adminRPCServer struct {
	grpcServer *grpc.Server

	listener net.Listener
	serveWg  sync.WaitGroup

	started uint32 // To be used atomically.
	stopped uint32 // To be used atomically.

	quit chan struct{}
	wg   sync.WaitGroup

	mainRPCServer *rpcServer
	auctioneer    *Auctioneer
	store         *subastadb.EtcdStore
}

// newAdminRPCServer creates a new adminRPCServer.
func newAdminRPCServer(mainRPCServer *rpcServer, listener net.Listener,
	serverOpts []grpc.ServerOption, auctioneer *Auctioneer,
	store *subastadb.EtcdStore) *adminRPCServer {

	return &adminRPCServer{
		grpcServer:    grpc.NewServer(serverOpts...),
		listener:      listener,
		quit:          make(chan struct{}),
		mainRPCServer: mainRPCServer,
		auctioneer:    auctioneer,
		store:         store,
	}
}

// Start starts the adminRPCServer, making it ready to accept incoming requests.
func (s *adminRPCServer) Start() error {
	if !atomic.CompareAndSwapUint32(&s.started, 0, 1) {
		return nil
	}

	log.Infof("Starting admin server")

	s.serveWg.Add(1)
	go func() {
		defer s.serveWg.Done()

		log.Infof("Admin RPC server listening on %s", s.listener.Addr())
		err := s.grpcServer.Serve(s.listener)
		if err != nil && err != grpc.ErrServerStopped {
			log.Errorf("Admin RPC server stopped with error: %v",
				err)
		}
	}()

	log.Infof("Admin server is now active")

	return nil
}

// Stop stops the server.
func (s *adminRPCServer) Stop() {
	if !atomic.CompareAndSwapUint32(&s.stopped, 0, 1) {
		return
	}

	log.Info("Stopping admin server")

	close(s.quit)
	s.wg.Wait()

	log.Info("Stopping admin gRPC server and listener")
	s.grpcServer.Stop()
	s.serveWg.Wait()

	log.Info("Admin server stopped")
}

func (s *adminRPCServer) MasterAccount(ctx context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.MasterAccountResponse, error) {

	masterAcct, err := s.store.FetchAuctioneerAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to fetch master account: %v",
			err)
	}

	auctioneerKey := masterAcct.AuctioneerKey
	return &adminrpc.MasterAccountResponse{
		Outpoint: &adminrpc.OutPoint{
			Txid:        masterAcct.OutPoint.Hash[:],
			OutputIndex: masterAcct.OutPoint.Index,
		},
		Balance: int64(masterAcct.Balance),
		KeyDescriptor: &adminrpc.KeyDescriptor{
			RawKeyBytes: auctioneerKey.PubKey.SerializeCompressed(),
			KeyLoc: &adminrpc.KeyLocator{
				KeyFamily: int32(auctioneerKey.Family),
				KeyIndex:  int32(auctioneerKey.Index),
			},
		},
		BatchKey: masterAcct.BatchKey[:],
		Pending:  masterAcct.IsPending,
	}, nil
}

func (s *adminRPCServer) ConnectedTraders(_ context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.ConnectedTradersResponse, error) {

	result := &adminrpc.ConnectedTradersResponse{
		Streams: make(map[string]*adminrpc.PubKeyList),
	}
	streams := s.mainRPCServer.ConnectedStreams()
	for lsatID, stream := range streams {
		acctList := &adminrpc.PubKeyList{RawKeyBytes: make(
			[][]byte, 0, len(stream.Subscriptions),
		)}
		for acctKey := range stream.Subscriptions {
			acctList.RawKeyBytes = append(
				acctList.RawKeyBytes, acctKey[:],
			)
		}
		result.Streams[lsatID.String()] = acctList
	}

	return result, nil
}

func (s *adminRPCServer) BatchTick(_ context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.EmptyResponse, error) {

	// Force a new batch ticker event in the main auctioneer state machine.
	s.auctioneer.cfg.BatchTicker.Force <- time.Now()

	return &adminrpc.EmptyResponse{}, nil
}

func (s *adminRPCServer) PauseBatchTicker(_ context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.EmptyResponse, error) {

	// Pause the batch ticker of the main auctioneer state machine.
	s.auctioneer.cfg.BatchTicker.Pause()

	return &adminrpc.EmptyResponse{}, nil
}
func (s *adminRPCServer) ResumeBatchTicker(_ context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.EmptyResponse, error) {

	// Resume the batch ticker of the main auctioneer state machine.
	s.auctioneer.cfg.BatchTicker.Resume()

	return &adminrpc.EmptyResponse{}, nil
}

func (s *adminRPCServer) ListOrders(ctx context.Context,
	req *adminrpc.ListOrdersRequest) (*adminrpc.ListOrdersResponse, error) {

	getFn := s.store.GetOrders
	if req.Archived {
		getFn = s.store.GetArchivedOrders
	}

	dbOrders, err := getFn(ctx)
	if err != nil {
		return nil, err
	}

	rpcAsks := make([]*clmrpc.ServerAsk, 0, len(dbOrders)/2)
	rpcBids := make([]*clmrpc.ServerBid, 0, len(dbOrders)/2)
	for _, dbOrder := range dbOrders {
		switch o := dbOrder.(type) {
		case *order.Ask:
			rpcAsks = append(rpcAsks, &clmrpc.ServerAsk{
				Details:           marshallServerOrder(o),
				MaxDurationBlocks: o.MaxDuration(),
				Version:           uint32(o.Version),
			})
		case *order.Bid:
			rpcBids = append(rpcBids, &clmrpc.ServerBid{
				Details:           marshallServerOrder(o),
				MinDurationBlocks: o.MinDuration(),
				Version:           uint32(o.Version),
			})
		}
	}

	return &adminrpc.ListOrdersResponse{
		Asks: rpcAsks,
		Bids: rpcBids,
	}, nil
}

func (s *adminRPCServer) ListAccounts(ctx context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.ListAccountsResponse, error) {

	dbAccounts, err := s.store.Accounts(ctx)
	if err != nil {
		return nil, err
	}

	rpcAccounts := make([]*clmrpc.AuctionAccount, 0, len(dbAccounts))
	for _, dbAccount := range dbAccounts {
		rpcAccount, err := marshallServerAccount(dbAccount)
		if err != nil {
			return nil, err
		}
		rpcAccounts = append(rpcAccounts, rpcAccount)
	}

	return &adminrpc.ListAccountsResponse{
		Accounts: rpcAccounts,
	}, nil
}

func (s *adminRPCServer) AuctionStatus(ctx context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.AuctionStatusResponse, error) {

	currentBatchKey, err := s.store.BatchKey(ctx)
	if err != nil {
		return nil, err
	}

	batchTicker := s.auctioneer.cfg.BatchTicker
	pendingID := s.auctioneer.getPendingBatchID()
	result := &adminrpc.AuctionStatusResponse{
		PendingBatchId:    pendingID[:],
		CurrentBatchId:    currentBatchKey.SerializeCompressed(),
		BatchTickerActive: batchTicker.IsActive(),
		LastTimedTick:     uint64(batchTicker.LastTimedTick().Unix()),
		SecondsToNextTick: uint64(batchTicker.NextTickIn().Seconds()),
	}

	// Don't calculate the last key if the current one is the initial one as
	// that would result in a value that is never used anywhere.
	if !currentBatchKey.IsEqual(subastadb.InitialBatchKey) {
		lastBatchKey := DecrementBatchKey(currentBatchKey)
		result.LastBatchId = lastBatchKey.SerializeCompressed()
	}

	return result, nil
}

func (s *adminRPCServer) ListBatches(ctx context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.ListBatchesResponse, error) {

	batches := make([][]byte, 0)
	currentBatchKey, err := s.store.BatchKey(ctx)
	if err != nil {
		return nil, err
	}

	for {
		// Add the current key to the list. We'll include the initial
		// key as that was also used for a batch.
		batches = append(batches, currentBatchKey.SerializeCompressed())

		// We should eventually arrive at the starting point.
		if currentBatchKey.IsEqual(subastadb.InitialBatchKey) {
			break
		}

		// Walk back by decrementing the key.
		currentBatchKey = DecrementBatchKey(currentBatchKey)
	}

	// Reverse the list to put the oldest/initial batch first to get a more
	// stable output in the terminal.
	for i, j := 0, len(batches)-1; i < j; i, j = i+1, j-1 {
		batches[i], batches[j] = batches[j], batches[i]
	}

	return &adminrpc.ListBatchesResponse{
		Batches: batches,
	}, nil
}

func (s *adminRPCServer) BatchSnapshot(ctx context.Context,
	req *clmrpc.BatchSnapshotRequest) (*adminrpc.AdminBatchSnapshotResponse,
	error) {

	log.Tracef("[BatchSnapshot] batch_id=%x", req.BatchId)

	// If the passed batch ID wasn't specified, or is nil, then we'll fetch
	// the key for the current batch key (which isn't associated with a
	// cleared batch, then walk that back one to get to the most recent
	// batch.
	var (
		err error

		zeroID orderT.BatchID

		batchID     orderT.BatchID
		batchKey    *btcec.PublicKey
		prevBatchID []byte
	)

	if len(req.BatchId) == 0 || bytes.Equal(zeroID[:], req.BatchId) {
		currentBatchKey, err := s.store.BatchKey(context.Background())
		if err != nil {
			return nil, fmt.Errorf("unable to fetch latest "+
				"batch key: %v", err)
		}

		batchKey = DecrementBatchKey(currentBatchKey)
		batchID = orderT.NewBatchID(batchKey)
	} else {
		copy(batchID[:], req.BatchId)

		batchKey, err = btcec.ParsePubKey(req.BatchId, btcec.S256())
		if err != nil {
			return nil, fmt.Errorf("unable to parse "+
				"batch ID (%x): %v", req.BatchId, err)
		}
	}

	// Now that we have the batch key, we'll also derive the _prior_ batch
	// key so the client can use this as a sort of linked list to navigate
	// the batch chain. Unless of course we reached the initial batch key.
	if !batchKey.IsEqual(subastadb.InitialBatchKey) {
		prevBatchKey := DecrementBatchKey(batchKey)
		prevBatchID = prevBatchKey.SerializeCompressed()
	}

	// Next, we'll fetch the targeted batch snapshot.
	batchSnapshot, batchTx, err := s.store.GetBatchSnapshot(ctx, batchID)
	if err != nil {
		return nil, err
	}

	resp := &adminrpc.AdminBatchSnapshotResponse{
		Version:           uint32(orderT.CurrentVersion),
		BatchId:           batchID[:],
		PrevBatchId:       prevBatchID,
		ClearingPriceRate: uint32(batchSnapshot.ClearingPrice),
	}

	// The response for this call is a bit simpler than the
	// RelevantBatchSnapshot call, in that we only need to return the set
	// of orders, and not also the accounts diffs.
	resp.MatchedOrders = make(
		[]*adminrpc.AdminMatchedOrderSnapshot, len(batchSnapshot.Orders),
	)
	for i, o := range batchSnapshot.Orders {
		ask := o.Details.Ask
		bid := o.Details.Bid
		quote := o.Details.Quote

		resp.MatchedOrders[i] = &adminrpc.AdminMatchedOrderSnapshot{
			Ask: &clmrpc.ServerAsk{
				Details:           marshallServerOrder(ask),
				MaxDurationBlocks: ask.MaxDuration(),
				Version:           uint32(ask.Version),
			},
			Bid: &clmrpc.ServerBid{
				Details:           marshallServerOrder(bid),
				MinDurationBlocks: bid.MinDuration(),
				Version:           uint32(bid.Version),
			},
			MatchingRate:     uint32(quote.MatchingRate),
			TotalSatsCleared: uint64(quote.TotalSatsCleared),
			UnitsMatched:     uint32(quote.UnitsMatched),
		}
	}

	// Finally, we'll serialize the batch transaction, which completes our
	// response.
	var txBuf bytes.Buffer
	if err := batchTx.Serialize(&txBuf); err != nil {
		return nil, err
	}

	resp.BatchTx = txBuf.Bytes()
	resp.BatchTxId = batchTx.TxHash().String()

	return resp, nil
}

func (s *adminRPCServer) ListBans(ctx context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.ListBansResponse, error) {

	// Collect banned accounts.
	accts, err := s.store.ListBannedAccounts(ctx)
	if err != nil {
		return nil, err
	}
	rpcAccts := make(map[string]*adminrpc.BanInfo, len(accts))
	for acctKey, banInfo := range accts {
		rpcAccts[hex.EncodeToString(acctKey[:])] = &adminrpc.BanInfo{
			Height:   banInfo.Height,
			Duration: banInfo.Duration,
		}
	}

	// Collect banned nodes.
	nodes, err := s.store.ListBannedNodes(ctx)
	if err != nil {
		return nil, err
	}
	rpcNodes := make(map[string]*adminrpc.BanInfo, len(nodes))
	for nodeKey, banInfo := range accts {
		rpcNodes[hex.EncodeToString(nodeKey[:])] = &adminrpc.BanInfo{
			Height:   banInfo.Height,
			Duration: banInfo.Duration,
		}
	}

	return &adminrpc.ListBansResponse{
		BannedAccounts: rpcAccts,
		BannedNodes:    rpcNodes,
	}, nil
}

func (s *adminRPCServer) RemoveBan(ctx context.Context,
	req *adminrpc.RemoveBanRequest) (*adminrpc.EmptyResponse, error) {

	var (
		removeFn func(context.Context, *btcec.PublicKey) error
		keyBytes []byte
	)
	switch {
	case req.GetAccount() != nil:
		keyBytes = req.GetAccount()
		removeFn = s.store.RemoveAccountBan

	case req.GetNode() != nil:
		keyBytes = req.GetNode()
		removeFn = s.store.RemoveNodeBan

	default:
		return nil, fmt.Errorf("must set either node or account")
	}

	key, err := btcec.ParsePubKey(keyBytes, btcec.S256())
	if err != nil {
		return nil, err
	}
	err = removeFn(ctx, key)
	if err != nil {
		return nil, err
	}

	return &adminrpc.EmptyResponse{}, nil
}

func (s *adminRPCServer) RemoveReservation(ctx context.Context,
	req *adminrpc.RemoveReservationRequest) (*adminrpc.EmptyResponse, error) {

	var tokenID *lsat.TokenID
	switch {
	case req.GetTraderKey() != nil:
		traderKey, err := btcec.ParsePubKey(
			req.GetTraderKey(), btcec.S256(),
		)
		if err != nil {
			return nil, err
		}
		_, tokenID, err = s.store.HasReservationForKey(ctx, traderKey)
		if err != nil {
			return nil, err
		}

	case req.GetLsat() != nil:
		tokenID = &lsat.TokenID{}
		copy(tokenID[:], req.GetLsat())

	default:
		return nil, fmt.Errorf("must set either node or account")
	}

	err := s.store.RemoveReservation(ctx, *tokenID)
	if err != nil {
		return nil, err
	}

	return &adminrpc.EmptyResponse{}, nil
}
