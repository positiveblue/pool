package subasta

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/wtxmgr"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/faraday/fiat"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/pool/auctioneerrpc"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/accounting"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightninglabs/subasta/ban"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/status"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/traderterms"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/sweep"
	"google.golang.org/grpc"
)

var (
	// defaultLeaseDuration is the default lease duration we use for locking
	// additional inputs to our batch.
	defaultLeaseDuration = time.Minute * 10
)

// adminRPCServer is a server that implements the admin server RPC interface and
// serves administrative and super user content.
type adminRPCServer struct {
	network    *chaincfg.Params
	grpcServer *grpc.Server

	listener net.Listener
	serveWg  sync.WaitGroup

	started uint32 // To be used atomically.
	stopped uint32 // To be used atomically.

	mainRPCServer  *rpcServer
	auctioneer     *Auctioneer
	statusReporter *status.Reporter
	store          subastadb.AdminStore

	durationBuckets *order.DurationBuckets

	lightningClient lndclient.LightningClient
	wallet          lndclient.WalletKitClient

	lockID wtxmgr.LockID
}

// newAdminRPCServer creates a new adminRPCServer.
func newAdminRPCServer(network *chaincfg.Params, mainRPCServer *rpcServer,
	listener net.Listener, serverOpts []grpc.ServerOption,
	auctioneer *Auctioneer, store subastadb.AdminStore,
	durationBuckets *order.DurationBuckets,
	wallet lndclient.WalletKitClient,
	lightningClient lndclient.LightningClient,
	statusReporter *status.Reporter) (*adminRPCServer, error) {

	// Generate a lock ID for the utxo leases that this instance is going to
	// request.
	var lockID wtxmgr.LockID
	if _, err := rand.Read(lockID[:]); err != nil {
		return nil, err
	}

	return &adminRPCServer{
		network:         network,
		grpcServer:      grpc.NewServer(serverOpts...),
		listener:        listener,
		mainRPCServer:   mainRPCServer,
		auctioneer:      auctioneer,
		store:           store,
		statusReporter:  statusReporter,
		durationBuckets: durationBuckets,
		lightningClient: lightningClient,
		wallet:          wallet,
		lockID:          lockID,
	}, nil
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
		Version:  int32(masterAcct.Version),
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
		traderKeys := make([][]byte, 0, len(stream.Subscriptions))
		for acctKey := range stream.Subscriptions {
			traderKey := make([]byte, len(acctKey[:]))
			copy(traderKey, acctKey[:])
			traderKeys = append(traderKeys, traderKey)
		}
		result.Streams[lsatID.String()] = &adminrpc.PubKeyList{
			RawKeyBytes: traderKeys,
		}
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

	rpcAsks := make([]*adminrpc.ServerAsk, 0, len(dbOrders)/2)
	rpcBids := make([]*adminrpc.ServerBid, 0, len(dbOrders)/2)
	for _, dbOrder := range dbOrders {
		rpcDetails, err := marshallServerOrder(dbOrder)
		if err != nil {
			return nil, err
		}

		switch o := dbOrder.(type) {
		case *order.Ask:
			rpcAsks = append(rpcAsks, &adminrpc.ServerAsk{
				Details:             rpcDetails,
				LeaseDurationBlocks: o.LeaseDuration(),
				Version:             uint32(o.Version),
				State: auctioneerrpc.OrderState(
					o.Details().State,
				),
				UserAgent: o.UserAgent,
			})
		case *order.Bid:
			nodeTier, err := marshallNodeTier(o.MinNodeTier)
			if err != nil {
				return nil, err
			}

			rpcBids = append(rpcBids, &adminrpc.ServerBid{
				Details:             rpcDetails,
				LeaseDurationBlocks: o.LeaseDuration(),
				Version:             uint32(o.Version),
				State:               auctioneerrpc.OrderState(o.Details().State),
				MinNodeTier:         nodeTier,
				UserAgent:           o.UserAgent,
				SelfChanBalance:     uint64(o.SelfChanBalance),
				IsSidecar:           o.IsSidecar,
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
	req *adminrpc.AccountDetailsRequest) (*adminrpc.Account, error) {

	acctKey, err := btcec.ParsePubKey(req.AccountKey)
	if err != nil {
		return nil, err
	}
	acct, err := s.store.Account(ctx, acctKey, req.IncludeDiff)
	if err != nil {
		return nil, err
	}
	return marshallAdminAccount(acct)
}

// EditAccount edits the details of an existing account.
func (s *adminRPCServer) EditAccount(ctx context.Context,
	req *adminrpc.EditAccountRequest) (*adminrpc.Account, error) {

	// Retrieve the account with the associated key.
	acctKey, err := btcec.ParsePubKey(req.AccountKey)
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
		acct, err = s.store.UpdateAccount(ctx, acct, mods...)
		if err != nil {
			return nil, err
		}
	}

	return marshallAdminAccount(acct)
}

// DeleteAccountDiff deletes the staged diff of an account.
func (s *adminRPCServer) DeleteAccountDiff(ctx context.Context,
	req *adminrpc.DeleteAccountDiffRequest) (*adminrpc.EmptyResponse, error) {

	acctKey, err := btcec.ParsePubKey(req.AccountKey)
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

	rpcAccounts := make([]*adminrpc.Account, 0, len(dbAccounts))
	for _, dbAccount := range dbAccounts {
		rpcAccount, err := marshallAdminAccount(dbAccount)
		if err != nil {
			return nil, err
		}
		rpcAccounts = append(rpcAccounts, rpcAccount)
	}

	return &adminrpc.ListAccountsResponse{
		Accounts: rpcAccounts,
	}, nil
}

// AuctionStatus returns information about the current state of the auctioneer
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

	feeRate, _, err := s.auctioneer.EstimateNextBatchFee(ctx)
	if err != nil {
		return nil, err
	}

	durationBuckets, err := s.store.LeaseDurations(ctx)
	if err != nil {
		return nil, err
	}
	rpcDurationBuckets := make(map[uint32]auctioneerrpc.DurationBucketState)
	for duration, state := range durationBuckets {
		rpcState, err := marshallDurationBucketState(state)
		if err != nil {
			return nil, err
		}
		rpcDurationBuckets[duration] = rpcState
	}

	batchTicker := s.auctioneer.cfg.BatchTicker
	pendingID := s.auctioneer.getPendingBatchID()
	result := &adminrpc.AuctionStatusResponse{
		PendingBatchId:           pendingID[:],
		CurrentBatchId:           currentBatchKey.SerializeCompressed(),
		BatchTickerActive:        batchTicker.IsActive(),
		LastTimedTick:            uint64(batchTicker.LastTimedTick().Unix()),
		SecondsToNextTick:        uint64(batchTicker.NextTickIn().Seconds()),
		AuctionState:             state.String(),
		LeaseDurationBuckets:     rpcDurationBuckets,
		ServerState:              s.statusReporter.GetStatus().Status,
		NextBatchFeeRateSatPerKw: uint32(feeRate),
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
	req *auctioneerrpc.BatchSnapshotRequest) (*adminrpc.AdminBatchSnapshotResponse,
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

		batchKey, err = btcec.ParsePubKey(req.BatchId)
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
		Version: uint32(batch.Version),
		MatchedOrders: make(
			map[uint32]*adminrpc.AdminMatchedOrderSnapshots,
		),
		BatchId:             batchID[:],
		PrevBatchId:         prevBatchID,
		ClearingPriceRate:   make(map[uint32]uint32),
		CreationTimestampNs: uint64(batch.CreationTimestamp.UnixNano()),
	}

	// The response for this call is a bit simpler than the
	// RelevantBatchSnapshot call, in that we only need to return the set
	// of orders, and not also the accounts diffs.
	for duration, subBatch := range batch.SubBatches {
		snapshots := make(
			[]*adminrpc.AdminMatchedOrderSnapshot, len(subBatch),
		)
		for i, o := range subBatch {
			ask := o.Details.Ask
			bid := o.Details.Bid
			quote := o.Details.Quote

			askDetails, err := marshallServerOrder(ask)
			if err != nil {
				return nil, err
			}
			bidDetails, err := marshallServerOrder(bid)
			if err != nil {
				return nil, err
			}

			snapshots[i] = &adminrpc.AdminMatchedOrderSnapshot{
				Ask: &auctioneerrpc.ServerAsk{
					Details:             askDetails,
					LeaseDurationBlocks: ask.LeaseDuration(),
					Version:             uint32(ask.Version),
				},
				Bid: &auctioneerrpc.ServerBid{
					Details:             bidDetails,
					LeaseDurationBlocks: bid.LeaseDuration(),
					Version:             uint32(bid.Version),
					SelfChanBalance: uint64(
						bid.SelfChanBalance,
					),
				},
				MatchingRate:     uint32(quote.MatchingRate),
				TotalSatsCleared: uint64(quote.TotalSatsCleared),
				UnitsMatched:     uint32(quote.UnitsMatched),
			}
		}

		resp.MatchedOrders[duration] = &adminrpc.AdminMatchedOrderSnapshots{
			Snapshots: snapshots,
		}
		resp.ClearingPriceRate[duration] = uint32(
			batch.ClearingPrices[duration],
		)
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

	currentHeight := s.mainRPCServer.bestHeight()
	// Collect banned accounts.
	accts, err := s.store.ListBannedAccounts(ctx, currentHeight)
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
	nodes, err := s.store.ListBannedNodes(ctx, currentHeight)
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

	key, err := btcec.ParsePubKey(keyBytes)
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
		banFn func(context.Context, *btcec.PublicKey,
			*ban.Info) error
		keyBytes []byte
	)
	switch {
	case req.GetAccount() != nil:
		keyBytes = req.GetAccount()
		banFn = s.store.BanAccount

	case req.GetNode() != nil:
		keyBytes = req.GetNode()
		banFn = s.store.BanNode

	default:
		return nil, fmt.Errorf("must set either node or account")
	}

	if req.Duration == 0 {
		return nil, fmt.Errorf("must specify duration in blocks")
	}

	key, err := btcec.ParsePubKey(keyBytes)
	if err != nil {
		return nil, err
	}
	banInfo := &ban.Info{
		Height:   s.mainRPCServer.bestHeight(),
		Duration: req.Duration,
	}
	err = banFn(ctx, key, banInfo)
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
		traderKey, err := btcec.ParsePubKey(req.GetTraderKey())
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

func (s *adminRPCServer) BumpBatchFeeRate(_ context.Context,
	req *adminrpc.BumpBatchFeeRateRequest) (*adminrpc.EmptyResponse, error) {

	feePref := sweep.FeePreference{}
	switch {
	case req.FeeRateSatPerKw > 0:
		feePref.FeeRate = chainfee.SatPerKWeight(req.FeeRateSatPerKw)

	case req.FeeRateSatPerVbyte > 0:
		satPerKVByte := chainfee.SatPerKVByte(
			req.FeeRateSatPerVbyte * 1000,
		)
		feePref.FeeRate = satPerKVByte.FeePerKWeight()

	case req.ConfTarget > 0:
		feePref.ConfTarget = req.ConfTarget

	default:
		return nil, fmt.Errorf("must set either conf target or one " +
			"of the fee rates")
	}
	if err := s.auctioneer.RequestBatchFeeBump(feePref); err != nil {
		return nil, err
	}

	return &adminrpc.EmptyResponse{}, nil
}

// QueryNodeRating returns the current rating for a given node.
func (s *adminRPCServer) QueryNodeRating(_ context.Context,
	req *adminrpc.RatingQueryRequest) (*adminrpc.RatingQueryResponse, error) {

	var pub [33]byte
	copy(pub[:], req.NodeKey)

	nodeRating := s.mainRPCServer.ratingAgency.RateNode(pub)

	return &adminrpc.RatingQueryResponse{
		NodeKey:  pub[:],
		NodeTier: uint32(nodeRating),
	}, nil
}

// ModifyNodeRatings attempts to modify the rating of a given node.
func (s *adminRPCServer) ModifyNodeRatings(ctx context.Context,
	req *adminrpc.ModifyRatingRequest) (*adminrpc.ModifyRatingResponse,
	error) {

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

// ListNodeRatings lists the current set of valid node ratings.
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

func (s *adminRPCServer) StoreLeaseDuration(ctx context.Context,
	in *adminrpc.LeaseDuration) (*adminrpc.EmptyResponse, error) {

	// Sanity check to avoid adding invalid buckets by accident.
	if in.Duration == 0 ||
		in.Duration%orderT.MinimumOrderDurationBlocks != 0 {

		return nil, fmt.Errorf("invalid duration %d, must be non-zero "+
			"and multiple of %d", in.Duration,
			orderT.MinimumOrderDurationBlocks)
	}

	marketState, err := parseRPCDurationBucketState(in.BucketState)
	if err != nil {
		return nil, fmt.Errorf("error parsing bucket state: %v", err)
	}

	err = s.store.StoreLeaseDuration(ctx, in.Duration, marketState)
	if err != nil {
		return nil, fmt.Errorf("error storing duration: %v", err)
	}

	// We've updated the database, let's now also update the in-memory state
	// of the bucket instance that the order book also uses. If that market
	// already exists, it will just be updated to the new state.
	s.durationBuckets.PutMarket(in.Duration, marketState)

	return &adminrpc.EmptyResponse{}, nil
}

func (s *adminRPCServer) RemoveLeaseDuration(ctx context.Context,
	in *adminrpc.LeaseDuration) (*adminrpc.EmptyResponse, error) {

	// Make sure the bucket we're about to remove doesn't have any active
	// orders.
	activeOrders, err := s.store.GetOrders(ctx)
	if err != nil {
		return nil, fmt.Errorf("error getting active orders: %v", err)
	}
	for _, activeOrder := range activeOrders {
		if activeOrder.Details().LeaseDuration == in.Duration {
			return nil, fmt.Errorf("market with duration %d still "+
				"has active order %v", in.Duration,
				activeOrder.Nonce())
		}
	}

	err = s.store.RemoveLeaseDuration(ctx, in.Duration)
	if err != nil {
		return nil, fmt.Errorf("error removing duration: %v", err)
	}

	// We've updated the database, let's now also update the in-memory state
	// of the bucket instance that the order book also uses.
	s.durationBuckets.RemoveMarket(in.Duration)

	return &adminrpc.EmptyResponse{}, nil
}

func (s *adminRPCServer) ListTraderTerms(ctx context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.ListTraderTermsResponse, error) {

	resp := &adminrpc.ListTraderTermsResponse{}
	allDbTerms, err := s.store.AllTraderTerms(ctx)
	if err != nil {
		return nil, err
	}

	resp.Terms = make([]*adminrpc.TraderTerms, len(allDbTerms))
	for idx, dbTerms := range allDbTerms {
		terms := &adminrpc.TraderTerms{
			LsatId:  dbTerms.TraderID[:],
			BaseFee: -1,
			FeeRate: -1,
		}
		if dbTerms.BaseFee != nil {
			baseFee := *dbTerms.BaseFee
			terms.BaseFee = int64(baseFee)
		}
		if dbTerms.FeeRate != nil {
			feeRate := *dbTerms.FeeRate
			terms.FeeRate = int64(feeRate)
		}

		resp.Terms[idx] = terms
	}

	return resp, nil
}

func (s *adminRPCServer) StoreTraderTerms(ctx context.Context,
	terms *adminrpc.TraderTerms) (*adminrpc.EmptyResponse, error) {

	dbTerms := &traderterms.Custom{}
	copy(dbTerms.TraderID[:], terms.LsatId)

	if terms.BaseFee >= 0 {
		baseFee := btcutil.Amount(terms.BaseFee)
		dbTerms.BaseFee = &baseFee
	}
	if terms.FeeRate >= 0 {
		feeRate := btcutil.Amount(terms.FeeRate)
		dbTerms.FeeRate = &feeRate
	}

	s.mainRPCServer.activeTraders.invalidateCache(dbTerms.TraderID)

	return &adminrpc.EmptyResponse{}, s.store.PutTraderTerms(ctx, dbTerms)
}

func (s *adminRPCServer) RemoveTraderTerms(ctx context.Context,
	terms *adminrpc.TraderTerms) (*adminrpc.EmptyResponse, error) {

	var traderID lsat.TokenID
	copy(traderID[:], terms.LsatId)

	s.mainRPCServer.activeTraders.invalidateCache(traderID)

	return &adminrpc.EmptyResponse{}, s.store.DelTraderTerms(ctx, traderID)
}

func parseRPCDurationBucketState(
	rpcState auctioneerrpc.DurationBucketState) (order.DurationBucketState,
	error) {

	switch rpcState {
	case auctioneerrpc.DurationBucketState_NO_MARKET:
		return order.BucketStateNoMarket, nil

	case auctioneerrpc.DurationBucketState_MARKET_CLOSED:
		return order.BucketStateMarketClosed, nil

	case auctioneerrpc.DurationBucketState_ACCEPTING_ORDERS:
		return order.BucketStateAcceptingOrders, nil

	case auctioneerrpc.DurationBucketState_MARKET_OPEN:
		return order.BucketStateClearingMarket, nil

	default:
		return 0, fmt.Errorf("unknown duration bucket state: %v",
			rpcState)
	}
}

func (s *adminRPCServer) MoveFunds(ctx context.Context,
	req *adminrpc.MoveFundsRequest) (*adminrpc.EmptyResponse, error) {

	io := &batchtx.BatchIO{}

	for _, in := range req.Inputs {
		hash, err := chainhash.NewHash(in.Outpoint.Txid)
		if err != nil {
			return nil, err
		}

		pkScript, err := hex.DecodeString(in.PkScript)
		if err != nil {
			return nil, err
		}

		// We parse the script for this input and use it to set the
		// correct weight estimation closure. We do it here instead of
		// when we actually go to create the batch transaction, such
		// that we can be sure the input is actually supported. We only
		// support P2WKH and TaprootPubKeySpend.
		var weightEstimate func(*input.TxWeightEstimator) error

		// TODO (positiveblue): support P2WSH?
		scriptClass := txscript.GetScriptClass(pkScript)
		switch scriptClass {
		case txscript.WitnessV0PubKeyHashTy:
			weightEstimate = input.WitnessKeyHash.AddWeightEstimation

		case txscript.WitnessV1TaprootTy:
			weightEstimate = input.TaprootPubKeySpend.AddWeightEstimation

		default:
			return nil, fmt.Errorf("unsupported script type: %v",
				scriptClass)
		}

		io.Inputs = append(io.Inputs, &batchtx.RequestedInput{
			PrevOutPoint: wire.OutPoint{
				Hash:  *hash,
				Index: in.Outpoint.OutputIndex,
			},
			Value:             btcutil.Amount(in.Value),
			PkScript:          pkScript,
			AddWeightEstimate: weightEstimate,
		})
	}

	for _, out := range req.Outputs {
		addr, err := btcutil.DecodeAddress(out.Address, s.network)
		if err != nil {
			return nil, err
		}

		pkScript, err := txscript.PayToAddrScript(addr)
		if err != nil {
			return nil, err
		}

		// Similar to what we did for inputs, we parse the output
		// script to make sure it is among or supported types (P2WKH
		// and P2TR) and set the correct weight estimation closure.
		var weightEstimate func(*input.TxWeightEstimator) error

		// TODO (positiveblue): support P2WSH?
		scriptClass := txscript.GetScriptClass(pkScript)
		switch scriptClass {
		case txscript.WitnessV0PubKeyHashTy:
			weightEstimate = func(w *input.TxWeightEstimator) error {
				w.AddP2WKHOutput()
				return nil
			}

		case txscript.WitnessV1TaprootTy:
			weightEstimate = func(w *input.TxWeightEstimator) error {
				w.AddP2TROutput()
				return nil
			}

		default:
			return nil, fmt.Errorf("unsupported script type: %v",
				scriptClass)
		}

		io.Outputs = append(io.Outputs, &batchtx.RequestedOutput{
			PkScript:          pkScript,
			Value:             btcutil.Amount(out.Value),
			AddWeightEstimate: weightEstimate,
		})
	}

	// Now that we have created the request we want to include in the next
	// batch, go through and lease all outputs. If this fails for any
	// output, we'll release them again.
	releaseOutputs := func(lastIndex int) {
		for i := 0; i < lastIndex; i++ {
			in := io.Inputs[i]
			err := s.wallet.ReleaseOutput(
				ctx, s.lockID, in.PrevOutPoint,
			)
			if err != nil {
				log.Warnf("Unable to release output %v: %v",
					in.PrevOutPoint, err)
			}
		}
	}

	for i, in := range io.Inputs {
		_, err := s.wallet.LeaseOutput(
			ctx, s.lockID, in.PrevOutPoint, defaultLeaseDuration,
		)
		if err != nil {
			releaseOutputs(i)
			return nil, fmt.Errorf("unable to lease output %v: %v",
				in.PrevOutPoint, err)
		}
	}

	if err := s.auctioneer.RequestIO(io); err != nil {
		releaseOutputs(len(io.Inputs))
		return nil, err
	}

	return &adminrpc.EmptyResponse{}, nil
}

// FinancialReport returns a financial report for the specified dates.
func (s *adminRPCServer) FinancialReport(ctx context.Context,
	req *adminrpc.FinancialReportRequest) (*adminrpc.FinancialReportResponse,
	error) {

	startDate := time.Unix(req.StartTimestamp, 0)
	endDate := time.Unix(req.EndTimestamp, 0)
	if endDate.After(time.Now().UTC()) {
		endDate = time.Now().UTC()
	}

	// TODO(positieblue): make this a simple db query + set the store
	// in the accounting cfg.
	getBatches := func(context.Context) (accounting.BatchSnapshotMap,
		error) {

		allBatches, err := s.store.Batches(ctx)
		if err != nil {
			return nil, err
		}

		batches := make(accounting.BatchSnapshotMap)
		for batchID, snapshot := range allBatches {
			timestamp := snapshot.OrderBatch.CreationTimestamp

			if timestamp.Before(startDate) {
				continue
			}

			if timestamp.After(endDate) {
				continue
			}

			batches[batchID] = snapshot
		}

		return batches, nil
	}

	getPrice, err := accounting.GetPriceFunc(startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("unable to get price function: %v", err)
	}

	cfg := &accounting.Config{
		Start:                startDate,
		End:                  endDate,
		LightningClient:      s.lightningClient,
		GetBatches:           getBatches,
		GetAuctioneerBalance: s.store.GetAuctioneerBalance,
		GetPrice:             getPrice,
	}

	report, err := accounting.CreateReport(cfg)
	if err != nil {
		return nil, fmt.Errorf("unable to create report: %v", err)
	}

	batchEntries := make(
		[]*adminrpc.FinancialReportBatchEntry,
		0,
		len(report.BatchEntries),
	)

	for _, entry := range report.BatchEntries {
		batchEntries = append(
			batchEntries, marshallBatchReportEntry(entry),
		)
	}

	LSATEntries := make(
		[]*adminrpc.FinancialReportLSATEntry,
		0,
		len(report.LSATEntries),
	)
	for _, entry := range report.LSATEntries {
		LSATEntries = append(
			LSATEntries, marshallLSATReportEntry(entry),
		)
	}

	summary := marshalReportSummary(&report.Summary)
	return &adminrpc.FinancialReportResponse{
		BatchEntries: batchEntries,
		LsatEntries:  LSATEntries,
		Summary:      summary,
	}, nil
}

// Shutdown shuts down the whole server.
func (s *adminRPCServer) Shutdown(context.Context,
	*adminrpc.EmptyRequest) (*adminrpc.EmptyResponse, error) {

	// Logging a critical error should cause the logger to initiate a
	// shutdown.
	log.Critical("Shutdown requested through the admin RPC")

	return &adminrpc.EmptyResponse{}, nil
}

// SetStatus sets the current server status as it is reported to the k8s health
// and readiness endpoints.
func (s *adminRPCServer) SetStatus(_ context.Context,
	req *adminrpc.SetStatusRequest) (*adminrpc.EmptyResponse, error) {

	if err := s.statusReporter.SetStatus(req.ServerState); err != nil {
		return nil, err
	}

	return &adminrpc.EmptyResponse{}, nil
}

// SetLogLevel sets the log level of the whole server to the new value.
func (s *adminRPCServer) SetLogLevel(_ context.Context,
	req *adminrpc.SetLogLevelRequest) (*adminrpc.EmptyResponse, error) {

	log.Infof("Setting new log level to %s", req.LogLevel)
	return &adminrpc.EmptyResponse{}, build.ParseAndSetDebugLevels(
		req.LogLevel, logWriter,
	)
}

// marshallBatchReportEntry translates an accounting.Entry into its admin RPC
// counterpart.
func marshallBatchReportEntry(
	entry *accounting.BatchEntry) *adminrpc.FinancialReportBatchEntry {

	return &adminrpc.FinancialReportBatchEntry{
		BatchKey:        entry.BatchID[:],
		Timestamp:       entry.Timestamp.Unix(),
		BatchTxId:       entry.TxID,
		BatchTxFees:     uint64(entry.BatchTxFees),
		AccruedFees:     uint64(entry.AccruedFees),
		TraderChainFees: uint64(entry.TraderChainFees),
		ProfitInSats:    int64(entry.ProfitInSats),
		ProfitInUsd:     entry.ProfitInUSD.String(),
		BtcPrice:        marshallBTCPrice(entry.BTCPrice),
	}
}

// marshallLSATReportEntry translates an accounting.LSATEntry into its admin
// RPC counterpart.
func marshallLSATReportEntry(
	entry *accounting.LSATEntry) *adminrpc.FinancialReportLSATEntry {

	return &adminrpc.FinancialReportLSATEntry{
		Timestamp:    entry.Timestamp.Unix(),
		ProfitInSats: int64(entry.ProfitInSats),
		ProfitInUsd:  entry.ProfitInUSD.String(),
		BtcPrice:     marshallBTCPrice(entry.BTCPrice),
	}
}

// marshallBTCPrice translates a *fiat.Price into its admin RPC counterpart.
func marshallBTCPrice(btcPrice *fiat.Price) *adminrpc.BTCPrice {
	return &adminrpc.BTCPrice{
		Timestamp: btcPrice.Timestamp.Unix(),
		Price:     btcPrice.Price.String(),
		Currency:  btcPrice.Currency,
	}
}

// marshalReportSummary translates an accounting.Summary into its admin RPC
// counterpart.
func marshalReportSummary(
	summary *accounting.Summary) *adminrpc.FinancialReportSummary {

	return &adminrpc.FinancialReportSummary{
		CreationTimestamp:   summary.CreationTime.Unix(),
		StartTimestamp:      summary.Start.Unix(),
		EndTimestamp:        summary.End.Unix(),
		ClosingBalance:      int64(summary.ClosingBalance),
		ClosingBalanceInUsd: summary.ClosingBalanceInUSD.String(),
		LeaseBatchFees:      int64(summary.LeaseBatchFees),
		LeaseBatchFeesInUsd: summary.LeaseBatchFeesInUSD.String(),
		Lsat:                int64(summary.LSAT),
		LsatInUsd:           summary.LSATInUSD.String(),
		ChainFees:           int64(summary.ChainFees),
		ChainFeesInUsd:      summary.ChainFeesInUSD.String(),
		NetRevenue:          int64(summary.NetRevenue),
		NetRevenueInUsd:     summary.NetRevenueInUSD.String(),
	}
}

// marshallAdminAccount translates an account.Account into its admin RPC
// counterpart.
func marshallAdminAccount(acct *account.Account) (*adminrpc.Account, error) {
	rpcState, err := marshallAccountState(acct.State)
	if err != nil {
		return nil, err
	}

	rpcAcct := &adminrpc.Account{
		Value:         uint64(acct.Value),
		Expiry:        acct.Expiry,
		TraderKey:     acct.TraderKeyRaw[:],
		AuctioneerKey: acct.AuctioneerKey.PubKey.SerializeCompressed(),
		BatchKey:      acct.BatchKey.SerializeCompressed(),
		HeightHint:    acct.HeightHint,
		Outpoint:      acct.OutPoint.String(),
		UserAgent:     acct.UserAgent,
		State:         rpcState,
	}

	if acct.LatestTx != nil {
		var txBuf bytes.Buffer
		if err := acct.LatestTx.Serialize(&txBuf); err != nil {
			return nil, err
		}
		rpcAcct.LatestTx = txBuf.Bytes()
	}

	return rpcAcct, nil
}
