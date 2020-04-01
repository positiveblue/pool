package agora

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	accountT "github.com/lightninglabs/agora/client/account"
	"github.com/lightninglabs/agora/client/clmrpc"
	orderT "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
	"github.com/lightninglabs/agora/venue"
	"github.com/lightninglabs/agora/venue/matching"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/lnwire"
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

	store agoradb.Store

	batchExecutor *venue.BatchExecutor

	lnd *lndclient.GrpcLndServices

	bestHeight func() uint32

	feeSchedule *orderT.LinearFeeSchedule

	// connectedStreams is the list of all currently connected
	// bi-directional update streams. Each trader has exactly one stream
	// but can subscribe to updates for multiple accounts through the same
	// stream.
	connectedStreams map[lsat.TokenID]*TraderStream
}

// newRPCServer creates a new rpcServer.
func newRPCServer(store agoradb.Store, lnd *lndclient.GrpcLndServices,
	accountManager *account.Manager, bestHeight func() uint32,
	orderBook *order.Book, batchExecutor *venue.BatchExecutor,
	feeSchedule *orderT.LinearFeeSchedule, listener net.Listener,
	serverOpts []grpc.ServerOption,
	subscribeTimeout time.Duration) *rpcServer {

	return &rpcServer{
		grpcServer:       grpc.NewServer(serverOpts...),
		listener:         listener,
		bestHeight:       bestHeight,
		lnd:              lnd,
		accountManager:   accountManager,
		orderBook:        orderBook,
		store:            store,
		batchExecutor:    batchExecutor,
		feeSchedule:      feeSchedule,
		quit:             make(chan struct{}),
		connectedStreams: make(map[lsat.TokenID]*TraderStream),
		subscribeTimeout: subscribeTimeout,
	}
}

// Start starts the rpcServer, making it ready to accept incoming requests.
func (s *rpcServer) Start() error {
	if !atomic.CompareAndSwapUint32(&s.started, 0, 1) {
		return nil
	}

	log.Infof("Starting auction server")

	s.serveWg.Add(1)
	go func() {
		defer s.serveWg.Done()

		log.Infof("RPC server listening on %s", s.listener.Addr())
		err := s.grpcServer.Serve(s.listener)
		if err != nil && err != grpc.ErrServerStopped {
			log.Errorf("RPC server stopped with error: %v", err)
		}
	}()

	log.Infof("Auction server is now active")

	return nil
}

// Stop stops the server.
func (s *rpcServer) Stop() error {
	if !atomic.CompareAndSwapUint32(&s.stopped, 0, 1) {
		return nil
	}

	log.Info("Stopping auction server")

	close(s.quit)
	s.wg.Wait()

	log.Info("Stopping lnd client, gRPC server and listener")
	s.grpcServer.GracefulStop()
	err := s.listener.Close()
	if err != nil {
		return fmt.Errorf("error closing gRPC listener: %v", err)
	}
	s.serveWg.Wait()

	log.Info("Auction server stopped")
	return nil
}

func (s *rpcServer) ReserveAccount(ctx context.Context,
	req *clmrpc.ReserveAccountRequest) (*clmrpc.ReserveAccountResponse,
	error) {

	// The token ID can only be zero when testing locally without Kirin (or
	// during the integration tests). In a real deployment, Kirin enforces
	// the token to be set so we don't need an explicit check here.
	tokenID := tokenIDFromContext(ctx)

	// TODO(guggero): Make sure we enforce maxAccountsPerTrader here.

	reservation, err := s.accountManager.ReserveAccount(ctx, tokenID)
	if err != nil {
		return nil, err
	}

	return &clmrpc.ReserveAccountResponse{
		AuctioneerKey:   reservation.AuctioneerKey.PubKey.SerializeCompressed(),
		InitialBatchKey: reservation.InitialBatchKey.SerializeCompressed(),
	}, nil
}

// parseRPCAccountParams parses the relevant account parameters from a
// ServerInitAccountRequest RPC message.
func parseRPCAccountParams(
	req *clmrpc.ServerInitAccountRequest) (*account.Parameters, error) {

	var txid chainhash.Hash
	copy(txid[:], req.AccountPoint.Txid)
	accountPoint := wire.OutPoint{
		Hash:  txid,
		Index: req.AccountPoint.OutputIndex,
	}

	traderKey, err := btcec.ParsePubKey(req.UserSubKey, btcec.S256())
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
	req *clmrpc.ServerInitAccountRequest) (*clmrpc.ServerInitAccountResponse, error) {

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

	return &clmrpc.ServerInitAccountResponse{}, nil
}

func (s *rpcServer) ModifyAccount(ctx context.Context,
	req *clmrpc.ServerModifyAccountRequest) (
	*clmrpc.ServerModifyAccountResponse, error) {

	traderKey, err := btcec.ParsePubKey(req.UserSubKey, btcec.S256())
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

	var newAccountParams *account.Parameters
	if req.NewAccount != nil {
		newAccountParams, err = parseRPCAccountParams(req.NewAccount)
		if err != nil {
			return nil, err
		}
	}

	accountSig, err := s.accountManager.ModifyAccount(
		ctx, traderKey, newInputs, newOutputs, newAccountParams,
		s.bestHeight(),
	)
	if err != nil {
		return nil, err
	}

	return &clmrpc.ServerModifyAccountResponse{
		AccountSig: accountSig,
	}, nil
}

// SubmitOrder parses a client's request to submit an order, validates it and
// if successful, stores it to the database and hands it over to the manager
// for further processing.
func (s *rpcServer) SubmitOrder(ctx context.Context,
	req *clmrpc.ServerSubmitOrderRequest) (
	*clmrpc.ServerSubmitOrderResponse, error) {

	// TODO(roasbeef): don't accept orders if auctioneer doesn't have
	// master account
	//  * make new interface for? have all operations flow thru the
	//    auctioneer?
	//  * allow order cancellations tho?

	var o order.ServerOrder
	switch requestOrder := req.Details.(type) {
	case *clmrpc.ServerSubmitOrderRequest_Ask:
		a := requestOrder.Ask
		clientKit, serverKit, err := s.parseRPCOrder(
			ctx, a.Version, a.Details,
		)
		if err != nil {
			return nil, err
		}
		clientAsk := &orderT.Ask{
			Kit:         *clientKit,
			MaxDuration: uint32(a.MaxDurationBlocks),
		}
		o = &order.Ask{
			Ask: *clientAsk,
			Kit: *serverKit,
		}

	case *clmrpc.ServerSubmitOrderRequest_Bid:
		b := requestOrder.Bid
		clientKit, serverKit, err := s.parseRPCOrder(
			ctx, b.Version, b.Details,
		)
		if err != nil {
			return nil, err
		}
		clientBid := &orderT.Bid{
			Kit:         *clientKit,
			MinDuration: uint32(b.MinDurationBlocks),
		}
		o = &order.Bid{
			Bid: *clientBid,
			Kit: *serverKit,
		}

	default:
		return nil, fmt.Errorf("invalid order request")
	}

	// Formally everything seems OK, hand over the order to the manager for
	// further validation and processing.
	err := s.orderBook.PrepareOrder(ctx, o)

	// TODO(guggero): Remove once real batch execution is working.
	// For now, we just simulate a batch for each registered trader's
	// accounts.
	for _, trader := range s.connectedStreams {
		for _, traderAcct := range trader.Subscriptions {
			_, err = s.batchExecutor.Submit(&matching.OrderBatch{
				Orders: []matching.MatchedOrder{
					{
						Asker: *traderAcct.Trader,
					},
				},
				ClearingPrice: 0,
			})
			if err != nil {
				log.Errorf("Error faking batch execution: %v",
					err)
			}
		}
	}

	return mapOrderResp(o.Nonce(), err)
}

// CancelOrder tries to remove an order from the order book and mark it as
// revoked by the user.
func (s *rpcServer) CancelOrder(ctx context.Context,
	req *clmrpc.ServerCancelOrderRequest) (
	*clmrpc.ServerCancelOrderResponse, error) {

	var nonce orderT.Nonce
	copy(nonce[:], req.OrderNonce)
	err := s.orderBook.CancelOrder(ctx, nonce)
	if err != nil {
		return nil, err
	}

	return &clmrpc.ServerCancelOrderResponse{}, nil
}

// SubscribeBatchAuction is a streaming RPC that allows a trader to subscribe
// to updates and events around accounts and orders. This method will be called
// by the RPC server once per connection and will keep running for the entire
// length of the connection. Each method invocation represents one trader with
// multiple accounts and multiple order per account.
func (s *rpcServer) SubscribeBatchAuction(
	stream clmrpc.ChannelAuctioneer_SubscribeBatchAuctionServer) error {

	// Don't let the rpcServer shut down while we have traders connected.
	s.wg.Add(1)
	defer s.wg.Done()

	// First let's make sure the trader isn't trying to open more than one
	// connection. Pinning this to the LSAT is not a 100% guarantee there
	// won't be two streams with the same accounts, but it should prevent
	// a badly implemented client from draining our TCP connections by
	// accident. The token ID can only be zero when testing locally without
	// Kirin (or during the integration tests). In a real deployment, Kirin
	// enforces the token to be set so we don't need an explicit check here.
	traderID := tokenIDFromContext(stream.Context())
	if _, ok := s.connectedStreams[traderID]; ok {
		return fmt.Errorf("client already connected, only one stream " +
			"per trader is allowed")
	}
	log.Debugf("New trader client_id=%x connected to stream", traderID)

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
	s.connectedStreams[traderID] = trader
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
			log.Debugf("New subscription, client_id=%x, acct=%x",
				traderID, newSub.AccountKey)
			err := s.addStreamSubscription(traderID, newSub)
			if err != nil {
				return fmt.Errorf("unable to register "+
					"subscription: %v", err)
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
			log.Debugf("Trader client_id=%x is disconnecting",
				trader.Lsat)
			return s.disconnectTrader(traderID)

		// An error happened anywhere in the process, we need to abort
		// the connection.
		case err := <-trader.comms.err:
			log.Errorf("Error in trader stream: %v", err)

			trader.comms.abort()

			err2 := s.disconnectTrader(traderID)
			if err2 != nil {
				log.Errorf("Unable to disconnect trader: %v",
					err2)
			}
			return fmt.Errorf("error reading client stream: %v",
				err)

		// The server is shutting down.
		case <-s.quit:
			err := stream.Send(&clmrpc.ServerAuctionMessage{
				Msg: &clmrpc.ServerAuctionMessage_Shutdown{
					Shutdown: &clmrpc.ServerShutdown{},
				},
			})
			if err != nil {
				log.Errorf("Unable to send shutdown msg: %v",
					err)
			}

			trader.comms.abort()

			return fmt.Errorf("server shutting down")
		}
	}
}

// readIncomingStream reads incoming messages on a bi-directional stream and
// forwards them to the correct channels. For now, only subscription messages
// can be sent from the client to the server.
func (s *rpcServer) readIncomingStream(trader *TraderStream,
	stream clmrpc.ChannelAuctioneer_SubscribeBatchAuctionServer) {

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
func (s *rpcServer) handleIncomingMessage(rpcMsg *clmrpc.ClientAuctionMessage,
	stream clmrpc.ChannelAuctioneer_SubscribeBatchAuctionServer,
	trader *TraderStream) {

	comms := trader.comms
	switch msg := rpcMsg.Msg.(type) {
	// A new account commitment is the first step of the 3-way auth
	// handshake between the auctioneer and the trader.
	case *clmrpc.ClientAuctionMessage_Commit:
		commit := msg.Commit

		// First check that they are using the latest version of the
		// batch execution protocol. If not, they need to update. Better
		// reject them now instead of waiting for a batch to be prepared
		// and then everybody bailing out because of a version mismatch.
		if commit.BatchVersion != uint32(orderT.CurrentVersion) {
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
		err = stream.Send(&clmrpc.ServerAuctionMessage{
			Msg: &clmrpc.ServerAuctionMessage_Challenge{
				Challenge: &clmrpc.ServerChallenge{
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
	case *clmrpc.ClientAuctionMessage_Subscribe:
		subscribe := msg.Subscribe

		// Parse their public key to validate the signature and later
		// retrieve it from the store.
		acctPubKey, err := btcec.ParsePubKey(
			msg.Subscribe.UserSubKey, btcec.S256(),
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
		sigValid, err := s.lnd.Signer.VerifyMessage(
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

		// The signature is valid, we can now fetch the account from the
		// store.
		acct, err := s.store.Account(stream.Context(), acctPubKey)
		if err != nil {
			comms.err <- fmt.Errorf("error reading account: %v",
				err)
			return
		}

		// Finally inform the batch executor about the new connected
		// client.
		commLine := &venue.DuplexLine{
			Send: comms.toTrader,
			Recv: comms.toServer,
		}
		trader := &venue.ActiveTrader{
			CommLine: commLine,
			Trader: &matching.Trader{
				AccountKey:      acctKey,
				AccountExpiry:   acct.Expiry,
				AccountOutPoint: acct.OutPoint,
				AccountBalance:  acct.Value,
			},
		}
		comms.newSub <- trader

	// The trader accepts an order execution.
	case *clmrpc.ClientAuctionMessage_Accept:
		nonces := make([]orderT.Nonce, len(msg.Accept.OrderNonce))
		for idx, rawNonce := range msg.Accept.OrderNonce {
			if len(rawNonce) != 32 {
				comms.err <- fmt.Errorf("invalid nonce: %x",
					rawNonce)
				return
			}
			copy(nonces[idx][:], rawNonce)
		}

		// De-multiplex the incoming message for the venue.
		for _, subscribedTrader := range trader.Subscriptions {
			traderMsg := &venue.TraderAcceptMsg{
				BatchID: msg.Accept.BatchId,
				Trader:  subscribedTrader,
				Orders:  nonces,
			}
			comms.toServer <- traderMsg
		}

	// The trader rejected an order execution.
	case *clmrpc.ClientAuctionMessage_Reject:
		// De-multiplex the incoming message for the venue.
		for _, subscribedTrader := range trader.Subscriptions {
			traderMsg := &venue.TraderRejectMsg{
				BatchID: msg.Reject.BatchId,
				Trader:  subscribedTrader,
				Reason:  msg.Reject.Reason,
			}
			comms.toServer <- traderMsg
		}

	// The trader signed their account inputs.
	case *clmrpc.ClientAuctionMessage_Sign:
		sigs := make(map[string][][]byte)
		for key, value := range msg.Sign.AccountWitness {
			sigs[key] = value.Witness
		}

		// De-multiplex the incoming message for the venue.
		for _, subscribedTrader := range trader.Subscriptions {
			traderMsg := &venue.TraderSignMsg{
				BatchID: msg.Sign.BatchId,
				Trader:  subscribedTrader,
				Sigs:    sigs,
			}
			comms.toServer <- traderMsg
		}

	default:
		comms.err <- fmt.Errorf("unknown trader message: %v", msg)
		return
	}
}

// sendToTrader converts an internal execution message to the gRPC format and
// sends it out on the stream to the trader.
func (s *rpcServer) sendToTrader(
	stream clmrpc.ChannelAuctioneer_SubscribeBatchAuctionServer,
	msg venue.ExecutionMsg) error {

	switch msg.(type) {
	case *venue.PrepareMsg:
		return stream.Send(&clmrpc.ServerAuctionMessage{
			Msg: &clmrpc.ServerAuctionMessage_Prepare{
				Prepare: &clmrpc.OrderMatchPrepare{
					// TODO(roasbeef): fill all fields.
				},
			},
		})

	case *venue.FinalizeMsg:
		return stream.Send(&clmrpc.ServerAuctionMessage{
			Msg: &clmrpc.ServerAuctionMessage_Finalize{
				Finalize: &clmrpc.OrderMatchFinalize{
					// TODO(roasbeef): fill all fields.
				},
			},
		})

	default:
		return fmt.Errorf("unknown message type: %v", msg)
	}
}

// addStreamSubscription adds an account subscription to the stream that is
// already established for a trader. The subscriptions are de-duplicated and new
// subscriptions are registered with the batch executor.
func (s *rpcServer) addStreamSubscription(traderID lsat.TokenID,
	newSub *venue.ActiveTrader) error {

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
	for _, trader := range subscriptions {
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
	req *clmrpc.ServerOrderStateRequest) (*clmrpc.ServerOrderStateResponse,
	error) {

	var nonce orderT.Nonce
	copy(nonce[:], req.OrderNonce)

	// The state of an order should be reflected in the database so we don't
	// need to ask the manager about it.
	o, err := s.store.GetOrder(ctx, nonce)
	if err != nil {
		return nil, err
	}
	return &clmrpc.ServerOrderStateResponse{
		State:            clmrpc.OrderState(o.Details().State),
		UnitsUnfulfilled: uint32(o.Details().UnitsUnfulfilled),
	}, nil
}

// FeeQuote returns all the fees as they are currently configured.
func (s *rpcServer) FeeQuote(_ context.Context, _ *clmrpc.FeeQuoteRequest) (
	*clmrpc.FeeQuoteResponse, error) {

	return &clmrpc.FeeQuoteResponse{
		ExecutionFee: &clmrpc.ExecutionFee{
			BaseFee: int64(s.feeSchedule.BaseFee()),
			FeeRate: int64(s.feeSchedule.FeeRate()),
		},
	}, nil
}

// parseRPCOrder parses the incoming raw RPC order into the go native data
// types used in the order struct.
func (s *rpcServer) parseRPCOrder(ctx context.Context, version uint32,
	details *clmrpc.ServerOrder) (*orderT.Kit, *order.Kit, error) {

	// Parse the RPC fields into the common client struct.
	clientKit, nodeKey, addrs, multiSigKey, err := orderT.ParseRPCServerOrder(
		version, details,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse server order: %v",
			err)
	}

	// Make sure the referenced account exists.
	_, err = s.store.Account(ctx, clientKit.AcctKey)
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
	*clmrpc.ServerSubmitOrderResponse, error) {

	switch err {
	case nil:
		return &clmrpc.ServerSubmitOrderResponse{
			Details: &clmrpc.ServerSubmitOrderResponse_Accepted{
				Accepted: true,
			},
		}, nil

	case order.ErrInvalidAmt:
		return &clmrpc.ServerSubmitOrderResponse{
			Details: &clmrpc.ServerSubmitOrderResponse_InvalidOrder{
				InvalidOrder: &clmrpc.InvalidOrder{
					OrderNonce: orderNonce[:],
					FailReason: clmrpc.InvalidOrder_INVALID_AMT,
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
	tokenValue := auth.FromContext(ctx, auth.KeyTokenID)
	if token, ok := tokenValue.(lsat.TokenID); ok {
		return token
	}
	return zeroToken
}
