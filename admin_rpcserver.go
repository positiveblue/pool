package subasta

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/aperture/lsat"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/sweep"
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

// MasterAccount returns information about the current state of the master
// account.
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

// ConnectedTraders returns a map of all connected traders identified by their
// LSAT ID and the account keys they have subscribed to.
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

// BatchTick forces the auctioneer to try to make a batch now.
func (s *adminRPCServer) BatchTick(_ context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.EmptyResponse, error) {

	// Force a new batch ticker event in the main auctioneer state machine.
	s.auctioneer.cfg.BatchTicker.ForceTick()

	return &adminrpc.EmptyResponse{}, nil
}

// PauseBatchTicker halts the automatic batch making ticker so only manually
// forced ticks will result in a match making attempt.
func (s *adminRPCServer) PauseBatchTicker(_ context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.EmptyResponse, error) {

	// Pause the batch ticker of the main auctioneer state machine.
	s.auctioneer.cfg.BatchTicker.Pause()

	// Make sure any manual ticks don't resume the ticker.
	s.auctioneer.auctionHaltedMtx.Lock()
	s.auctioneer.auctionHalted = true
	s.auctioneer.auctionHaltedMtx.Unlock()

	return &adminrpc.EmptyResponse{}, nil
}

// ResumeBatchTicker resumes the automatic batch making ticker.
func (s *adminRPCServer) ResumeBatchTicker(_ context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.EmptyResponse, error) {

	// Resume the batch ticker of the main auctioneer state machine.
	s.auctioneer.cfg.BatchTicker.Resume()

	// Resume normal operation, the auctioneer can now pause and resume
	// the ticker by itself normally.
	s.auctioneer.auctionHaltedMtx.Lock()
	s.auctioneer.auctionHalted = false
	s.auctioneer.auctionHaltedMtx.Unlock()

	return &adminrpc.EmptyResponse{}, nil
}

// ListOrders lists all currently known orders of the auctioneer database. A
// flag can specify if all active or all archived orders should be returned.
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

	rpcAsks := make([]*poolrpc.ServerAsk, 0, len(dbOrders)/2)
	rpcBids := make([]*poolrpc.ServerBid, 0, len(dbOrders)/2)
	for _, dbOrder := range dbOrders {
		switch o := dbOrder.(type) {
		case *order.Ask:
			rpcAsks = append(rpcAsks, &poolrpc.ServerAsk{
				Details:             marshallServerOrder(o),
				LeaseDurationBlocks: o.LeaseDuration(),
				Version:             uint32(o.Version),
			})
		case *order.Bid:
			nodeTier, err := marshallNodeTier(o.MinNodeTier)
			if err != nil {
				return nil, err
			}

			rpcBids = append(rpcBids, &poolrpc.ServerBid{
				Details:             marshallServerOrder(o),
				LeaseDurationBlocks: o.LeaseDuration(),
				Version:             uint32(o.Version),
				MinNodeTier:         nodeTier,
			})
		}
	}

	return &adminrpc.ListOrdersResponse{
		Asks: rpcAsks,
		Bids: rpcBids,
	}, nil
}

// AccountDetails retrieves the details of specified account from the store.
func (s *adminRPCServer) AccountDetails(ctx context.Context,
	req *adminrpc.AccountDetailsRequest) (*poolrpc.AuctionAccount, error) {

	acctKey, err := btcec.ParsePubKey(req.AccountKey, btcec.S256())
	if err != nil {
		return nil, err
	}
	acct, err := s.store.Account(ctx, acctKey, req.IncludeDiff)
	if err != nil {
		return nil, err
	}
	return marshallServerAccount(acct)
}

// EditAccount edits the details of an existing account.
func (s *adminRPCServer) EditAccount(ctx context.Context,
	req *adminrpc.EditAccountRequest) (*poolrpc.AuctionAccount, error) {

	// Retrieve the account with the associated key.
	acctKey, err := btcec.ParsePubKey(req.AccountKey, btcec.S256())
	if err != nil {
		return nil, err
	}
	acct, err := s.store.Account(ctx, acctKey, req.EditDiff)
	if err != nil {
		return nil, err
	}

	// Parse any fields we should update from the request.
	var mods []account.Modifier
	if req.Value != 0 {
		mods = append(
			mods, account.ValueModifier(btcutil.Amount(req.Value)),
		)
	}
	if req.RotateBatchKey != 0 {
		rotate := int(req.RotateBatchKey)
		mod := account.IncrementBatchKey()
		if req.RotateBatchKey < 0 {
			rotate *= -1
			mod = account.DecrementBatchKey()
		}
		for i := 0; i < rotate; i++ {
			mods = append(mods, mod)
		}
	}
	if req.Outpoint != nil {
		hash, err := chainhash.NewHash(req.Outpoint.Txid)
		if err != nil {
			return nil, err
		}
		mods = append(mods, account.OutPointModifier(wire.OutPoint{
			Hash:  *hash,
			Index: req.Outpoint.OutputIndex,
		}))
	}
	if len(req.LatestTx) > 0 {
		var latestTx wire.MsgTx
		err := latestTx.Deserialize(bytes.NewReader(req.LatestTx))
		if err != nil {
			return nil, err
		}
		mods = append(mods, account.LatestTxModifier(&latestTx))
	}

	// Either update the main account state or its diff as instructed per
	// the request.
	if req.EditDiff {
		err := s.store.UpdateAccountDiff(ctx, acctKey, mods)
		if err != nil {
			return nil, err
		}

		// Fetch the account again to return the new staged diff.
		acct, err = s.store.Account(ctx, acctKey, req.EditDiff)
		if err != nil {
			return nil, err
		}
	} else {
		err := s.store.UpdateAccount(ctx, acct, mods...)
		if err != nil {
			return nil, err
		}
	}

	return marshallServerAccount(acct)
}

// DeleteAccountDiff deletes the staged diff of an account.
func (s *adminRPCServer) DeleteAccountDiff(ctx context.Context,
	req *adminrpc.DeleteAccountDiffRequest) (*adminrpc.EmptyResponse, error) {

	acctKey, err := btcec.ParsePubKey(req.AccountKey, btcec.S256())
	if err != nil {
		return nil, err
	}

	if err := s.store.DeleteAccountDiff(ctx, acctKey); err != nil {
		return nil, err
	}

	return &adminrpc.EmptyResponse{}, nil
}

// ListAccounts returns a list of all currently known accounts of the auctioneer
// database.
func (s *adminRPCServer) ListAccounts(ctx context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.ListAccountsResponse, error) {

	dbAccounts, err := s.store.Accounts(ctx)
	if err != nil {
		return nil, err
	}

	rpcAccounts := make([]*poolrpc.AuctionAccount, 0, len(dbAccounts))
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

// AuctionState returns information about the current state of the auctioneer
// and the auction itself.
func (s *adminRPCServer) AuctionStatus(ctx context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.AuctionStatusResponse, error) {

	currentBatchKey, err := s.store.BatchKey(ctx)
	if err != nil {
		return nil, err
	}

	state, err := s.auctioneer.cfg.DB.AuctionState()
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
		AuctionState:      state.String(),
	}

	// Don't calculate the last key if the current one is the initial one as
	// that would result in a value that is never used anywhere.
	if !currentBatchKey.IsEqual(subastadb.InitialBatchKey) {
		lastBatchKey := poolscript.DecrementKey(currentBatchKey)
		result.LastBatchId = lastBatchKey.SerializeCompressed()
	}

	return result, nil
}

// ListBatches returns a list of all known batch IDs, including the most recent
// one which hasn't been used for a batch yet but will be for the next one.
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
		currentBatchKey = poolscript.DecrementKey(currentBatchKey)
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

// BatchSnapshot returns the stored snapshot information of one batch specified
// by its ID.
func (s *adminRPCServer) BatchSnapshot(ctx context.Context,
	req *poolrpc.BatchSnapshotRequest) (*adminrpc.AdminBatchSnapshotResponse,
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

		batchKey = poolscript.DecrementKey(currentBatchKey)
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
		prevBatchKey := poolscript.DecrementKey(batchKey)
		prevBatchID = prevBatchKey.SerializeCompressed()
	}

	// Next, we'll fetch the targeted batch snapshot.
	batchSnapshot, err := s.store.GetBatchSnapshot(ctx, batchID)
	if err != nil {
		return nil, err
	}
	batch := batchSnapshot.OrderBatch

	resp := &adminrpc.AdminBatchSnapshotResponse{
		Version:           uint32(orderT.CurrentVersion),
		BatchId:           batchID[:],
		PrevBatchId:       prevBatchID,
		ClearingPriceRate: uint32(batch.ClearingPrice),
	}

	// The response for this call is a bit simpler than the
	// RelevantBatchSnapshot call, in that we only need to return the set
	// of orders, and not also the accounts diffs.
	resp.MatchedOrders = make(
		[]*adminrpc.AdminMatchedOrderSnapshot, len(batch.Orders),
	)
	for i, o := range batch.Orders {
		ask := o.Details.Ask
		bid := o.Details.Bid
		quote := o.Details.Quote

		resp.MatchedOrders[i] = &adminrpc.AdminMatchedOrderSnapshot{
			Ask: &poolrpc.ServerAsk{
				Details:             marshallServerOrder(ask),
				LeaseDurationBlocks: ask.LeaseDuration(),
				Version:             uint32(ask.Version),
			},
			Bid: &poolrpc.ServerBid{
				Details:             marshallServerOrder(bid),
				LeaseDurationBlocks: bid.LeaseDuration(),
				Version:             uint32(bid.Version),
			},
			MatchingRate:     uint32(quote.MatchingRate),
			TotalSatsCleared: uint64(quote.TotalSatsCleared),
			UnitsMatched:     uint32(quote.UnitsMatched),
		}
	}

	// Finally, we'll serialize the batch transaction, which completes our
	// response.
	var txBuf bytes.Buffer
	if err := batchSnapshot.BatchTx.Serialize(&txBuf); err != nil {
		return nil, err
	}

	resp.BatchTx = txBuf.Bytes()
	resp.BatchTxId = batchSnapshot.BatchTx.TxHash().String()

	return resp, nil
}

// ListBans returns a list of all currently banned accounts and nodes stored in
// the auctioneer's database.
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
	for nodeKey, banInfo := range nodes {
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

// RemoveBan removes the ban for either a node or account ID.
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

// AddBan adds a ban for either a node or account ID.
func (s *adminRPCServer) AddBan(ctx context.Context,
	req *adminrpc.BanRequest) (*adminrpc.EmptyResponse, error) {

	var (
		banFn func(context.Context, *btcec.PublicKey, uint32,
			uint32) error
		keyBytes []byte
	)
	switch {
	case req.GetAccount() != nil:
		keyBytes = req.GetAccount()
		banFn = s.store.SetAccountBanInfo

	case req.GetNode() != nil:
		keyBytes = req.GetNode()
		banFn = s.store.SetNodeBanInfo

	default:
		return nil, fmt.Errorf("must set either node or account")
	}

	if req.Duration == 0 {
		return nil, fmt.Errorf("must specify duration in blocks")
	}

	key, err := btcec.ParsePubKey(keyBytes, btcec.S256())
	if err != nil {
		return nil, err
	}
	err = banFn(ctx, key, s.mainRPCServer.bestHeight(), req.Duration)
	if err != nil {
		return nil, err
	}

	return &adminrpc.EmptyResponse{}, nil
}

// RemoveReservation removes the reservation of either an account key or an LSAT
// ID. This can be used to manually un-stuck a trader that crashed during the
// account funding process.
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

// FundingConflicts returns a map of all recorded channel funding conflicts that
// occurred during match making attempts. These conflicts are recorded if two
// nodes either can't connect to each other or the channel funding negotiation
// fails for another reason. An entry in this list will prevent the match maker
// from matching orders between the two reported nodes in the future. The
// conflict is currently only held in memory and won't survive a restart of the
// auctioneer.
func (s *adminRPCServer) FundingConflicts(context.Context,
	*adminrpc.EmptyRequest) (*adminrpc.FundingConflictsResponse, error) {

	resp := &adminrpc.FundingConflictsResponse{
		Conflicts: make(map[string]*adminrpc.ConflictList),
	}
	conflictMap := s.auctioneer.cfg.FundingConflicts.Export()
	for reporter, subjectMap := range conflictMap {
		var rpcConflicts []*adminrpc.Conflict
		for subject, conflicts := range subjectMap {
			for _, conflict := range conflicts {
				subjectHex := hex.EncodeToString(subject[:])
				rpcConflict := &adminrpc.Conflict{
					Subject:         subjectHex,
					Reason:          conflict.Reason,
					ReportTimestamp: conflict.Reported.Unix(),
				}
				rpcConflicts = append(rpcConflicts, rpcConflict)
			}
		}

		reporterHex := hex.EncodeToString(reporter[:])
		resp.Conflicts[reporterHex] = &adminrpc.ConflictList{
			Conflicts: rpcConflicts,
		}
	}

	return resp, nil
}

// ClearConflicts removes all entries in the funding conflict map.
func (s *adminRPCServer) ClearConflicts(context.Context,
	*adminrpc.EmptyRequest) (*adminrpc.EmptyResponse, error) {

	s.auctioneer.cfg.FundingConflicts.Clear()

	return &adminrpc.EmptyResponse{}, nil
}

func (s *adminRPCServer) BumpBatchFeeRate(ctx context.Context,
	req *adminrpc.BumpBatchFeeRateRequest) (*adminrpc.EmptyResponse, error) {

	feePref := sweep.FeePreference{
		ConfTarget: req.ConfTarget,
		FeeRate:    chainfee.SatPerKWeight(req.FeeRateSatPerKw),
	}
	if err := s.auctioneer.RequestBatchFeeBump(feePref); err != nil {
		return nil, err
	}

	return &adminrpc.EmptyResponse{}, nil
}

// QueryNodeRating returns the current rating for a given node.
func (s *adminRPCServer) QueryNodeRating(ctx context.Context,
	req *adminrpc.RatingQueryRequest) (*adminrpc.RatingQueryResponse, error) {

	var pub [33]byte
	copy(pub[:], req.NodeKey)

	nodeRating := s.mainRPCServer.ratingAgency.RateNode(pub)

	return &adminrpc.RatingQueryResponse{
		NodeKey:  pub[:],
		NodeTier: uint32(nodeRating),
	}, nil
}

// ModifyRatingResponse attempts to modify the rating of a given node.
func (s *adminRPCServer) ModifyNodeRatings(ctx context.Context,
	req *adminrpc.ModifyRatingRequest) (*adminrpc.ModifyRatingResponse, error) {

	var pub [33]byte
	copy(pub[:], req.NodeKey)

	err := s.mainRPCServer.ratingsDB.ModifyNodeRating(
		ctx, pub, orderT.NodeTier(req.NewNodeTier),
	)
	if err != nil {
		return nil, err
	}

	return &adminrpc.ModifyRatingResponse{}, nil
}

// ListNodeRatingsResponse lists the current set of valid node ratings.
func (s *adminRPCServer) ListNodeRatings(ctx context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.ListNodeRatingsResponse, error) {

	nodeRatings, err := s.store.NodeRatings(ctx)
	if err != nil {
		return nil, err
	}

	resp := &adminrpc.ListNodeRatingsResponse{
		NodeRatings: make([]*adminrpc.NodeRating, 0, len(nodeRatings)),
	}
	for nodeKey, nodeRating := range nodeRatings {
		pubKey := nodeKey

		resp.NodeRatings = append(resp.NodeRatings, &adminrpc.NodeRating{
			NodeKey:  pubKey[:],
			NodeTier: uint32(nodeRating),
		})
	}

	return resp, nil
}
