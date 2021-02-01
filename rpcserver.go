package subasta

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/lndclient"
	accountT "github.com/lightninglabs/pool/account"
	"github.com/lightninglabs/pool/auctioneer"
	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/pool/chaninfo"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/feebump"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/ratings"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/chanbackup"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/tor"
	"google.golang.org/grpc"
)

const (
	// initTimeout is the maximum time we allow for the etcd store to be
	// initialized.
	initTimeout = 5 * time.Second

	// getInfoTimeout is the maximum time we allow for the GetInfo call to
	// the backing lnd node.
	getInfoTimeout = 5 * time.Second

	// maxAccountsPerTrader is the upper limit of how many accounts a trader
	// can have in an active state. This limitation is needed to make sure
	// the multiplexing of multiple accounts through the same gRPC stream
	// has an upper bound.
	maxAccountsPerTrader = 50

	// maxSnapshotsPerRequest is the maximum number of batch snapshots that
	// we return in the answer for one request.
	maxSnapshotsPerRequest = 100
)

var (
	// zeroBatchID is the empty batch ID that consists of all zeros.
	zeroBatchID orderT.BatchID
)

// TraderStream is a single long-lived connection between a trader client and
// the rpcServer. One trader might subscribe to updates of many accounts through
// the same stream. These account subscriptions are tracked with this struct
// and the message channels for each subscription multiplexed and de-multiplexed
// in the rpcServer.
type TraderStream struct {
	// Lsat is the authentication token the trader used when connecting.
	// This is how we identify the trader to track reputation.
	Lsat lsat.TokenID

	// Subscriptions is the map of all accounts the trader has subscribed
	// to updates to, keyed by the account key/ID.
	Subscriptions map[[33]byte]*venue.ActiveTrader

	// authNonce is the nonce the auctioneer picks to create the challenge,
	// together with the commitment the trader sends. This is sent back to
	// the trader as step 2 of the 3-way authentication handshake.
	authNonce [32]byte

	// comms is a summary of all connection, abort and error channels that
	// are needed for the bi-directional communication between the server
	// and a trader over the same long-lived stream.
	comms *commChannels
}

// commChannels is a set of bi-directional communication channels for exactly
// one connected trader. The same channels might be passed to the batch executor
// multiple times if a trader subscribes to updates of multiple accounts.
type commChannels struct {
	newSub   chan *venue.ActiveTrader
	toTrader chan venue.ExecutionMsg
	toServer chan venue.TraderMsg

	abortOnce sync.Once
	quitConn  chan struct{}

	err chan error
}

// abort can be called to initiate a shutdown of the communication channel
// between the client and server.
func (c *commChannels) abort() {
	c.abortOnce.Do(func() {
		close(c.quitConn)
	})
}

// rpcServer is a server that implements the auction server RPC interface and
// serves client requests by delegating the work to the respective managers.
type rpcServer struct {
	grpcServer *grpc.Server

	listener net.Listener
	serveWg  sync.WaitGroup

	started uint32 // To be used atomically.
	stopped uint32 // To be used atomically.

	quit             chan struct{}
	wg               sync.WaitGroup
	subscribeTimeout time.Duration

	accountManager *account.Manager

	orderBook *order.Book

	store subastadb.Store

	batchExecutor *venue.BatchExecutor

	auctioneer *Auctioneer

	signer lndclient.SignerClient

	ratingAgency ratings.Agency

	ratingsDB ratings.NodeRatingsDatabase

	bestHeight func() uint32

	terms *terms.AuctioneerTerms

	snapshotCache    map[orderT.BatchID]*subastadb.BatchSnapshot
	snapshotCacheMtx sync.Mutex

	// connectedStreams is the list of all currently connected
	// bi-directional update streams. Each trader has exactly one stream
	// but can subscribe to updates for multiple accounts through the same
	// stream.
	connectedStreams map[lsat.TokenID]*TraderStream

	// connectedStreamsMutex is a mutex guarding access to connectedStreams.
	connectedStreamsMutex sync.Mutex
}

// newRPCServer creates a new rpcServer.
func newRPCServer(store subastadb.Store, signer lndclient.SignerClient,
	accountManager *account.Manager, bestHeight func() uint32,
	orderBook *order.Book, batchExecutor *venue.BatchExecutor,
	auctioneer *Auctioneer, terms *terms.AuctioneerTerms,
	ratingAgency ratings.Agency, ratingsDB ratings.NodeRatingsDatabase,
	listener net.Listener, serverOpts []grpc.ServerOption,
	subscribeTimeout time.Duration) *rpcServer {

	return &rpcServer{
		grpcServer:       grpc.NewServer(serverOpts...),
		listener:         listener,
		bestHeight:       bestHeight,
		signer:           signer,
		accountManager:   accountManager,
		orderBook:        orderBook,
		store:            store,
		batchExecutor:    batchExecutor,
		auctioneer:       auctioneer,
		terms:            terms,
		quit:             make(chan struct{}),
		connectedStreams: make(map[lsat.TokenID]*TraderStream),
		snapshotCache:    make(map[orderT.BatchID]*subastadb.BatchSnapshot),
		subscribeTimeout: subscribeTimeout,
		ratingAgency:     ratingAgency,
		ratingsDB:        ratingsDB,
	}
}

// Start starts the rpcServer, making it ready to accept incoming requests.
func (s *rpcServer) Start() error {
	if !atomic.CompareAndSwapUint32(&s.started, 0, 1) {
		return nil
	}

	rpcLog.Infof("Starting auction server")

	s.serveWg.Add(1)
	go func() {
		defer s.serveWg.Done()

		rpcLog.Infof("RPC server listening on %s", s.listener.Addr())
		err := s.grpcServer.Serve(s.listener)
		if err != nil && err != grpc.ErrServerStopped {
			rpcLog.Errorf("RPC server stopped with error: %v", err)
		}
	}()

	rpcLog.Infof("Auction server is now active")

	return nil
}

// Stop stops the server.
func (s *rpcServer) Stop() {
	if !atomic.CompareAndSwapUint32(&s.stopped, 0, 1) {
		return
	}

	rpcLog.Info("Stopping auction server")

	close(s.quit)
	s.wg.Wait()

	rpcLog.Info("Stopping lnd client, gRPC server and listener")
	s.grpcServer.Stop()

	s.serveWg.Wait()

	rpcLog.Info("Auction server stopped")
}

func (s *rpcServer) ReserveAccount(ctx context.Context,
	req *auctioneerrpc.ReserveAccountRequest) (*auctioneerrpc.ReserveAccountResponse,
	error) {

	// The token ID can only be zero when testing locally without Kirin (or
	// during the integration tests). In a real deployment, Kirin enforces
	// the token to be set so we don't need an explicit check here.
	tokenID := tokenIDFromContext(ctx)

	// TODO(guggero): Make sure we enforce maxAccountsPerTrader here.

	// Parse the trader key to make sure it's a valid pubkey. More cannot
	// be checked at this moment.
	traderKey, err := btcec.ParsePubKey(req.TraderKey, btcec.S256())
	if err != nil {
		return nil, err
	}

	params := &account.Parameters{
		Value:     btcutil.Amount(req.AccountValue),
		Expiry:    req.AccountExpiry,
		TraderKey: traderKey,
	}
	reservation, err := s.accountManager.ReserveAccount(
		ctx, params, tokenID, s.bestHeight(),
	)
	if err == account.ErrNoAuctioneerAccount {
		return nil, errors.New("auction not ready for account creation yet")
	}
	if err != nil {
		return nil, err
	}

	return &auctioneerrpc.ReserveAccountResponse{
		AuctioneerKey:   reservation.AuctioneerKey.PubKey.SerializeCompressed(),
		InitialBatchKey: reservation.InitialBatchKey.SerializeCompressed(),
	}, nil
}

// parseRPCAccountParams parses the relevant account parameters from a
// ServerInitAccountRequest RPC message.
func parseRPCAccountParams(
	req *auctioneerrpc.ServerInitAccountRequest) (*account.Parameters, error) {

	var txid chainhash.Hash
	copy(txid[:], req.AccountPoint.Txid)
	accountPoint := wire.OutPoint{
		Hash:  txid,
		Index: req.AccountPoint.OutputIndex,
	}

	traderKey, err := btcec.ParsePubKey(req.TraderKey, btcec.S256())
	if err != nil {
		return nil, err
	}

	return &account.Parameters{
		OutPoint:  accountPoint,
		Value:     btcutil.Amount(req.AccountValue),
		Script:    req.AccountScript,
		Expiry:    req.AccountExpiry,
		TraderKey: traderKey,
	}, nil
}

func (s *rpcServer) InitAccount(ctx context.Context,
	req *auctioneerrpc.ServerInitAccountRequest) (*auctioneerrpc.ServerInitAccountResponse, error) {

	// The token ID can only be zero when testing locally without Kirin (or
	// during the integration tests). In a real deployment, Kirin enforces
	// the token to be set so we don't need an explicit check here.
	tokenID := tokenIDFromContext(ctx)

	accountParams, err := parseRPCAccountParams(req)
	if err != nil {
		return nil, err
	}

	err = s.accountManager.InitAccount(
		ctx, tokenID, accountParams, s.bestHeight(),
	)
	if err != nil {
		return nil, err
	}

	return &auctioneerrpc.ServerInitAccountResponse{}, nil
}

func (s *rpcServer) ModifyAccount(ctx context.Context,
	req *auctioneerrpc.ServerModifyAccountRequest) (
	*auctioneerrpc.ServerModifyAccountResponse, error) {

	traderKey, err := btcec.ParsePubKey(req.TraderKey, btcec.S256())
	if err != nil {
		return nil, err
	}

	var newInputs []*wire.TxIn
	if len(req.NewInputs) > 0 {
		newInputs = make([]*wire.TxIn, 0, len(req.NewInputs))
		for _, newInput := range req.NewInputs {
			opHash, err := chainhash.NewHash(newInput.Outpoint.Txid)
			if err != nil {
				return nil, err
			}

			newInputs = append(newInputs, &wire.TxIn{
				PreviousOutPoint: wire.OutPoint{
					Hash:  *opHash,
					Index: newInput.Outpoint.OutputIndex,
				},
				SignatureScript: newInput.SigScript,
			})
		}
	}

	var newOutputs []*wire.TxOut
	if len(req.NewOutputs) > 0 {
		newOutputs = make([]*wire.TxOut, 0, len(req.NewOutputs))
		for _, newOutput := range req.NewOutputs {
			// Make sure they've provided a valid output script.
			_, err := txscript.ParsePkScript(newOutput.Script)
			if err != nil {
				return nil, err
			}

			newOutputs = append(newOutputs, &wire.TxOut{
				Value:    int64(newOutput.Value),
				PkScript: newOutput.Script,
			})
		}
	}

	var modifiers []account.Modifier
	if req.NewParams != nil {
		if req.NewParams.Value != 0 {
			value := btcutil.Amount(req.NewParams.Value)
			m := account.ValueModifier(value)
			modifiers = append(modifiers, m)
		}
		if req.NewParams.Expiry != 0 {
			m := account.ExpiryModifier(req.NewParams.Expiry)
			modifiers = append(modifiers, m)
		}
	}

	var rawTraderKey [33]byte
	copy(rawTraderKey[:], req.TraderKey)

	// Consult with the auctioneer whether an account update should be
	// allowed at the moment as it may interfere with an ongoing batch.
	if !s.auctioneer.AllowAccountUpdate(matching.NewAccountID(traderKey)) {
		return nil, errors.New("account modification not allowed " +
			"during batch execution")
	}

	// Get the value locked up in orders for this account.
	lockedValue, err := s.orderBook.LockedValue(
		ctx, rawTraderKey, s.terms.FeeSchedule(),
	)
	if err != nil {
		return nil, err
	}

	accountSig, err := s.accountManager.ModifyAccount(
		ctx, traderKey, lockedValue, newInputs, newOutputs, modifiers,
		s.bestHeight(),
	)
	if err != nil {
		return nil, err
	}

	return &auctioneerrpc.ServerModifyAccountResponse{
		AccountSig: accountSig,
	}, nil
}

// SubmitOrder parses a client's request to submit an order, validates it and
// if successful, stores it to the database and hands it over to the manager
// for further processing.
func (s *rpcServer) SubmitOrder(ctx context.Context,
	req *auctioneerrpc.ServerSubmitOrderRequest) (
	*auctioneerrpc.ServerSubmitOrderResponse, error) {

	// TODO(roasbeef): don't accept orders if auctioneer doesn't have
	// master account
	//  * make new interface for? have all operations flow thru the
	//    auctioneer?
	//  * allow order cancellations tho?

	var o order.ServerOrder
	switch requestOrder := req.Details.(type) {
	case *auctioneerrpc.ServerSubmitOrderRequest_Ask:
		a := requestOrder.Ask
		clientKit, serverKit, err := s.parseRPCOrder(
			ctx, a.Version, a.Details, true,
		)
		if err != nil {
			return nil, err
		}
		clientKit.LeaseDuration = a.LeaseDurationBlocks

		clientAsk := &orderT.Ask{
			Kit: *clientKit,
		}
		o = &order.Ask{
			Ask: *clientAsk,
			Kit: *serverKit,
		}

	case *auctioneerrpc.ServerSubmitOrderRequest_Bid:
		b := requestOrder.Bid
		clientKit, serverKit, err := s.parseRPCOrder(
			ctx, b.Version, b.Details, false,
		)
		if err != nil {
			return nil, err
		}
		clientKit.LeaseDuration = b.LeaseDurationBlocks

		nodeTier, err := unmarshallNodeTier(b.MinNodeTier)
		if err != nil {
			return nil, err
		}
		clientBid := &orderT.Bid{
			Kit:         *clientKit,
			MinNodeTier: nodeTier,
		}
		o = &order.Bid{
			Bid: *clientBid,
			Kit: *serverKit,
		}

	default:
		return nil, fmt.Errorf("invalid order request")
	}

	// TODO(roasbeef): instead have callback/notification system on order
	// book itself?
	//  * eventually needed if we want to stream the info out live

	// Formally everything seems OK, hand over the order to the manager for
	// further validation and processing.
	err := s.orderBook.PrepareOrder(
		ctx, o, s.terms.FeeSchedule(), s.bestHeight(),
	)
	return mapOrderResp(o.Nonce(), err)
}

// CancelOrder tries to remove an order from the order book and mark it as
// revoked by the user.
func (s *rpcServer) CancelOrder(ctx context.Context,
	req *auctioneerrpc.ServerCancelOrderRequest) (
	*auctioneerrpc.ServerCancelOrderResponse, error) {

	var noncePreimage lntypes.Preimage
	copy(noncePreimage[:], req.OrderNoncePreimage)
	err := s.orderBook.CancelOrderWithPreimage(ctx, noncePreimage)
	if err != nil {
		return nil, err
	}

	return &auctioneerrpc.ServerCancelOrderResponse{}, nil
}

// SubscribeBatchAuction is a streaming RPC that allows a trader to subscribe
// to updates and events around accounts and orders. This method will be called
// by the RPC server once per connection and will keep running for the entire
// length of the connection. Each method invocation represents one trader with
// multiple accounts and multiple order per account.
func (s *rpcServer) SubscribeBatchAuction(
	stream auctioneerrpc.ChannelAuctioneer_SubscribeBatchAuctionServer) error {

	// Don't let the rpcServer shut down while we have traders connected.
	s.wg.Add(1)
	defer s.wg.Done()

	// First let's make sure the trader isn't trying to open more than one
	// connection. Pinning this to the LSAT is not a 100% guarantee there
	// won't be two streams with the same accounts, but it should prevent
	// a badly implemented client from draining our TCP connections by
	// accident. The token ID can only be zero when testing locally without
	// aperture (or during the integration tests). In a real deployment,
	// aperture enforces the token to be set so we don't need an explicit
	// check here.
	traderID := tokenIDFromContext(stream.Context())
	s.connectedStreamsMutex.Lock()
	_, ok := s.connectedStreams[traderID]
	s.connectedStreamsMutex.Unlock()
	if ok {
		return fmt.Errorf("client already connected, only one stream " +
			"per trader is allowed")
	}
	rpcLog.Debugf("New trader client_id=%x connected to stream", traderID)

	// Prepare the structure that we are going to use to track the trader
	// over the duration of this stream.
	trader := &TraderStream{
		Lsat:          traderID,
		Subscriptions: make(map[[33]byte]*venue.ActiveTrader),
		comms: &commChannels{
			newSub: make(chan *venue.ActiveTrader),
			// The following two channels must be buffered with the
			// same number as the maximum number of accounts per
			// trader! The reason is, messages from the trader
			// client are de-multiplexed for the venue as it only
			// knows about the individual accounts and doesn't know
			// which belong to the same client!
			toTrader: make(
				chan venue.ExecutionMsg, maxAccountsPerTrader,
			),
			toServer: make(
				chan venue.TraderMsg, maxAccountsPerTrader,
			),
			quitConn: make(chan struct{}),
			err:      make(chan error),
		},
	}
	s.connectedStreamsMutex.Lock()
	s.connectedStreams[traderID] = trader
	s.connectedStreamsMutex.Unlock()
	initialSubscriptionTimeout := time.After(s.subscribeTimeout)

	// Start the goroutine that just accepts incoming subscription requests.
	// We'll have multiple goroutines running here so we need to flatten
	// everything to make it easier to follow. What we flatten is the
	// following hierarchy of 1:n relationships:
	//     1 trader connection -> n subscriptions (accounts)
	//     1 subscription      -> n orders with update messages
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.readIncomingStream(trader, stream)
	}()

	// Handle de-multiplexed events and messages in one loop.
	for {
		select {
		// Disconnect traders if they don't send a subscription before
		// the timeout ends.
		case <-initialSubscriptionTimeout:
			if len(trader.Subscriptions) == 0 {
				err := s.disconnectTrader(traderID)
				if err != nil {
					return fmt.Errorf("unable to "+
						"disconnect on initial "+
						"timeout: %v", err)
				}
				return fmt.Errorf("no subscription received " +
					"before timeout")
			}

		// New incoming subscription.
		case newSub := <-trader.comms.newSub:
			rpcLog.Debugf("New subscription, client_id=%x, acct=%x",
				traderID, newSub.AccountKey)
			err := s.addStreamSubscription(traderID, newSub)
			if err != nil {
				return fmt.Errorf("unable to register "+
					"subscription: %v", err)
			}

			// Now that we know everything checks out, send an
			// acknowledgement message back to the trader. This is
			// needed so we always send a response to the trader,
			// both if the account exists and if it doesn't. This is
			// useful for the recovery so the trader knows
			// immediately if they can send the request to recover
			// that account or not.
			err = stream.Send(&auctioneerrpc.ServerAuctionMessage{
				Msg: &auctioneerrpc.ServerAuctionMessage_Success{
					Success: &auctioneerrpc.SubscribeSuccess{
						TraderKey: newSub.AccountKey[:],
					},
				},
			})
			if err != nil {
				return fmt.Errorf("error sending success: %v",
					err)
			}

		// Message from the trader to the batch executor.
		case toServerMsg := <-trader.comms.toServer:
			err := s.batchExecutor.HandleTraderMsg(toServerMsg)
			if err != nil {
				return fmt.Errorf("error handlilng trader "+
					"message: %v", err)
			}

		// Message from the batch executor to the trader.
		case toTraderMsg := <-trader.comms.toTrader:
			err := s.sendToTrader(stream, toTraderMsg)
			if err != nil {
				return fmt.Errorf("unable to send message: %v",
					err)
			}

		// The trader is signaling abort or is closing the connection.
		case <-trader.comms.quitConn:
			rpcLog.Debugf("Trader client_id=%x is disconnecting",
				trader.Lsat)
			return s.disconnectTrader(traderID)

		// An error happened anywhere in the process, we need to abort
		// the connection.
		case err := <-trader.comms.err:
			// The trader sent a valid signature for an account that
			// we don't know of. This either means there is a gap in
			// the account keys on the trader side because creating
			// one or more accounts failed. Or it means the trader
			// got to the next key after the last account key during
			// recovery.
			var e *subastadb.AccountNotFoundError
			if errors.As(err, &e) {
				errCode := auctioneerrpc.SubscribeError_ACCOUNT_DOES_NOT_EXIST
				err = stream.Send(&auctioneerrpc.ServerAuctionMessage{
					Msg: &auctioneerrpc.ServerAuctionMessage_Error{
						Error: &auctioneerrpc.SubscribeError{
							Error:     err.Error(),
							ErrorCode: errCode,
							TraderKey: e.AcctKey[:],
						},
					},
				})
				if err != nil {
					return fmt.Errorf("error sending err "+
						"msg: %v", err)
				}

				// We don't punish or disconnect the trader.
				continue
			}

			// The trader sent a valid signature for an account that
			// we only have a reservation for. This is yet another
			// special case that we need to handle separately
			// because we can't create a normal account subscription
			// as there's no account yet. Instead we can just send
			// back the recovery information now as this must be a
			// recovery attempt. The trader normally would never try
			// to subscribe to an account with a reservation only.
			var e2 *auctioneer.AcctResNotCompletedError
			if errors.As(err, &e2) {
				errCode := auctioneerrpc.SubscribeError_INCOMPLETE_ACCOUNT_RESERVATION
				partialAcct := &auctioneerrpc.AuctionAccount{
					Value:         uint64(e2.Value),
					TraderKey:     e2.AcctKey[:],
					AuctioneerKey: e2.AuctioneerKey[:],
					BatchKey:      e2.InitialBatchKey[:],
					Expiry:        e2.Expiry,
					HeightHint:    e2.HeightHint,
				}
				errMsg := &auctioneerrpc.SubscribeError{
					Error:              err.Error(),
					ErrorCode:          errCode,
					TraderKey:          e2.AcctKey[:],
					AccountReservation: partialAcct,
				}
				err = stream.Send(&auctioneerrpc.ServerAuctionMessage{
					Msg: &auctioneerrpc.ServerAuctionMessage_Error{
						Error: errMsg,
					},
				})
				if err != nil {
					return fmt.Errorf("error sending err "+
						"msg: %v", err)
				}

				// We don't punish or disconnect the trader.
				continue
			}

			rpcLog.Errorf("Error in trader stream: %v", err)

			trader.comms.abort()

			err2 := s.disconnectTrader(traderID)
			if err2 != nil {
				rpcLog.Errorf("Unable to disconnect trader: %v",
					err2)
			}
			return fmt.Errorf("error reading client stream: %v",
				err)

		// The server is shutting down.
		case <-s.quit:
			errCode := auctioneerrpc.SubscribeError_SERVER_SHUTDOWN
			err := stream.Send(&auctioneerrpc.ServerAuctionMessage{
				Msg: &auctioneerrpc.ServerAuctionMessage_Error{
					Error: &auctioneerrpc.SubscribeError{
						Error:     "server shutting down",
						ErrorCode: errCode,
					},
				},
			})
			if err != nil {
				rpcLog.Errorf("Unable to send shutdown msg: %v",
					err)
			}

			trader.comms.abort()

			return fmt.Errorf("server shutting down")
		}
	}
}

func (s *rpcServer) ConnectedStreams() map[lsat.TokenID]*TraderStream {
	s.connectedStreamsMutex.Lock()
	defer s.connectedStreamsMutex.Unlock()

	return s.connectedStreams
}

// readIncomingStream reads incoming messages on a bi-directional stream and
// forwards them to the correct channels. For now, only subscription messages
// can be sent from the client to the server.
func (s *rpcServer) readIncomingStream(trader *TraderStream,
	stream auctioneerrpc.ChannelAuctioneer_SubscribeBatchAuctionServer) {

	for {
		// We only end up here after each received message. But in case
		// we're shutting down, we don't need to block on reading
		// another one.
		select {
		case <-s.quit:
			return
		default:
		}

		// The client always has to respond in time to our challenge by
		// telling us which account they're interested in. We read that
		// subscription message and register with the order book and
		// venue that the trader for that account is now online.
		msg, err := stream.Recv()
		switch {
		// The default disconnect signal from the client, if the trader
		// is shut down.
		case err == io.EOF:
			trader.comms.abort()
			return

		// Any other error we receive is treated as critical and leads
		// to a termination of the stream.
		case err != nil:
			// In case we are shutting down, the goroutine that
			// reads the comms' errors might have already exited.
			select {
			case trader.comms.err <- fmt.Errorf("error reading "+
				"client stream: %v", err):
			case <-s.quit:
			}
			return
		}

		// Convert the gRPC message into an internal message.
		s.handleIncomingMessage(msg, stream, trader)
	}
}

// handleIncomingMessage parses the incoming gRPC messages, turns them into
// native structs and forwards them to the correct channel.
func (s *rpcServer) handleIncomingMessage(rpcMsg *auctioneerrpc.ClientAuctionMessage,
	stream auctioneerrpc.ChannelAuctioneer_SubscribeBatchAuctionServer,
	trader *TraderStream) {

	comms := trader.comms
	switch msg := rpcMsg.Msg.(type) {
	// A new account commitment is the first step of the 3-way auth
	// handshake between the auctioneer and the trader.
	case *auctioneerrpc.ClientAuctionMessage_Commit:
		commit := msg.Commit

		// First check that they are using the latest version of the
		// batch execution protocol. If not, they need to update. Better
		// reject them now instead of waiting for a batch to be prepared
		// and then everybody bailing out because of a version mismatch.
		if commit.BatchVersion != uint32(orderT.CurrentBatchVersion) {
			comms.err <- orderT.ErrVersionMismatch
			return
		}

		// We don't know what's in the commit yet so we can only make
		// sure it's long enough and not zero.
		if len(commit.CommitHash) != 32 {
			comms.err <- fmt.Errorf("invalid commit hash")
			return
		}
		var commitHash [32]byte
		copy(commitHash[:], commit.CommitHash)

		// Create the random nonce that will be combined into the
		// challenge with the commitment we just received.
		_, err := rand.Read(trader.authNonce[:])
		if err != nil {
			comms.err <- fmt.Errorf("error creating nonce: %v",
				err)
			return
		}
		challenge := accountT.AuthChallenge(
			commitHash, trader.authNonce,
		)

		// Send the step 2 message with the challenge to the trader.
		// The user's sub key is just threaded through so they can
		// map this response to their subscription.
		err = stream.Send(&auctioneerrpc.ServerAuctionMessage{
			Msg: &auctioneerrpc.ServerAuctionMessage_Challenge{
				Challenge: &auctioneerrpc.ServerChallenge{
					Challenge:  challenge[:],
					CommitHash: commitHash[:],
				},
			},
		})
		if err != nil {
			comms.err <- fmt.Errorf("error sending challenge: %v",
				err)
			return
		}

	// New account subscription, we need to check the signature as part of
	// the authentication process.
	case *auctioneerrpc.ClientAuctionMessage_Subscribe:
		subscribe := msg.Subscribe

		// Parse their public key to validate the signature and later
		// retrieve it from the store.
		acctPubKey, err := btcec.ParsePubKey(
			msg.Subscribe.TraderKey, btcec.S256(),
		)
		if err != nil {
			comms.err <- fmt.Errorf("error parsing account key: %v",
				err)
			return
		}
		var acctKey [33]byte
		copy(acctKey[:], acctPubKey.SerializeCompressed())

		// Validate their nonce.
		if len(subscribe.CommitNonce) != 32 {
			comms.err <- fmt.Errorf("invalid commit nonce")
			return
		}
		var traderNonce [32]byte
		copy(traderNonce[:], subscribe.CommitNonce)

		// Verify the signed auth hash. We now have all the parts that
		// are needed to construct the auth hash from scratch. We do
		// so to make sure we actually got the correct commitment from
		// the trader.
		commitHash := accountT.CommitAccount(acctKey, traderNonce)
		challenge := accountT.AuthChallenge(
			commitHash, trader.authNonce,
		)
		authHash := accountT.AuthHash(commitHash, challenge)
		sig := msg.Subscribe.AuthSig
		sigValid, err := s.signer.VerifyMessage(
			stream.Context(), authHash[:], sig, acctKey,
		)
		if err != nil {
			comms.err <- fmt.Errorf("unable to verify auth "+
				"signature: %v", err)
			return
		}
		if !sigValid {
			comms.err <- fmt.Errorf("signature not valid for "+
				"public key %x", acctKey)
			return
		}

		// The signature is valid, the trader proved that they are in
		// possession of the trader private key. We now check if the
		// account exists on our side. First we need to determine if the
		// account ever made it out of the reservation state.
		res, _, err := s.store.HasReservationForKey(
			stream.Context(), acctPubKey,
		)
		if err == nil {
			// There is a reservation. We cannot create a full
			// subscription as we don't have a full account to do so
			// with. Send back our state of the reservation to allow
			// the trader to recover (if the TX ever made it to the
			// chain.
			comms.err <- newAcctResNotCompletedError(res)
			return
		}

		// There is no reservation, we can now fetch the account from
		// the store. If the account does not exist, the trader might be
		// trying to recover from a lost database state and is going
		// through their keys to find accounts we know.
		acct, err := s.store.Account(stream.Context(), acctPubKey, true)
		if err != nil {
			comms.err <- &subastadb.AccountNotFoundError{
				AcctKey: acctKey,
			}
			return
		}

		// Finally inform the batch executor about the new connected
		// client.
		commLine := &venue.DuplexLine{
			Send: comms.toTrader,
			Recv: comms.toServer,
		}
		venueTrader := matching.NewTraderFromAccount(acct)
		activeTrader := &venue.ActiveTrader{
			CommLine: commLine,
			Trader:   &venueTrader,
			TokenID:  trader.Lsat,
		}

		comms.newSub <- activeTrader

	// The trader accepts an order execution.
	case *auctioneerrpc.ClientAuctionMessage_Accept:
		// De-multiplex the incoming message for the venue.
		for _, subscribedTrader := range trader.Subscriptions {
			var batchID orderT.BatchID
			copy(batchID[:], msg.Accept.BatchId)
			traderMsg := &venue.TraderAcceptMsg{
				BatchID: batchID,
				Trader:  subscribedTrader,
			}
			comms.toServer <- traderMsg
		}

	// The trader rejected an order execution.
	case *auctioneerrpc.ClientAuctionMessage_Reject:
		var batchID orderT.BatchID
		copy(batchID[:], msg.Reject.BatchId)

		// De-multiplex the incoming message for the venue.
		for _, subscribedTrader := range trader.Subscriptions {
			traderMsg, err := parseRPCReject(
				msg, batchID, subscribedTrader,
			)
			if err != nil {
				comms.err <- err
				return
			}

			comms.toServer <- traderMsg
		}

	// The trader signed their account inputs.
	case *auctioneerrpc.ClientAuctionMessage_Sign:
		sigs := make(map[string]*btcec.Signature)
		for acctString, sigBytes := range msg.Sign.AccountSigs {
			sig, err := btcec.ParseDERSignature(sigBytes, btcec.S256())
			if err != nil {
				comms.err <- fmt.Errorf("unable to parse "+
					"account sig: %v", err)
				return
			}

			sigs[acctString] = sig
		}

		chanInfos, err := parseRPCChannelInfo(msg.Sign.ChannelInfos)
		if err != nil {
			comms.err <- err
			return
		}

		// De-multiplex the incoming message for the venue.
		for _, subscribedTrader := range trader.Subscriptions {
			// If we don't have a signature for this particular
			// trader, we can't blindly de-multi-plex this
			// particular message type to all accounts of the
			// connected daemon, some of them might not be involved
			// in the batch in question. Otherwise the auctioneer
			// will try to extract the signature for an account that
			// was not signed with.
			key := hex.EncodeToString(subscribedTrader.AccountKey[:])
			_, ok := sigs[key]
			if !ok {
				continue
			}

			traderMsg := &venue.TraderSignMsg{
				BatchID:      msg.Sign.BatchId,
				Trader:       subscribedTrader,
				Sigs:         sigs,
				ChannelInfos: chanInfos,
			}
			comms.toServer <- traderMsg
		}

	// The trader wants to recover their lost account. We'll only do this
	// for accounts that are already subscribed so we can be sure it exists
	// on our side.
	case *auctioneerrpc.ClientAuctionMessage_Recover:
		var traderKey [33]byte
		copy(traderKey[:], msg.Recover.TraderKey)
		_, ok := trader.Subscriptions[traderKey]
		if !ok {
			comms.err <- fmt.Errorf("account %x not subscribed",
				traderKey)
			return
		}

		// Send the account info to the trader and cancel all open
		// orders of that account in the process.
		err := s.sendAccountRecovery(traderKey, stream)
		if err != nil {
			comms.err <- fmt.Errorf("could not send recovery: %v",
				err)
			return
		}

	default:
		comms.err <- fmt.Errorf("unknown trader message: %v", msg)
		return
	}
}

// sendAccountRecovery fetches an account from the database and sends all
// information the auctioneer has to the trader. All open/pending accounts of
// that account will be canceled as they cannot be recovered.
func (s *rpcServer) sendAccountRecovery(traderKey [33]byte,
	stream auctioneerrpc.ChannelAuctioneer_SubscribeBatchAuctionServer) error {

	acctPubkey, err := btcec.ParsePubKey(traderKey[:], btcec.S256())
	if err != nil {
		return fmt.Errorf("could not parse account key: %v", err)
	}

	// Load the account from the store. We want to send the latest state of
	// the account to the trader so we include any diff that's been applied
	// to it in case a batch cleared recently but hasn't finalized yet.
	acct, err := s.store.Account(stream.Context(), acctPubkey, true)
	if err != nil {
		return fmt.Errorf("could not load account: %v", err)
	}

	// Cancel all open/pending orders associated with the recovered account
	// as the trader won't be able to recover those. The order book will
	// inform the venue to remove the orders from consideration if they're
	// currently being processed in a batch.
	activeOrders, err := s.store.GetOrders(stream.Context())
	if err != nil {
		return fmt.Errorf("error reading orders: %v", err)
	}
	for _, o := range activeOrders {
		if o.Details().AcctKey == acct.TraderKeyRaw {
			err = s.orderBook.CancelOrder(
				stream.Context(), o.Nonce(),
			)
			if err != nil {
				return fmt.Errorf("error canceling order %v: %v",
					o.Nonce(), err)
			}
		}
	}

	// Now that we've updated all orders we can send the recovery
	// information to the trader. If there's an error on our side here,
	// after we've updated the orders it doesn't really matter because these
	// orders will never become active anyway.
	rpcAcct, err := marshallServerAccount(acct)
	if err != nil {
		return fmt.Errorf("error marshalling account: %v", err)
	}
	err = stream.Send(&auctioneerrpc.ServerAuctionMessage{
		Msg: &auctioneerrpc.ServerAuctionMessage_Account{
			Account: rpcAcct,
		},
	})
	if err != nil {
		return fmt.Errorf("error sending recovery: %v", err)
	}

	return nil
}

// sendToTrader converts an internal execution message to the gRPC format and
// sends it out on the stream to the trader.
func (s *rpcServer) sendToTrader(
	stream auctioneerrpc.ChannelAuctioneer_SubscribeBatchAuctionServer,
	msg venue.ExecutionMsg) error {

	switch m := msg.(type) {
	case *venue.PrepareMsg:
		prepareMsg, err := marshallPrepareMsg(m)
		if err != nil {
			return fmt.Errorf("unable to marshall prepare msg: %v",
				err)
		}
		return stream.Send(prepareMsg)

	case *venue.SignBeginMsg:
		return stream.Send(&auctioneerrpc.ServerAuctionMessage{
			Msg: &auctioneerrpc.ServerAuctionMessage_Sign{
				Sign: &auctioneerrpc.OrderMatchSignBegin{
					BatchId: m.BatchID[:],
				},
			},
		})

	case *venue.FinalizeMsg:
		return stream.Send(&auctioneerrpc.ServerAuctionMessage{
			Msg: &auctioneerrpc.ServerAuctionMessage_Finalize{
				Finalize: &auctioneerrpc.OrderMatchFinalize{
					BatchId:    m.BatchID[:],
					BatchTxid:  m.BatchTxID[:],
					HeightHint: s.bestHeight() - 1,
				},
			},
		})

	default:
		return fmt.Errorf("unknown message type: %v", msg)
	}
}

// marshallPrepareMsg translates the venue's prepare message struct into the
// RPC representation.
func marshallPrepareMsg(m *venue.PrepareMsg) (*auctioneerrpc.ServerAuctionMessage,
	error) {

	feeSchedule, ok := m.ExecutionFee.(*terms.LinearFeeSchedule)
	if !ok {
		return nil, fmt.Errorf("FeeSchedule w/o fee rate used: %T",
			m.ExecutionFee)
	}

	// Orders and prices are grouped by the distinct lease duration markets.
	markets := make(map[uint32]*auctioneerrpc.MatchedMarket)

	// Each order the user submitted may be matched to one or more
	// corresponding orders, so we'll map the in-memory representation we
	// use to the proto representation that we need to send to the client.
	for duration, subBatches := range m.MatchedOrders {
		matchedOrders := make(map[string]*auctioneerrpc.MatchedOrder)
		for traderOrderNonce, orderMatches := range subBatches {
			rpcLog.Debugf("Order(%x) matched w/ %v orders",
				traderOrderNonce[:], len(m.MatchedOrders))

			nonceStr := hex.EncodeToString(traderOrderNonce[:])
			matchedOrders[nonceStr] = &auctioneerrpc.MatchedOrder{}
			mo := matchedOrders[nonceStr]

			// As we support partial patches, this trader nonce
			// might be matched with a set of other orders, so we'll
			// unroll this here now.
			for _, o := range orderMatches {
				// Find out if the recipient of the message is
				// the asker or bidder. Traders with the same
				// token can't be matched so we know that if the
				// asker's account is in the list of charged
				// accounts, the trader is the asker.
				isAsk := false
				for _, acct := range m.ChargedAccounts {
					acctKey := acct.StartingState.AccountKey
					if o.Asker.AccountKey == acctKey {
						isAsk = true
						break
					}
				}

				unitsFilled := o.Details.Quote.UnitsMatched

				// If the client had their bid matched, then
				// we'll send over the ask information and the
				// other way around if it's a bid.
				ask, bid := o.Details.Ask, o.Details.Bid
				if !isAsk {
					mo.MatchedAsks = append(
						mo.MatchedAsks,
						marshallMatchedAsk(
							ask, unitsFilled,
						),
					)
				} else {
					mo.MatchedBids = append(
						mo.MatchedBids,
						marshallMatchedBid(
							bid, unitsFilled,
						),
					)
				}
			}
		}

		markets[duration] = &auctioneerrpc.MatchedMarket{
			MatchedOrders:     matchedOrders,
			ClearingPriceRate: uint32(m.ClearingPrices[duration]),
		}
	}

	// Next, for each account that the user had in this batch, we'll
	// generate a similar RPC account diff so they can verify their portion
	// of the batch.
	accountDiffs := make([]*auctioneerrpc.AccountDiff, len(m.ChargedAccounts))
	for idx, acctDiff := range m.ChargedAccounts {
		var err error
		accountDiffs[idx], err = marshallAccountDiff(
			acctDiff, m.AccountOutPoints[idx],
		)
		if err != nil {
			return nil, err
		}
	}

	// To accommodate any legacy node participating in a batch, we need to
	// also send the orders in the deprecated fields. This assumes the
	// trader was matched with a 2016 block duration order, otherwise
	// something would be quite wrong.
	var (
		legacyMatchedOrders map[string]*auctioneerrpc.MatchedOrder
		legacyClearingPrice uint32
	)
	legacyMarket, ok := markets[orderT.LegacyLeaseDurationBucket]
	if ok {
		legacyMatchedOrders = legacyMarket.MatchedOrders
		legacyClearingPrice = legacyMarket.ClearingPriceRate
	}

	// Last, group the matched orders by their lease duration markets.
	return &auctioneerrpc.ServerAuctionMessage{
		Msg: &auctioneerrpc.ServerAuctionMessage_Prepare{
			Prepare: &auctioneerrpc.OrderMatchPrepare{
				MatchedOrders:     legacyMatchedOrders,
				ClearingPriceRate: legacyClearingPrice,
				ChargedAccounts:   accountDiffs,
				ExecutionFee: &auctioneerrpc.ExecutionFee{
					BaseFee: uint64(m.ExecutionFee.BaseFee()),
					FeeRate: uint64(feeSchedule.FeeRate()),
				},
				BatchTransaction: m.BatchTx,
				FeeRateSatPerKw:  uint64(m.FeeRate),
				BatchId:          m.BatchID[:],
				BatchVersion:     m.BatchVersion,
				MatchedMarkets:   markets,
			},
		},
	}, nil
}

// addStreamSubscription adds an account subscription to the stream that is
// already established for a trader. The subscriptions are de-duplicated and new
// subscriptions are registered with the batch executor.
func (s *rpcServer) addStreamSubscription(traderID lsat.TokenID,
	newSub *venue.ActiveTrader) error {

	s.connectedStreamsMutex.Lock()
	defer s.connectedStreamsMutex.Unlock()

	trader, ok := s.connectedStreams[traderID]
	if !ok {
		return fmt.Errorf("stream for trader %v not found", traderID)
	}

	// Make sure we don't add the same account twice, even though the client
	// might send us duplicate subscriptions.
	if _, ok := trader.Subscriptions[newSub.AccountKey]; ok {
		return nil
	}

	if len(trader.Subscriptions) == maxAccountsPerTrader {
		return fmt.Errorf("maximum number of %d accounts subscribed",
			maxAccountsPerTrader)
	}

	monitoring.ObserveNewConnection(newSub.AccountKey)

	// There's no subscription for that account yet, notify our batch
	// executor that the trader for a certain account is now connected.
	trader.Subscriptions[newSub.AccountKey] = newSub
	err := s.batchExecutor.RegisterTrader(newSub)
	if err != nil {
		return fmt.Errorf("error registering trader at venue: %v", err)
	}
	return nil
}

// disconnectTrader removes a trading client stream connection and unregisters
// all account subscriptions from the batch executor.
func (s *rpcServer) disconnectTrader(traderID lsat.TokenID) error {
	s.connectedStreamsMutex.Lock()
	defer s.connectedStreamsMutex.Unlock()

	trader, ok := s.connectedStreams[traderID]
	if !ok {
		return fmt.Errorf("stream for trader %v not found", traderID)
	}

	// Make a copy of the subscriptions before removing the trader stream
	// so we can still unsubscribe them but are sure the stream is removed
	// even if unregistering results in an error.
	subscriptions := trader.Subscriptions
	delete(s.connectedStreams, traderID)

	// TODO(rooasbeef): notify some other component that the
	// client is no longer there?
	for acctKey, trader := range subscriptions {
		monitoring.ObserveFailedConnection(acctKey)

		err := s.batchExecutor.UnregisterTrader(trader)
		if err != nil {
			return fmt.Errorf("error unregistering"+
				"trader at venue: %v", err)
		}
	}
	return nil
}

// OrderState returns the of an order as it is currently known to the order
// store.
func (s *rpcServer) OrderState(ctx context.Context,
	req *auctioneerrpc.ServerOrderStateRequest) (*auctioneerrpc.ServerOrderStateResponse,
	error) {

	var nonce orderT.Nonce
	copy(nonce[:], req.OrderNonce)

	// The state of an order should be reflected in the database so we don't
	// need to ask the manager about it.
	o, err := s.store.GetOrder(ctx, nonce)
	if err != nil {
		return nil, err
	}
	return &auctioneerrpc.ServerOrderStateResponse{
		State:            auctioneerrpc.OrderState(o.Details().State),
		UnitsUnfulfilled: uint32(o.Details().UnitsUnfulfilled),
	}, nil
}

// Terms returns the current dynamic terms like max account size, max order
// duration in blocks and the auction fee schedule.
func (s *rpcServer) Terms(ctx context.Context, _ *auctioneerrpc.TermsRequest) (
	*auctioneerrpc.TermsResponse, error) {

	nextBatchFeeRate, err := s.auctioneer.cfg.Wallet.EstimateFee(
		ctx, s.auctioneer.cfg.ConfTarget,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to estimate fee rate for next "+
			"batch: %v", err)
	}
	nextBatchClear := time.Now().Add(s.auctioneer.cfg.BatchTicker.NextTickIn())

	resp := &auctioneerrpc.TermsResponse{
		MaxAccountValue: uint64(s.terms.MaxAccountValue),
		// The max order duration is now deprecated, but old clients
		// will still use it to validate their orders so we need to set
		// it to a very high value. We'll make sure we don't accept
		// orders outside of the duration buckets in later commits.
		MaxOrderDurationBlocks: 365 * 144,
		ExecutionFee: &auctioneerrpc.ExecutionFee{
			BaseFee: uint64(s.terms.OrderExecBaseFee),
			FeeRate: uint64(s.terms.OrderExecFeeRate),
		},
		LeaseDurations:           make(map[uint32]bool),
		NextBatchConfTarget:      uint32(s.auctioneer.cfg.ConfTarget),
		NextBatchFeeRateSatPerKw: uint64(nextBatchFeeRate),
		NextBatchClearTimestamp:  uint64(nextBatchClear.Unix()),
		LeaseDurationBuckets:     make(map[uint32]auctioneerrpc.DurationBucketState),
	}

	durationBuckets := s.orderBook.DurationBuckets()
	err = durationBuckets.IterBuckets(
		func(d uint32, s order.DurationBucketState) error {
			marketOpen := (s != order.BucketStateMarketClosed &&
				s != order.BucketStateNoMarket)

			rpcState, err := marshallDurationBucketState(s)
			if err != nil {
				return err
			}

			resp.LeaseDurations[d] = marketOpen // nolint:staticcheck
			resp.LeaseDurationBuckets[d] = rpcState

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// RelevantBatchSnapshot returns a slimmed-down snapshot of the requested batch
// only pertaining to the requested accounts.
func (s *rpcServer) RelevantBatchSnapshot(ctx context.Context,
	req *auctioneerrpc.RelevantBatchRequest) (*auctioneerrpc.RelevantBatch, error) {

	// We'll start by retrieving the snapshot of the requested batch.
	batchKey, err := btcec.ParsePubKey(req.Id, btcec.S256())
	if err != nil {
		return nil, err
	}

	batchSnapshot, err := s.lookupSnapshot(ctx, batchKey)
	if err != nil {
		return nil, err
	}
	batch := batchSnapshot.OrderBatch
	batchTx := batchSnapshot.BatchTx

	var buf bytes.Buffer
	err = batchTx.Serialize(&buf)
	if err != nil {
		return nil, err
	}

	resp := &auctioneerrpc.RelevantBatch{
		// TODO(wilmer): Set remaining fields when available.
		Version:             uint32(batch.Version),
		Id:                  batchKey.SerializeCompressed(),
		ExecutionFee:        nil,
		Transaction:         buf.Bytes(),
		FeeRateSatPerKw:     0,
		CreationTimestampNs: uint64(batch.CreationTimestamp.UnixNano()),
	}

	// With the batch obtained, we'll filter it for the requested accounts.
	// If there weren't any, we'll just return the batch as is.
	if len(req.Accounts) == 0 {
		return resp, nil
	}

	// This consists of providing the trader with diffs for all of the
	// requested accounts that participated in the batch, and the orders
	// matched that resulted in these diffs.
	accounts := make(map[matching.AccountID]struct{})
	resp.ChargedAccounts = make([]*auctioneerrpc.AccountDiff, 0, len(req.Accounts))
	for _, account := range req.Accounts {
		var accountID matching.AccountID
		copy(accountID[:], account)

		diff, ok := batch.FeeReport.AccountDiffs[accountID]
		if !ok {
			continue
		}

		outputIndex, ok := poolscript.LocateOutputScript(
			batchTx, diff.RecreatedOutput.PkScript,
		)
		if !ok {
			return nil, fmt.Errorf("unable to find output for trader")
		}
		accountOutPoint := wire.OutPoint{
			Hash:  batchTx.TxHash(),
			Index: outputIndex,
		}

		accountDiff, err := marshallAccountDiff(diff, accountOutPoint)
		if err != nil {
			return nil, err
		}

		accounts[accountID] = struct{}{}
		resp.ChargedAccounts = append(resp.ChargedAccounts, accountDiff)
	}

	resp.MatchedMarkets = make(map[uint32]*auctioneerrpc.MatchedMarket)
	for duration := range batch.SubBatches {
		resp.MatchedMarkets[duration] = &auctioneerrpc.MatchedMarket{
			MatchedOrders:     make(map[string]*auctioneerrpc.MatchedOrder),
			ClearingPriceRate: uint32(batch.ClearingPrices[duration]),
		}
	}

	// An order can be fulfilled by multiple orders of the opposing type, so
	// make sure we take that into notice.
	for duration, subBatch := range batch.SubBatches {
		matchedOrders := resp.MatchedMarkets[duration].MatchedOrders
		for _, o := range subBatch {
			if _, ok := accounts[o.Asker.AccountKey]; ok {
				nonce := o.Details.Ask.Nonce().String()
				matchedBid := marshallMatchedBid(
					o.Details.Bid,
					o.Details.Quote.UnitsMatched,
				)
				resp.MatchedOrders[nonce].MatchedBids = append( // nolint:staticcheck
					resp.MatchedOrders[nonce].MatchedBids, matchedBid, // nolint:staticcheck
				)
				matchedOrders[nonce].MatchedBids = append(
					matchedOrders[nonce].MatchedBids,
					matchedBid,
				)
				continue
			}

			if _, ok := accounts[o.Bidder.AccountKey]; ok {
				nonce := o.Details.Bid.Nonce().String()
				matchedAsk := marshallMatchedAsk(
					o.Details.Ask,
					o.Details.Quote.UnitsMatched,
				)
				resp.MatchedOrders[nonce].MatchedAsks = append( // nolint:staticcheck
					resp.MatchedOrders[nonce].MatchedAsks, matchedAsk, // nolint:staticcheck
				)
				matchedOrders[nonce].MatchedAsks = append(
					matchedOrders[nonce].MatchedAsks,
					matchedAsk,
				)
			}
		}
	}

	return resp, nil
}

// parseRPCOrder parses the incoming raw RPC order into the go native data
// types used in the order struct.
func (s *rpcServer) parseRPCOrder(ctx context.Context, version uint32,
	details *auctioneerrpc.ServerOrder,
	orderIsAsk bool) (*orderT.Kit, *order.Kit, error) {

	// Parse the RPC fields into the common client struct.
	clientKit, nodeKey, addrs, multiSigKey, err := parseRPCServerOrder(
		version, details, orderIsAsk,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse server order: %v",
			err)
	}

	// Make sure the referenced account exists.
	acctKey, err := btcec.ParsePubKey(clientKit.AcctKey[:], btcec.S256())
	if err != nil {
		return nil, nil, err
	}
	_, err = s.store.Account(ctx, acctKey, true)
	if err != nil {
		return nil, nil, fmt.Errorf("account not found: %v", err)
	}

	// Parse the rest of the parameters.
	serverKit := &order.Kit{}
	serverKit.Sig, err = lnwire.NewSigFromRawSignature(details.OrderSig)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse order signature: "+
			"%v", err)
	}
	copy(serverKit.NodeKey[:], nodeKey[:])
	serverKit.NodeAddrs = addrs
	copy(serverKit.MultiSigKey[:], multiSigKey[:])
	serverKit.ChanType = order.ChanType(details.ChanType)

	return clientKit, serverKit, nil
}

// mapOrderResp maps the error returned from the order manager into the correct
// RPC return type.
func mapOrderResp(orderNonce orderT.Nonce, err error) (
	*auctioneerrpc.ServerSubmitOrderResponse, error) {

	switch err {
	case nil:
		return &auctioneerrpc.ServerSubmitOrderResponse{
			Details: &auctioneerrpc.ServerSubmitOrderResponse_Accepted{
				Accepted: true,
			},
		}, nil

	case order.ErrInvalidAmt:
		return &auctioneerrpc.ServerSubmitOrderResponse{
			Details: &auctioneerrpc.ServerSubmitOrderResponse_InvalidOrder{
				InvalidOrder: &auctioneerrpc.InvalidOrder{
					OrderNonce: orderNonce[:],
					FailReason: auctioneerrpc.InvalidOrder_INVALID_AMT,
					FailString: err.Error(),
				},
			},
		}, nil

	default:
		return nil, err
	}
}

// tokenIDFromContext tries to extract the LSAT from the given context. If no
// token is found, the zero token is returned.
func tokenIDFromContext(ctx context.Context) lsat.TokenID {
	var zeroToken lsat.TokenID
	tokenValue := lsat.FromContext(ctx, lsat.KeyTokenID)
	if token, ok := tokenValue.(lsat.TokenID); ok {
		return token
	}
	return zeroToken
}

// marshallAccountDiff translates a matching.AccountDiff to its RPC counterpart.
func marshallAccountDiff(diff matching.AccountDiff,
	acctOutPoint wire.OutPoint) (*auctioneerrpc.AccountDiff, error) {

	// TODO: Need to extend account.OnChainState with DustExtendedOffChain
	// and DustAddedToFees.
	var (
		endingState auctioneerrpc.AccountDiff_AccountState
		opIdx       int32
	)
	switch state := account.EndingState(diff.EndingBalance); {
	case state == account.OnChainStateRecreated:
		endingState = auctioneerrpc.AccountDiff_OUTPUT_RECREATED
		opIdx = int32(acctOutPoint.Index)
	case state == account.OnChainStateFullySpent:
		endingState = auctioneerrpc.AccountDiff_OUTPUT_FULLY_SPENT
		opIdx = -1
	default:
		return nil, fmt.Errorf("unhandled state %v", state)
	}

	return &auctioneerrpc.AccountDiff{
		EndingBalance: uint64(diff.EndingBalance),
		EndingState:   endingState,
		OutpointIndex: opIdx,
		TraderKey:     diff.StartingState.AccountKey[:],
	}, nil
}

// marshallServerAccount translates an account.Account into its RPC counterpart.
func marshallServerAccount(acct *account.Account) (*auctioneerrpc.AuctionAccount, error) {
	rpcAcct := &auctioneerrpc.AuctionAccount{
		Value:         uint64(acct.Value),
		Expiry:        acct.Expiry,
		TraderKey:     acct.TraderKeyRaw[:],
		AuctioneerKey: acct.AuctioneerKey.PubKey.SerializeCompressed(),
		BatchKey:      acct.BatchKey.SerializeCompressed(),
		HeightHint:    acct.HeightHint,
		Outpoint: &auctioneerrpc.OutPoint{
			Txid:        acct.OutPoint.Hash[:],
			OutputIndex: acct.OutPoint.Index,
		},
	}

	switch acct.State {
	case account.StatePendingOpen:
		rpcAcct.State = auctioneerrpc.AuctionAccountState_STATE_PENDING_OPEN

	case account.StateOpen:
		rpcAcct.State = auctioneerrpc.AuctionAccountState_STATE_OPEN

	case account.StateExpired:
		rpcAcct.State = auctioneerrpc.AuctionAccountState_STATE_EXPIRED

	case account.StateClosed:
		rpcAcct.State = auctioneerrpc.AuctionAccountState_STATE_CLOSED

	case account.StatePendingUpdate:
		rpcAcct.State = auctioneerrpc.AuctionAccountState_STATE_PENDING_UPDATE

	case account.StatePendingBatch:
		rpcAcct.State = auctioneerrpc.AuctionAccountState_STATE_PENDING_BATCH

	default:
		return nil, fmt.Errorf("unknown account state")
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

// marshallMatchedAsk translates an order.Ask to its RPC counterpart.
func marshallMatchedAsk(ask *order.Ask,
	unitsFilled orderT.SupplyUnit) *auctioneerrpc.MatchedAsk {

	return &auctioneerrpc.MatchedAsk{
		Ask: &auctioneerrpc.ServerAsk{
			Details:             marshallServerOrder(ask),
			LeaseDurationBlocks: ask.LeaseDuration(),
			Version:             uint32(ask.Version),
		},
		UnitsFilled: uint32(unitsFilled),
	}
}

// marshallMatchedBid translates an order.Bid to its RPC counterpart.
func marshallMatchedBid(bid *order.Bid,
	unitsFilled orderT.SupplyUnit) *auctioneerrpc.MatchedBid {

	return &auctioneerrpc.MatchedBid{
		Bid: &auctioneerrpc.ServerBid{
			Details:             marshallServerOrder(bid),
			LeaseDurationBlocks: bid.LeaseDuration(),
			Version:             uint32(bid.Version),
		},
		UnitsFilled: uint32(unitsFilled),
	}
}

// marshallServerOrder translates an order.ServerOrder to its RPC counterpart.
func marshallServerOrder(order order.ServerOrder) *auctioneerrpc.ServerOrder {
	nonce := order.Nonce()

	return &auctioneerrpc.ServerOrder{
		TraderKey:   order.Details().AcctKey[:],
		RateFixed:   order.Details().FixedRate,
		Amt:         uint64(order.Details().Amt),
		OrderNonce:  nonce[:],
		OrderSig:    order.ServerDetails().Sig.ToSignatureBytes(),
		MultiSigKey: order.ServerDetails().MultiSigKey[:],
		NodePub:     order.ServerDetails().NodeKey[:],
		NodeAddr:    marshallNodeAddrs(order.ServerDetails().NodeAddrs),
		// TODO: ChanType should be an enum in RPC?
		ChanType:                uint32(order.ServerDetails().ChanType),
		MaxBatchFeeRateSatPerKw: uint64(order.Details().MaxBatchFeeRate),
		MinChanAmt:              uint64(order.Details().MinUnitsMatch.ToSatoshis()),
	}
}

// marshallNodeAddrs tranlates a []net.Addr to its RPC counterpart.
func marshallNodeAddrs(addrs []net.Addr) []*auctioneerrpc.NodeAddress {
	res := make([]*auctioneerrpc.NodeAddress, 0, len(addrs))
	for _, addr := range addrs {
		res = append(res, &auctioneerrpc.NodeAddress{
			Network: addr.Network(),
			Addr:    addr.String(),
		})
	}
	return res
}

// parseRPCServerOrder parses the incoming raw RPC server order into the go
// native data types used in the order struct.
func parseRPCServerOrder(version uint32, details *auctioneerrpc.ServerOrder,
	orderIsAsk bool) (*orderT.Kit, [33]byte, []net.Addr, [33]byte, error) {

	var (
		nonce       orderT.Nonce
		nodeKey     [33]byte
		nodeAddrs   = make([]net.Addr, 0, len(details.NodeAddr))
		multiSigKey [33]byte
	)

	copy(nonce[:], details.OrderNonce)
	kit := orderT.NewKit(nonce)
	kit.Version = orderT.Version(version)
	kit.FixedRate = details.RateFixed
	kit.Amt = btcutil.Amount(details.Amt)
	kit.Units = orderT.NewSupplyFromSats(kit.Amt)
	kit.UnitsUnfulfilled = kit.Units
	kit.MaxBatchFeeRate = chainfee.SatPerKWeight(
		details.MaxBatchFeeRateSatPerKw,
	)
	kit.MinUnitsMatch = orderT.NewSupplyFromSats(
		btcutil.Amount(details.MinChanAmt),
	)

	// The trader must supply a nonce.
	if nonce == orderT.ZeroNonce {
		return nil, nodeKey, nodeAddrs, multiSigKey,
			fmt.Errorf("invalid nonce")
	}

	copy(kit.AcctKey[:], details.TraderKey)

	nodePubKey, err := btcec.ParsePubKey(details.NodePub, btcec.S256())
	if err != nil {
		return nil, nodeKey, nodeAddrs, multiSigKey,
			fmt.Errorf("unable to parse node pub key: %v",
				err)
	}
	copy(nodeKey[:], nodePubKey.SerializeCompressed())

	// We expect nodes that submit ask orders to have an advertised address
	// so they can accept inbound connections. For bids, it's ok if they
	// don't have an addr as they'll be connecting out to the maker.
	if len(details.NodeAddr) == 0 && orderIsAsk {
		return nil, nodeKey, nodeAddrs, multiSigKey,
			fmt.Errorf("invalid node addresses")
	}
	for _, rpcAddr := range details.NodeAddr {
		// Obtain the host to determine if this is a Tor address.
		host, _, err := net.SplitHostPort(rpcAddr.Addr)
		if err != nil {
			host = rpcAddr.Addr
		}

		var addr net.Addr
		switch {
		case tor.IsOnionHost(host):
			addr, err = parseOnionAddr(rpcAddr.Addr)
			if err != nil {
				return nil, nodeKey, nodeAddrs, multiSigKey,
					fmt.Errorf("unable to parse node "+
						"addr: %v", err)
			}

		default:
			addr, err = net.ResolveTCPAddr(
				rpcAddr.Network, rpcAddr.Addr,
			)
			if err != nil {
				return nil, nodeKey, nodeAddrs, multiSigKey,
					fmt.Errorf("unable to parse node "+
						"addr: %v", err)
			}
		}

		nodeAddrs = append(nodeAddrs, addr)
	}

	multiSigPubkey, err := btcec.ParsePubKey(
		details.MultiSigKey, btcec.S256(),
	)
	if err != nil {
		return nil, nodeKey, nodeAddrs, multiSigKey,
			fmt.Errorf("unable to parse multi sig pub key: %v", err)
	}
	copy(multiSigKey[:], multiSigPubkey.SerializeCompressed())

	return kit, nodeKey, nodeAddrs, multiSigKey, nil
}

// parseRPCChannelInfo returns a map of ChannelInfo indexed by their channel
// outpoint from its RPC representation.
func parseRPCChannelInfo(rpcChanInfos map[string]*auctioneerrpc.ChannelInfo) (
	map[wire.OutPoint]*chaninfo.ChannelInfo, error) {

	chanInfos := make(map[wire.OutPoint]*chaninfo.ChannelInfo)
	for chanPointStr, rpcChanInfo := range rpcChanInfos {
		// The channel outpoint is formatted as a string, parse it.
		parts := strings.Split(chanPointStr, ":")
		if len(parts) != 2 {
			return nil, errors.New("expected channel outpoint of " +
				"form txid:idx")
		}
		hash, err := chainhash.NewHashFromStr(parts[0])
		if err != nil {
			return nil, err
		}
		idx, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid output index: %v", err)
		}
		chanPoint := wire.OutPoint{Hash: *hash, Index: uint32(idx)}

		// Determine the appropriate channel type.
		var version chanbackup.SingleBackupVersion
		switch rpcChanInfo.Type {
		case auctioneerrpc.ChannelType_TWEAKLESS:
			version = chanbackup.TweaklessCommitVersion
		case auctioneerrpc.ChannelType_ANCHORS:
			version = chanbackup.AnchorsCommitVersion
		default:
			return nil, fmt.Errorf("unhandled channel type %v",
				rpcChanInfo.Type)
		}

		// Parse all of the included keys.
		localNodeKey, err := btcec.ParsePubKey(
			rpcChanInfo.LocalNodeKey, btcec.S256(),
		)
		if err != nil {
			return nil, fmt.Errorf("invalid local node key: %v",
				err)
		}
		remoteNodeKey, err := btcec.ParsePubKey(
			rpcChanInfo.RemoteNodeKey, btcec.S256(),
		)
		if err != nil {
			return nil, fmt.Errorf("invalid remote node key: %v",
				err)
		}

		localPaymentBasePoint, err := btcec.ParsePubKey(
			rpcChanInfo.LocalPaymentBasePoint, btcec.S256(),
		)
		if err != nil {
			return nil, fmt.Errorf("invalid local payment base "+
				"point: %v", err)
		}
		remotePaymentBasePoint, err := btcec.ParsePubKey(
			rpcChanInfo.RemotePaymentBasePoint, btcec.S256(),
		)
		if err != nil {
			return nil, fmt.Errorf("invalid remote payment base "+
				"point: %v", err)
		}

		chanInfos[chanPoint] = &chaninfo.ChannelInfo{
			Version:                version,
			LocalNodeKey:           localNodeKey,
			RemoteNodeKey:          remoteNodeKey,
			LocalPaymentBasePoint:  localPaymentBasePoint,
			RemotePaymentBasePoint: remotePaymentBasePoint,
		}
	}

	return chanInfos, nil
}

func parseRPCReject(msg *auctioneerrpc.ClientAuctionMessage_Reject,
	batchID orderT.BatchID, trader *venue.ActiveTrader) (venue.TraderMsg,
	error) {

	// Handle partial reject differently, needs to be a specific message.
	switch msg.Reject.ReasonCode {
	// Only some of the orders are rejected.
	case auctioneerrpc.OrderMatchReject_PARTIAL_REJECT:
		orders := make(map[orderT.Nonce]*venue.Reject)
		for nonceStr, reason := range msg.Reject.RejectedOrders {
			// Parse the nonce of the rejected order first.
			nonceBytes, err := hex.DecodeString(nonceStr)
			if err != nil {
				return nil, fmt.Errorf("unable to parse "+
					"nonce: %v", err)
			}
			var nonce orderT.Nonce
			copy(nonce[:], nonceBytes)

			// Then parse the reject type.
			var rejectType venue.RejectType
			switch reason.ReasonCode {
			case auctioneerrpc.OrderReject_DUPLICATE_PEER:
				rejectType = venue.PartialRejectDuplicatePeer

			case auctioneerrpc.OrderReject_CHANNEL_FUNDING_FAILED:
				rejectType = venue.PartialRejectFundingFailed

			default:
				return nil, fmt.Errorf("unknown RPC reject "+
					"type: %v", reason.ReasonCode)
			}

			orders[nonce] = &venue.Reject{
				Type:   rejectType,
				Reason: reason.Reason,
			}
		}

		return &venue.TraderPartialRejectMsg{
			BatchID: batchID,
			Trader:  trader,
			Orders:  orders,
		}, nil

	// Trader rejects the whole batch.
	default:
		var rejectType venue.RejectType
		switch msg.Reject.ReasonCode {
		case auctioneerrpc.OrderMatchReject_BATCH_VERSION_MISMATCH:
			rejectType = venue.FullRejectBatchVersionMismatch

		case auctioneerrpc.OrderMatchReject_SERVER_MISBEHAVIOR:
			rejectType = venue.FullRejectServerMisbehavior

		case auctioneerrpc.OrderMatchReject_UNKNOWN:
			rejectType = venue.FullRejectUnknown

		default:
			return nil, fmt.Errorf("unknown RPC reject "+
				"type: %v", msg.Reject.ReasonCode)
		}

		return &venue.TraderRejectMsg{
			BatchID: batchID,
			Trader:  trader,
			Type:    rejectType,
			Reason:  msg.Reject.Reason,
		}, nil
	}
}

// newAcctResNotCompletedError creates a new AcctResNotCompletedError error from
// an account reservation.
func newAcctResNotCompletedError(
	res *account.Reservation) *auctioneer.AcctResNotCompletedError {

	result := &auctioneer.AcctResNotCompletedError{
		Value:      res.Value,
		AcctKey:    res.TraderKeyRaw,
		Expiry:     res.Expiry,
		HeightHint: res.HeightHint,
	}
	copy(
		result.AuctioneerKey[:],
		res.AuctioneerKey.PubKey.SerializeCompressed(),
	)
	copy(
		result.InitialBatchKey[:],
		res.InitialBatchKey.SerializeCompressed(),
	)
	return result
}

// BatchSnapshot returns details about a past executed batch. If the target
// batch ID is nil, then the last executed batch will be returned.
func (s *rpcServer) BatchSnapshot(ctx context.Context,
	req *auctioneerrpc.BatchSnapshotRequest) (*auctioneerrpc.BatchSnapshotResponse, error) {

	rpcLog.Tracef("[BatchSnapshot] batch_id=%x", req.BatchId)

	// If the passed batch ID wasn't specified, or is nil, then we'll fetch
	// the key for the current batch key (which isn't associated with a
	// cleared batch, then walk that back one to get to the most recent
	// batch.
	var (
		err      error
		batchKey *btcec.PublicKey
	)

	if len(req.BatchId) == 0 || bytes.Equal(zeroBatchID[:], req.BatchId) {
		currentBatchKey, err := s.store.BatchKey(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to fetch latest "+
				"batch key: %v", err)
		}

		batchKey = poolscript.DecrementKey(currentBatchKey)
	} else {
		batchKey, err = btcec.ParsePubKey(req.BatchId, btcec.S256())
		if err != nil {
			return nil, fmt.Errorf("unable to parse "+
				"batch ID (%x): %v", req.BatchId, err)
		}
	}

	batchSnapshot, err := s.lookupSnapshot(ctx, batchKey)
	if err != nil {
		return nil, err
	}

	return marshallBatchSnapshot(batchKey, batchSnapshot)
}

// lookupSnapshot checks whether the batch snapshot with the given batch ID
// already exists in the cache. If it does, it's returned directly, otherwise
// it is fetched from the database and placed into the cache.
func (s *rpcServer) lookupSnapshot(ctx context.Context,
	batchKey *btcec.PublicKey) (*subastadb.BatchSnapshot, error) {

	// Hold the mutex during the whole duration of this method to ensure
	// that two parallel requests for the same batch ID will be serialized
	// properly and the second one can be served from cache.
	s.snapshotCacheMtx.Lock()
	defer s.snapshotCacheMtx.Unlock()

	// TODO(guggero): Use LRU cache instead of storing all batches in RAM?
	batchID := orderT.NewBatchID(batchKey)
	batchSnapshot, ok := s.snapshotCache[batchID]

	// If we don't have that particular batch in the cache, let's fetch it
	// once and populate the cache for future requests.
	if !ok {
		var err error
		batchSnapshot, err = s.store.GetBatchSnapshot(ctx, batchID)
		if err != nil {
			return nil, err
		}

		s.snapshotCache[batchID] = batchSnapshot
	}

	return batchSnapshot, nil
}

// BatchSnapshots returns a list of batch snapshots starting at the start batch
// ID and going back through the history of batches, returning at most the
// number of specified batches. A maximum of 100 snapshots can be queried in
// one call. If no start batch ID is provided, the most recent finalized batch
// is used as the starting point to go back from.
func (s *rpcServer) BatchSnapshots(ctx context.Context,
	req *auctioneerrpc.BatchSnapshotsRequest) (*auctioneerrpc.BatchSnapshotsResponse,
	error) {

	rpcLog.Tracef("[BatchSnapshots] start_batch_id=%x, num_batches_back=%x",
		req.StartBatchId, req.NumBatchesBack)

	if req.NumBatchesBack == 0 || req.NumBatchesBack > maxSnapshotsPerRequest {
		return nil, fmt.Errorf("invalid num batches back, must be "+
			"between 1 and %d", maxSnapshotsPerRequest)
	}

	// If the passed start batch ID wasn't specified, or is nil, then we'll
	// fetch the key for the current batch key (which isn't associated with
	// a cleared batch, then walk that back one to get to the most recent
	// batch.
	var (
		err           error
		startBatchKey *btcec.PublicKey
	)
	if len(req.StartBatchId) == 0 ||
		bytes.Equal(zeroBatchID[:], req.StartBatchId) {

		currentBatchKey, err := s.store.BatchKey(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to fetch latest batch "+
				"key: %v", err)
		}

		// If there is no finalized batch yet, return an empty response.
		if currentBatchKey.IsEqual(subastadb.InitialBatchKey) {
			return &auctioneerrpc.BatchSnapshotsResponse{}, nil
		}

		startBatchKey = poolscript.DecrementKey(currentBatchKey)
	} else {
		startBatchKey, err = btcec.ParsePubKey(
			req.StartBatchId, btcec.S256(),
		)
		if err != nil {
			return nil, fmt.Errorf("unable to parse start batch "+
				"ID (%x): %v", req.StartBatchId, err)
		}
	}

	numBatches := int(req.NumBatchesBack)
	resp := &auctioneerrpc.BatchSnapshotsResponse{
		Batches: make([]*auctioneerrpc.BatchSnapshotResponse, 0, numBatches),
	}
	batchKey := startBatchKey
	for i := 0; i < numBatches; i++ {
		// Try to get the snapshot from the cache.
		batchSnapshot, err := s.lookupSnapshot(ctx, batchKey)
		if err != nil {
			return nil, err
		}

		rpcBatch, err := marshallBatchSnapshot(batchKey, batchSnapshot)
		if err != nil {
			return nil, err
		}
		resp.Batches = append(resp.Batches, rpcBatch)

		// We've reached the initial batch key, we can't continue any
		// further and are done.
		if batchKey.IsEqual(subastadb.InitialBatchKey) {
			break
		}

		batchKey = poolscript.DecrementKey(batchKey)
	}

	return resp, nil
}

// marshallBatchSnapshot converts a batch snapshot into the RPC representation.
func marshallBatchSnapshot(batchKey *btcec.PublicKey,
	batchSnapshot *subastadb.BatchSnapshot) (*auctioneerrpc.BatchSnapshotResponse,
	error) {

	batch := batchSnapshot.OrderBatch
	batchTx := batchSnapshot.BatchTx
	prevBatchID := zeroBatchID[:]

	// Now that we have the batch key, we'll also derive the _prior_ batch
	// key so the client can use this as a sort of linked list to navigate
	// the batch chain. Unless of course we reached the initial batch key.
	if !batchKey.IsEqual(subastadb.InitialBatchKey) {
		prevBatchKey := poolscript.DecrementKey(batchKey)
		prevBatchID = prevBatchKey.SerializeCompressed()
	}

	resp := &auctioneerrpc.BatchSnapshotResponse{
		Version:             uint32(batch.Version),
		BatchId:             batchKey.SerializeCompressed(),
		PrevBatchId:         prevBatchID,
		CreationTimestampNs: uint64(batch.CreationTimestamp.UnixNano()),
		MatchedOrders: make(
			[]*auctioneerrpc.MatchedOrderSnapshot, len(batch.Orders),
		),
		MatchedMarkets: make(map[uint32]*auctioneerrpc.MatchedMarketSnapshot),
	}

	for duration, subBatch := range batch.SubBatches {
		resp.MatchedMarkets[duration] = &auctioneerrpc.MatchedMarketSnapshot{
			MatchedOrders: make(
				[]*auctioneerrpc.MatchedOrderSnapshot, len(subBatch),
			),
			ClearingPriceRate: uint32(batch.ClearingPrices[duration]),
		}
	}

	// The response for this call is a bit simpler than the
	// RelevantBatchSnapshot call, in that we only need to return the set
	// of orders, and not also the accounts diffs.
	orderIdx := 0
	for duration, subBatch := range batch.SubBatches {
		market := resp.MatchedMarkets[duration]
		for i, o := range subBatch {
			ask := o.Details.Ask
			bid := o.Details.Bid
			quote := o.Details.Quote

			rpcSnapshot := &auctioneerrpc.MatchedOrderSnapshot{
				Ask: &auctioneerrpc.AskSnapshot{
					Version:             uint32(ask.Version),
					LeaseDurationBlocks: ask.LeaseDuration(),
					RateFixed:           ask.Details().FixedRate,
					ChanType:            uint32(ask.ServerDetails().ChanType),
				},
				Bid: &auctioneerrpc.BidSnapshot{
					Version:             uint32(bid.Version),
					LeaseDurationBlocks: bid.LeaseDuration(),
					RateFixed:           bid.Details().FixedRate,
					ChanType:            uint32(bid.ServerDetails().ChanType),
				},
				MatchingRate:     uint32(quote.MatchingRate),
				TotalSatsCleared: uint64(quote.TotalSatsCleared),
				UnitsMatched:     uint32(quote.UnitsMatched),
			}
			market.MatchedOrders[i] = rpcSnapshot
			resp.MatchedOrders[orderIdx] = rpcSnapshot // nolint:staticcheck
			orderIdx++
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

	// We'll also need to include its fee rate in the response.
	txWeight := blockchain.GetTransactionWeight(btcutil.NewTx(batchTx))
	txFeeRate := feebump.FeeRate(batchSnapshot.BatchTxFee, txWeight)
	resp.BatchTxFeeRateSatPerKw = uint64(txFeeRate)

	return resp, nil
}

// NodeRating returns node rating for a set of nodes on LN.
func (s *rpcServer) NodeRating(ctx context.Context,
	req *auctioneerrpc.ServerNodeRatingRequest) (*auctioneerrpc.ServerNodeRatingResponse, error) {

	nodeRatings := make([]*auctioneerrpc.NodeRating, 0, len(req.NodePubkeys))
	for _, nodePub := range req.NodePubkeys {
		var pub [33]byte
		copy(pub[:], nodePub)

		nodeTier := orderT.DefaultMinNodeTier
		if s.ratingAgency != nil {
			nodeTier = s.ratingAgency.RateNode(pub)
		}

		rpcNodeTier, err := marshallNodeTier(nodeTier)
		if err != nil {
			return nil, err
		}

		nodeRatings = append(nodeRatings, &auctioneerrpc.NodeRating{
			NodePubkey: nodePub,
			NodeTier:   rpcNodeTier,
		})
	}

	return &auctioneerrpc.ServerNodeRatingResponse{
		NodeRatings: nodeRatings,
	}, nil
}

// parseOnionAddr parses an onion address specified in host:port format.
func parseOnionAddr(onionAddr string) (net.Addr, error) {
	addrHost, addrPort, err := net.SplitHostPort(onionAddr)
	if err != nil {
		// If the port wasn't specified, then we'll assume the
		// default p2p port.
		addrHost = onionAddr
		addrPort = "9735" // TODO(roasbeef): constant somewhere?
	}

	portNum, err := strconv.Atoi(addrPort)
	if err != nil {
		return nil, err
	}

	return &tor.OnionAddr{
		OnionService: addrHost,
		Port:         portNum,
	}, nil
}

// unmarshallNodeTier maps the RPC node tier enum to the node tier used in
// memory.
func unmarshallNodeTier(nodeTier auctioneerrpc.NodeTier) (orderT.NodeTier, error) {
	switch nodeTier {
	// This is the boundary where we enforce our interpretation of the min
	// node tier: clients that specify the default on the RPC layer will be
	// mapped to our current in-memory default.
	case auctioneerrpc.NodeTier_TIER_DEFAULT:
		// TODO(roasbeef): base off order version?
		return orderT.DefaultMinNodeTier, nil

	case auctioneerrpc.NodeTier_TIER_1:
		return orderT.NodeTier1, nil

	case auctioneerrpc.NodeTier_TIER_0:
		return orderT.NodeTier0, nil

	default:
		return 0, fmt.Errorf("unknown node tier: %v", nodeTier)
	}
}

// marshallNodeTier maps the node tier integer into the enum used on the RPC
// interface.
func marshallNodeTier(nodeTier orderT.NodeTier) (auctioneerrpc.NodeTier, error) {
	switch nodeTier {

	case orderT.NodeTierDefault:
		return auctioneerrpc.NodeTier_TIER_DEFAULT, nil

	case orderT.NodeTier1:
		return auctioneerrpc.NodeTier_TIER_1, nil

	case orderT.NodeTier0:
		return auctioneerrpc.NodeTier_TIER_0, nil

	default:
		return 0, fmt.Errorf("unknown node tier: %v", nodeTier)
	}
}

// marshallDurationBucketState maps the duration bucket state integer into the
// enum used on the RPC interface.
func marshallDurationBucketState(
	state order.DurationBucketState) (auctioneerrpc.DurationBucketState, error) {

	switch state {
	case order.BucketStateNoMarket:
		return auctioneerrpc.DurationBucketState_NO_MARKET, nil

	case order.BucketStateMarketClosed:
		return auctioneerrpc.DurationBucketState_MARKET_CLOSED, nil

	case order.BucketStateAcceptingOrders:
		return auctioneerrpc.DurationBucketState_ACCEPTING_ORDERS, nil

	case order.BucketStateClearingMarket:
		return auctioneerrpc.DurationBucketState_MARKET_OPEN, nil

	default:
		return 0, fmt.Errorf("unknown duration bucket state: %v", state)
	}
}
