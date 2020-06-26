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
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/kirin/auth"
	accountT "github.com/lightninglabs/llm/account"
	"github.com/lightninglabs/llm/auctioneer"
	"github.com/lightninglabs/llm/clmrpc"
	"github.com/lightninglabs/llm/clmscript"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
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

	store subastadb.Store

	batchExecutor *venue.BatchExecutor

	lnd *lndclient.GrpcLndServices

	bestHeight func() uint32

	feeSchedule *orderT.LinearFeeSchedule

	// connectedStreams is the list of all currently connected
	// bi-directional update streams. Each trader has exactly one stream
	// but can subscribe to updates for multiple accounts through the same
	// stream.
	connectedStreams map[lsat.TokenID]*TraderStream

	// connectedStreamsMutex is a mutex guarding access to connectedStreams.
	connectedStreamsMutex sync.Mutex
}

// newRPCServer creates a new rpcServer.
func newRPCServer(store subastadb.Store, lnd *lndclient.GrpcLndServices,
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
func (s *rpcServer) Stop() {
	if !atomic.CompareAndSwapUint32(&s.stopped, 0, 1) {
		return
	}

	log.Info("Stopping auction server")

	close(s.quit)
	s.wg.Wait()

	log.Info("Stopping lnd client, gRPC server and listener")
	s.grpcServer.Stop()

	s.serveWg.Wait()

	log.Info("Auction server stopped")
}

func (s *rpcServer) ReserveAccount(ctx context.Context,
	req *clmrpc.ReserveAccountRequest) (*clmrpc.ReserveAccountResponse,
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
	}

	accountSig, err := s.accountManager.ModifyAccount(
		ctx, traderKey, newInputs, newOutputs, modifiers,
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
			MaxDuration: a.MaxDurationBlocks,
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
			MinDuration: b.MinDurationBlocks,
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
	err := s.orderBook.PrepareOrder(ctx, o)
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
			log.Debugf("New subscription, client_id=%x, acct=%x",
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
			err = stream.Send(&clmrpc.ServerAuctionMessage{
				Msg: &clmrpc.ServerAuctionMessage_Success{
					Success: &clmrpc.SubscribeSuccess{
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
			log.Debugf("Trader client_id=%x is disconnecting",
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
				errCode := clmrpc.SubscribeError_ACCOUNT_DOES_NOT_EXIST
				err = stream.Send(&clmrpc.ServerAuctionMessage{
					Msg: &clmrpc.ServerAuctionMessage_Error{
						Error: &clmrpc.SubscribeError{
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
				errCode := clmrpc.SubscribeError_INCOMPLETE_ACCOUNT_RESERVATION
				partialAcct := &clmrpc.AuctionAccount{
					Value:         uint64(e2.Value),
					TraderKey:     e2.AcctKey[:],
					AuctioneerKey: e2.AuctioneerKey[:],
					BatchKey:      e2.InitialBatchKey[:],
					Expiry:        e2.Expiry,
					HeightHint:    e2.HeightHint,
				}
				errMsg := &clmrpc.SubscribeError{
					Error:              err.Error(),
					ErrorCode:          errCode,
					TraderKey:          e2.AcctKey[:],
					AccountReservation: partialAcct,
				}
				err = stream.Send(&clmrpc.ServerAuctionMessage{
					Msg: &clmrpc.ServerAuctionMessage_Error{
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
			errCode := clmrpc.SubscribeError_SERVER_SHUTDOWN
			err := stream.Send(&clmrpc.ServerAuctionMessage{
				Msg: &clmrpc.ServerAuctionMessage_Error{
					Error: &clmrpc.SubscribeError{
						Error:     "server shutting down",
						ErrorCode: errCode,
					},
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

func (s *rpcServer) ConnectedStreams() map[lsat.TokenID]*TraderStream {
	s.connectedStreamsMutex.Lock()
	defer s.connectedStreamsMutex.Unlock()

	return s.connectedStreams
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
			var batchID orderT.BatchID
			copy(batchID[:], msg.Accept.BatchId)
			traderMsg := &venue.TraderAcceptMsg{
				BatchID: batchID,
				Trader:  subscribedTrader,
				Orders:  nonces,
			}
			comms.toServer <- traderMsg
		}

	// The trader rejected an order execution.
	case *clmrpc.ClientAuctionMessage_Reject:
		// De-multiplex the incoming message for the venue.
		for _, subscribedTrader := range trader.Subscriptions {
			var batchID orderT.BatchID
			copy(batchID[:], msg.Reject.BatchId)
			traderMsg := &venue.TraderRejectMsg{
				BatchID: batchID,
				Trader:  subscribedTrader,
				Reason:  msg.Reject.Reason,
			}
			comms.toServer <- traderMsg
		}

	// The trader signed their account inputs.
	case *clmrpc.ClientAuctionMessage_Sign:
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

		// De-multiplex the incoming message for the venue.
		for _, subscribedTrader := range trader.Subscriptions {
			traderMsg := &venue.TraderSignMsg{
				BatchID: msg.Sign.BatchId,
				Trader:  subscribedTrader,
				Sigs:    sigs,
			}
			comms.toServer <- traderMsg
		}

	// The trader wants to recover their lost account. We'll only do this
	// for accounts that are already subscribed so we can be sure it exists
	// on our side.
	case *clmrpc.ClientAuctionMessage_Recover:
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
	stream clmrpc.ChannelAuctioneer_SubscribeBatchAuctionServer) error {

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
				return fmt.Errorf("error canceling order: "+
					"%v", err)
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
	err = stream.Send(&clmrpc.ServerAuctionMessage{
		Msg: &clmrpc.ServerAuctionMessage_Account{
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
	stream clmrpc.ChannelAuctioneer_SubscribeBatchAuctionServer,
	msg venue.ExecutionMsg) error {

	switch m := msg.(type) {
	case *venue.PrepareMsg:
		feeSchedule, ok := m.ExecutionFee.(*orderT.LinearFeeSchedule)
		if !ok {
			return fmt.Errorf("FeeSchedule w/o fee rate used: %T",
				m.ExecutionFee)
		}

		// Each order the user submitted may be matched to one or more
		// corresponding orders, so we'll map the in-memory
		// representation we use to the proto representation that we
		// need to send to the client.
		matchedOrders := make(map[string]*clmrpc.MatchedOrder)
		for traderOrderNonce, orderMatches := range m.MatchedOrders {
			log.Debugf("Order(%x) matched w/ %v orders",
				traderOrderNonce[:], len(m.MatchedOrders))

			// As we support partial patches, this trader nonce
			// might be matched with a set of other orders, so
			// we'll unroll this here now.
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

				nonceStr := hex.EncodeToString(
					traderOrderNonce[:],
				)
				unitsFilled := o.Details.Quote.UnitsMatched

				ask, bid := o.Details.Ask, o.Details.Bid

				matchedOrders[nonceStr] = &clmrpc.MatchedOrder{}

				// If the client had their bid matched, then
				// we'll send over the ask information and the
				// other way around if it's a bid.
				if !isAsk {
					matchedOrders[nonceStr].MatchedAsks = append(
						matchedOrders[nonceStr].MatchedAsks,
						marshallMatchedAsk(ask, unitsFilled),
					)
				} else {
					matchedOrders[nonceStr].MatchedBids = append(
						matchedOrders[nonceStr].MatchedBids,
						marshallMatchedBid(bid, unitsFilled),
					)
				}
			}
		}

		// Next, for each account that the user had in this batch,
		// we'll generate a similar RPC account diff so they can verify
		// their portion of the batch.
		var accountDiffs []*clmrpc.AccountDiff
		for idx, acctDiff := range m.ChargedAccounts {
			acctDiff, err := marshallAccountDiff(
				acctDiff, m.AccountOutPoints[idx],
			)
			if err != nil {
				return err
			}

			accountDiffs = append(accountDiffs, acctDiff)
		}

		return stream.Send(&clmrpc.ServerAuctionMessage{
			Msg: &clmrpc.ServerAuctionMessage_Prepare{
				Prepare: &clmrpc.OrderMatchPrepare{
					MatchedOrders:     matchedOrders,
					ClearingPriceRate: uint32(m.ClearingPrice),
					ChargedAccounts:   accountDiffs,
					ExecutionFee: &clmrpc.ExecutionFee{
						BaseFee: uint64(m.ExecutionFee.BaseFee()),
						FeeRate: uint64(feeSchedule.FeeRate()),
					},
					BatchTransaction: m.BatchTx,
					FeeRateSatPerKw:  uint64(m.FeeRate),
					BatchId:          m.BatchID[:],
					BatchVersion:     m.BatchVersion,
				},
			},
		})

	case *venue.SignBeginMsg:
		return stream.Send(&clmrpc.ServerAuctionMessage{
			Msg: &clmrpc.ServerAuctionMessage_Sign{
				Sign: &clmrpc.OrderMatchSignBegin{
					BatchId: m.BatchID[:],
				},
			},
		})

	case *venue.FinalizeMsg:
		return stream.Send(&clmrpc.ServerAuctionMessage{
			Msg: &clmrpc.ServerAuctionMessage_Finalize{
				Finalize: &clmrpc.OrderMatchFinalize{
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
			BaseFee: uint64(s.feeSchedule.BaseFee()),
			FeeRate: uint64(s.feeSchedule.FeeRate()),
		},
	}, nil
}

// RelevantBatchSnapshot returns a slimmed-down snapshot of the requested batch
// only pertaining to the requested accounts.
func (s *rpcServer) RelevantBatchSnapshot(ctx context.Context,
	req *clmrpc.RelevantBatchRequest) (*clmrpc.RelevantBatch, error) {

	// We'll start by retrieving the snapshot of the requested batch.
	var batchID orderT.BatchID
	copy(batchID[:], req.Id)

	// TODO(wilmer): Add caching layer? LRU?
	batch, batchTx, err := s.store.GetBatchSnapshot(ctx, batchID)
	if err != nil {
		return nil, err
	}

	resp := &clmrpc.RelevantBatch{
		// TODO(wilmer): Set remaining fields when available.
		Version:           uint32(orderT.CurrentVersion),
		Id:                batchID[:],
		ClearingPriceRate: uint32(batch.ClearingPrice),
		ExecutionFee:      nil,
		Transaction:       nil,
		FeeRateSatPerKw:   0,
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
	resp.ChargedAccounts = make([]*clmrpc.AccountDiff, 0, len(req.Accounts))
	for _, account := range req.Accounts {
		var accountID matching.AccountID
		copy(accountID[:], account)

		diff, ok := batch.FeeReport.AccountDiffs[accountID]
		if !ok {
			continue
		}

		outputIndex, ok := clmscript.LocateOutputScript(
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

	// An order can be fulfilled by multiple orders of the opposing type, so
	// make sure we take that into notice.
	resp.MatchedOrders = make(map[string]*clmrpc.MatchedOrder)
	for _, order := range batch.Orders {
		if _, ok := accounts[order.Asker.AccountKey]; ok {
			nonce := order.Details.Ask.Nonce().String()
			matchedBid := marshallMatchedBid(
				order.Details.Bid, order.Details.Quote.UnitsMatched,
			)
			resp.MatchedOrders[nonce].MatchedBids = append(
				resp.MatchedOrders[nonce].MatchedBids, matchedBid,
			)
			continue
		}

		if _, ok := accounts[order.Bidder.AccountKey]; ok {
			nonce := order.Details.Bid.Nonce().String()
			matchedAsk := marshallMatchedAsk(
				order.Details.Ask, order.Details.Quote.UnitsMatched,
			)
			resp.MatchedOrders[nonce].MatchedAsks = append(
				resp.MatchedOrders[nonce].MatchedAsks, matchedAsk,
			)
		}
	}

	return resp, nil
}

// parseRPCOrder parses the incoming raw RPC order into the go native data
// types used in the order struct.
func (s *rpcServer) parseRPCOrder(ctx context.Context, version uint32,
	details *clmrpc.ServerOrder) (*orderT.Kit, *order.Kit, error) {

	// Parse the RPC fields into the common client struct.
	clientKit, nodeKey, addrs, multiSigKey, err := parseRPCServerOrder(
		version, details,
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

// marshallAccountDiff translates a matching.AccountDiff to its RPC counterpart.
func marshallAccountDiff(diff matching.AccountDiff,
	acctOutPoint wire.OutPoint) (*clmrpc.AccountDiff, error) {

	// TODO: Need to extend account.OnChainState with DustExtendedOffChain
	// and DustAddedToFees.
	var (
		endingState clmrpc.AccountDiff_AccountState
		opIdx       int32
	)
	switch state := account.EndingState(diff.EndingBalance); {
	case state == account.OnChainStateRecreated:
		endingState = clmrpc.AccountDiff_OUTPUT_RECREATED
		opIdx = int32(acctOutPoint.Index)
	case state == account.OnChainStateFullySpent:
		endingState = clmrpc.AccountDiff_OUTPUT_FULLY_SPENT
		opIdx = -1
	default:
		return nil, fmt.Errorf("unhandled state %v", state)
	}

	return &clmrpc.AccountDiff{
		EndingBalance: uint64(diff.EndingBalance),
		EndingState:   endingState,
		OutpointIndex: opIdx,
		TraderKey:     diff.StartingState.AccountKey[:],
	}, nil
}

// marshallServerAccount translates an account.Account into its RPC counterpart.
func marshallServerAccount(acct *account.Account) (*clmrpc.AuctionAccount, error) {
	rpcAcct := &clmrpc.AuctionAccount{
		Value:         uint64(acct.Value),
		Expiry:        acct.Expiry,
		TraderKey:     acct.TraderKeyRaw[:],
		AuctioneerKey: acct.AuctioneerKey.PubKey.SerializeCompressed(),
		BatchKey:      acct.BatchKey.SerializeCompressed(),
		HeightHint:    acct.HeightHint,
		Outpoint: &clmrpc.OutPoint{
			Txid:        acct.OutPoint.Hash[:],
			OutputIndex: acct.OutPoint.Index,
		},
	}

	switch acct.State {
	case account.StatePendingOpen:
		rpcAcct.State = clmrpc.AuctionAccountState_STATE_PENDING_OPEN

	case account.StateOpen:
		rpcAcct.State = clmrpc.AuctionAccountState_STATE_OPEN

	case account.StateExpired:
		rpcAcct.State = clmrpc.AuctionAccountState_STATE_EXPIRED

	case account.StateClosed:
		rpcAcct.State = clmrpc.AuctionAccountState_STATE_CLOSED

		var buf bytes.Buffer
		err := acct.CloseTx.Serialize(&buf)
		if err != nil {
			return nil, err
		}
		rpcAcct.CloseTx = buf.Bytes()

	case account.StatePendingUpdate:
		rpcAcct.State = clmrpc.AuctionAccountState_STATE_PENDING_UPDATE

	default:
		return nil, fmt.Errorf("unknown account state")
	}

	return rpcAcct, nil
}

// marshallMatchedAsk translates an order.Ask to its RPC counterpart.
func marshallMatchedAsk(ask *order.Ask,
	unitsFilled orderT.SupplyUnit) *clmrpc.MatchedAsk {

	return &clmrpc.MatchedAsk{
		Ask: &clmrpc.ServerAsk{
			Details:           marshallServerOrder(ask),
			MaxDurationBlocks: ask.MaxDuration(),
			Version:           uint32(ask.Version),
		},
		UnitsFilled: uint32(unitsFilled),
	}
}

// marshallMatchedBid translates an order.Bid to its RPC counterpart.
func marshallMatchedBid(bid *order.Bid,
	unitsFilled orderT.SupplyUnit) *clmrpc.MatchedBid {

	return &clmrpc.MatchedBid{
		Bid: &clmrpc.ServerBid{
			Details:           marshallServerOrder(bid),
			MinDurationBlocks: bid.MinDuration(),
			Version:           uint32(bid.Version),
		},
		UnitsFilled: uint32(unitsFilled),
	}
}

// marshallServerOrder translates an order.ServerOrder to its RPC counterpart.
func marshallServerOrder(order order.ServerOrder) *clmrpc.ServerOrder {
	nonce := order.Nonce()

	return &clmrpc.ServerOrder{
		TraderKey:   order.Details().AcctKey[:],
		RateFixed:   order.Details().FixedRate,
		Amt:         uint64(order.Details().Amt),
		OrderNonce:  nonce[:],
		OrderSig:    order.ServerDetails().Sig.ToSignatureBytes(),
		MultiSigKey: order.ServerDetails().MultiSigKey[:],
		NodePub:     order.ServerDetails().NodeKey[:],
		NodeAddr:    marshallNodeAddrs(order.ServerDetails().NodeAddrs),
		// TODO: ChanType should be an enum in RPC?
		ChanType:               uint32(order.ServerDetails().ChanType),
		FundingFeeRateSatPerKw: uint64(order.Details().FundingFeeRate),
	}
}

// marshallNodeAddrs tranlates a []net.Addr to its RPC counterpart.
func marshallNodeAddrs(addrs []net.Addr) []*clmrpc.NodeAddress {
	res := make([]*clmrpc.NodeAddress, 0, len(addrs))
	for _, addr := range addrs {
		res = append(res, &clmrpc.NodeAddress{
			Network: addr.Network(),
			Addr:    addr.String(),
		})
	}
	return res
}

// parseRPCServerOrder parses the incoming raw RPC server order into the go
// native data types used in the order struct.
func parseRPCServerOrder(version uint32,
	details *clmrpc.ServerOrder) (*orderT.Kit, [33]byte, []net.Addr,
	[33]byte, error) {

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
	kit.FundingFeeRate = chainfee.SatPerKWeight(
		details.FundingFeeRateSatPerKw,
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
	if len(details.NodeAddr) == 0 {
		return nil, nodeKey, nodeAddrs, multiSigKey,
			fmt.Errorf("invalid node addresses")
	}
	for _, rpcAddr := range details.NodeAddr {
		addr, err := net.ResolveTCPAddr(rpcAddr.Network, rpcAddr.Addr)
		if err != nil {
			return nil, nodeKey, nodeAddrs, multiSigKey,
				fmt.Errorf("unable to parse node ddr: %v", err)
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
