package subasta

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"sync"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/llm/clmrpc"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/ticker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type auctioneerWallet struct {
	lndclient.LightningClient
	lndclient.WalletKitClient
}

// A compile-time assertion to ensure auctioneerWallet meets the Wallet
// interface.
var _ Wallet = (*auctioneerWallet)(nil)

// auctioneerStore is a simple wrapper around the main database that maintains
// an in-memory atomically modified auction state.
type auctioneerStore struct {
	// state is the current auctioneer state.
	//
	// NOTE: This MUST be used atomically
	state uint32

	*subastadb.EtcdStore
}

// UpdateAuctionState updates the current state of the auction.
//
// NOTE: This state doesn't need to be persisted, but it should be
// durable during the lifetime of this interface. This method is use
// mainly to make testing state transition in the auction easier.
func (a *auctioneerStore) UpdateAuctionState(newState AuctionState) error {
	atomic.StoreUint32(&a.state, uint32(newState))
	return nil
}

// AuctionState returns the current state of the auction. If no state
// modification have been made, then this method should return the default
// state.
//
// NOTE: This state doesn't need to be persisted. This method is use
// mainly to make testing state transition in the auction easier.
func (a *auctioneerStore) AuctionState() (AuctionState, error) {
	return AuctionState(atomic.LoadUint32(&a.state)), nil
}

var _ AuctioneerDatabase = (*auctioneerStore)(nil)

// executorStore is a wrapper around the normal database to implement the
// ExecutorStore interface. This only exposes some new methods to update and
// read the in-memory execution state.
type executorStore struct {
	// state is the current batch execution state.
	//
	// NOTE: This MUST be used atomically
	state uint32

	subastadb.Store
}

// ExecutionState returns the current execution state.
func (e *executorStore) ExecutionState() (venue.ExecutionState, error) {
	return venue.ExecutionState(atomic.LoadUint32(&e.state)), nil
}

// UpdateExecutionState updates the current execution state.
func (e *executorStore) UpdateExecutionState(newState venue.ExecutionState) error {
	atomic.StoreUint32(&e.state, uint32(newState))
	return nil
}

var _ venue.ExecutorStore = (*executorStore)(nil)

// Server is the main auction auctioneer server.
type Server struct {
	rpcServer   *rpcServer
	adminServer *adminRPCServer

	lnd            *lndclient.GrpcLndServices
	identityPubkey [33]byte

	store subastadb.Store

	accountManager *account.Manager

	orderBook *order.Book

	batchExecutor *venue.BatchExecutor

	auctioneer *Auctioneer

	channelEnforcer *chanenforcement.ChannelEnforcer

	quit chan struct{}

	wg sync.WaitGroup

	startOnce sync.Once
	stopOnce  sync.Once
}

// NewServer returns a new auctioneer server that is started in daemon mode,
// listens for gRPC connections and executes commands.
func NewServer(cfg *Config) (*Server, error) {
	// First, we'll set up our logging infrastructure so all operations
	// below will properly be logged.
	if err := initLogging(cfg); err != nil {
		return nil, fmt.Errorf("unable to init logging: %w", err)
	}

	// With our logging set up, we'll now establish our initial connection
	// to the backing lnd instance.
	lnd, err := lndclient.NewLndServices(&lndclient.LndServicesConfig{
		LndAddress:  cfg.Lnd.Host,
		Network:     cfg.Network,
		MacaroonDir: cfg.Lnd.MacaroonDir,
		TLSPath:     cfg.Lnd.TLSPath,
	})
	if err != nil {
		return nil, err
	}

	// Next, we'll open our primary connection to the main backing
	// database.
	store, err := subastadb.NewEtcdStore(
		*lnd.ChainParams, cfg.Etcd.Host, cfg.Etcd.User,
		cfg.Etcd.Password,
	)
	if err != nil {
		return nil, err
	}

	// With our database open, we can set up the manager which watches over
	// all the trader accounts.
	accountManager, err := account.NewManager(&account.ManagerConfig{
		Store:         store,
		Wallet:        lnd.WalletKit,
		Signer:        lnd.Signer,
		ChainNotifier: lnd.ChainNotifier,
		MaxAcctValue:  btcutil.Amount(cfg.MaxAcctValue),
	})
	if err != nil {
		return nil, err
	}

	// Instantiate our fee schedule now that will be used by different parts
	// during the batch execution.
	feeSchedule := orderT.NewLinearFeeSchedule(
		btcutil.Amount(cfg.ExecFeeBase), btcutil.Amount(cfg.ExecFeeRate),
	)

	// Continuing, we create the batch executor which will communicate
	// between the trader's an auctioneer for each batch epoch.
	exeStore := &executorStore{
		Store: store,
	}
	batchExecutor := venue.NewBatchExecutor(
		exeStore, lnd.Signer, defaultMsgTimeout,
		venue.NewExeBatchStorer(store),
	)

	orderBook := order.NewBook(&order.BookConfig{
		Store:     store,
		Signer:    lnd.Signer,
		SubmitFee: btcutil.Amount(cfg.OrderSubmitFee),
	})
	server := &Server{
		lnd:            lnd,
		store:          store,
		accountManager: accountManager,
		orderBook:      orderBook,
		batchExecutor:  batchExecutor,
		auctioneer: NewAuctioneer(AuctioneerConfig{
			DB: &auctioneerStore{
				EtcdStore: store,
			},
			ChainNotifier: lnd.ChainNotifier,
			Wallet: &auctioneerWallet{
				WalletKitClient: lnd.WalletKit,
				LightningClient: lnd.Client,
			},
			StartingAcctValue: 1_000_000,
			BatchTicker:       ticker.NewForce(defaultBatchTickInterval),
			CallMarket: matching.NewUniformPriceCallMarket(
				&matching.LastAcceptedBid{}, feeSchedule,
				func(acctID matching.AccountID) (*account.Account, error) {
					acctKey, err := btcec.ParsePubKey(acctID[:], btcec.S256())
					if err != nil {
						return nil, err
					}

					return store.Account(
						context.Background(), acctKey, false,
					)
				},
			),
			OrderFeed:     orderBook,
			BatchExecutor: batchExecutor,
			FeeSchedule:   feeSchedule,
		}),
		channelEnforcer: chanenforcement.New(&chanenforcement.Config{
			ChainNotifier: lnd.ChainNotifier,
			PackageSource: store,
		}),
		quit: make(chan struct{}),
	}

	// With all our other initialization complete, we'll now create the
	// main RPC server.
	//
	// First, we'll set up the series of interceptors for our gRPC server
	// which we'll initialize shortly below.
	var interceptor ServerInterceptor = &auth.ServerInterceptor{}
	if cfg.FakeAuth && cfg.Network == "mainnet" {
		return nil, fmt.Errorf("cannot use fake LSAT auth for mainnet")
	}
	if cfg.FakeAuth {
		interceptor = &regtestInterceptor{}
	}
	serverOpts := []grpc.ServerOption{
		grpc.UnaryInterceptor(interceptor.UnaryInterceptor),
		grpc.StreamInterceptor(interceptor.StreamInterceptor),
	}
	certOpts, err := extractCertOpt(cfg)
	if err != nil {
		return nil, err
	}
	if certOpts != nil {
		serverOpts = append(serverOpts, certOpts)
	}

	// Next, create our listener, and initialize the primary gRPc server
	// for HTTP/2 connections.
	log.Infof("Starting gRPC listener")
	grpcListener := cfg.RPCListener
	if grpcListener == nil {
		grpcListener, err = net.Listen("tcp", defaultAuctioneerAddr)
		if err != nil {
			return nil, fmt.Errorf("RPC server unable to listen "+
				"on %s", defaultAuctioneerAddr)
		}
	}
	auctioneerServer := newRPCServer(
		store, lnd, accountManager, server.auctioneer.BestHeight,
		server.orderBook, batchExecutor, feeSchedule, grpcListener,
		serverOpts, cfg.SubscribeTimeout,
	)
	server.rpcServer = auctioneerServer

	clmrpc.RegisterChannelAuctioneerServer(
		auctioneerServer.grpcServer, auctioneerServer,
	)

	// Finally, create our admin RPC that is by default only exposed on the
	// local loopback interface.
	log.Infof("Starting admin gRPC listener")
	adminListener := cfg.AdminRPCListener
	if adminListener == nil {
		adminListener, err = net.Listen("tcp", defaultAdminAddr)
		if err != nil {
			return nil, fmt.Errorf("admin RPC server unable to "+
				"listen on %s", defaultAdminAddr)
		}
	}
	server.adminServer = newAdminRPCServer(
		auctioneerServer, adminListener, []grpc.ServerOption{},
		server.auctioneer,
	)
	adminrpc.RegisterAuctionAdminServer(
		server.adminServer.grpcServer, server.adminServer,
	)

	return server, nil
}

// Start attempts to start the auctioneer server which includes the RPC server
// and the main auctioneer state machine loop.
func (s *Server) Start() error {
	var startErr error

	s.startOnce.Do(func() {
		log.Infof("Starting primary server")

		ctx := context.Background()
		etcdCtx, etcdCancel := context.WithTimeout(ctx, initTimeout)
		defer etcdCancel()
		if err := s.store.Init(etcdCtx); err != nil {
			startErr = fmt.Errorf("unable to initialize etcd "+
				"store: %v", err)
			return
		}

		lndCtx, lndCancel := context.WithTimeout(ctx, getInfoTimeout)
		defer lndCancel()
		infoResp, err := s.lnd.Client.GetInfo(lndCtx)
		if err != nil {
			startErr = fmt.Errorf("unable to retrieve lnd node "+
				"public key: %v", err)
			return
		}
		s.identityPubkey = infoResp.IdentityPubkey

		// Start managers.
		if err := s.accountManager.Start(); err != nil {
			startErr = fmt.Errorf("unable to start account "+
				"manager: %v", err)
			return
		}
		if err := s.orderBook.Start(); err != nil {
			startErr = fmt.Errorf("unable to start order "+
				"manager: %v", err)
			return
		}
		if err := s.auctioneer.Start(); err != nil {
			startErr = fmt.Errorf("unable to start auctioneer "+
				"executor: %v", err)
			return
		}
		if err := s.batchExecutor.Start(); err != nil {
			startErr = fmt.Errorf("unable to start batch "+
				"executor: %v", err)
			return
		}
		if err := s.channelEnforcer.Start(); err != nil {
			startErr = fmt.Errorf("unable to start channel "+
				"enforcer: %v", err)
			return
		}

		// Start the gRPC server itself.
		err = s.rpcServer.Start()
		if err != nil {
			startErr = fmt.Errorf("unable to start auction "+
				"server: %w", err)
			return
		}

		// And finally the admin RPC server.
		err = s.adminServer.Start()
		if err != nil {
			startErr = fmt.Errorf("unable to start admin "+
				"server: %w", err)
			return
		}
	})

	return startErr
}

// Stop shuts down the server, including all client connections and network
// listeners.
func (s *Server) Stop() error {
	log.Info("Received shutdown signal, stopping server")

	var stopErr error

	s.stopOnce.Do(func() {
		close(s.quit)

		s.adminServer.Stop()
		s.rpcServer.Stop()

		s.channelEnforcer.Stop()
		if err := s.batchExecutor.Stop(); err != nil {
			stopErr = fmt.Errorf("unable to stop batch executor: "+
				"%w", err)
			return
		}
		if err := s.auctioneer.Stop(); err != nil {
			stopErr = fmt.Errorf("unable to stop auctioneer: %w",
				err)
			return
		}
		s.orderBook.Stop()
		s.accountManager.Stop()
		s.lnd.Close()
		s.wg.Wait()
	})

	return stopErr
}

// newRegtestInterceptor creates an LSAT interceptor that reads the dummy LSAT
// ID added by the client's counterpart and uses that as the client's main
// identification. As its name suggests, this should only be used for testing on
// local regtest networks.
type ServerInterceptor interface {
	// UnaryInterceptor intercepts normal, non-streaming requests from the
	// client to the server.
	UnaryInterceptor(context.Context, interface{}, *grpc.UnaryServerInfo,
		grpc.UnaryHandler) (resp interface{}, err error)

	// StreamInterceptor intercepts streaming requests from the client to
	// the server.
	StreamInterceptor(interface{}, grpc.ServerStream,
		*grpc.StreamServerInfo, grpc.StreamHandler) error
}

// wrappedStream is a helper struct that allows to overwrite the context of a
// gRPC stream.
type wrappedStream struct {
	grpc.ServerStream
	WrappedContext context.Context
}

// Context returns the overwritten context of the stream.
func (w *wrappedStream) Context() context.Context {
	return w.WrappedContext
}

// regtestInterceptor is a dummy gRPC interceptor that can be used on regtest to
// simulate identification through LSAT.
type regtestInterceptor struct{}

// UnaryInterceptor intercepts non-streaming requests and reads the dummy LSAT
// ID.
func (i *regtestInterceptor) UnaryInterceptor(ctx context.Context,
	req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (
	resp interface{}, err error) {

	id, err := idFromContext(ctx)
	if err != nil {
		log.Debugf("No ID extracted, error was: %v", err)
		return handler(ctx, req)
	}
	idCtx := auth.AddToContext(ctx, auth.KeyTokenID, *id)
	return handler(idCtx, req)
}

// StreamingInterceptor intercepts streaming requests and reads the dummy LSAT
// ID.
func (i *regtestInterceptor) StreamInterceptor(srv interface{},
	ss grpc.ServerStream, _ *grpc.StreamServerInfo,
	handler grpc.StreamHandler) error {

	ctx := ss.Context()
	id, err := idFromContext(ctx)
	if err != nil {
		log.Debugf("No ID extracted, error was: %v", err)
		return handler(srv, ss)
	}

	idCtx := auth.AddToContext(ctx, auth.KeyTokenID, *id)
	wrappedStream := &wrappedStream{ss, idCtx}
	return handler(srv, wrappedStream)
}

// idFromContext extracts the dummy ID specified in the gRPC metadata. The MD
// field looks like this:
//   Authorization: LSATID <hex>
func idFromContext(ctx context.Context) (*lsat.TokenID, error) {
	dummyRex := regexp.MustCompile("LSATID ([a-f0-9]{64})")
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, fmt.Errorf("context contains no metadata")
	}
	authHeader := md.Get(auth.HeaderAuthorization)[0]
	log.Debugf("Auth header present in request: %s", authHeader)
	if !dummyRex.MatchString(authHeader) {
		log.Debugf("Auth header didn't match dummy ID")
		return nil, nil
	}
	matches := dummyRex.FindStringSubmatch(authHeader)
	idHex, err := hex.DecodeString(matches[1])
	if err != nil {
		return nil, err
	}

	var clientID lsat.TokenID
	copy(clientID[:], idHex)
	log.Debugf("Decoded client/token ID %s from auth header",
		clientID.String())
	return &clientID, nil
}
