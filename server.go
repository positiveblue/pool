package subasta

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"sync"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcutil"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightninglabs/subasta/chain"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/ratings"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/matching"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

const (
	// headerRESTProxyAccept is the name of a gRPC metadata field that is
	// set only if the request was forwarded by the REST proxy.
	headerRESTProxyAccept = "grpcgateway-accept"
)

var (
	// lsatTokenREST is a dummy LSAT token ID that is set if a request is
	// made by the REST proxy (which might be on the LSAT whitelist in
	// aperture and therefore might not have a real token) and no token can
	// be found.
	lsatTokenREST = lsat.TokenID{
		// This is hex for the string "restproxy".
		0x72, 0x65, 0x73, 0x74, 0x70, 0x72, 0x6f, 0x78, 0x79,
	}
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
	state    AuctionState
	stateMtx sync.Mutex

	*subastadb.EtcdStore
}

func newAuctioneerStore(db *subastadb.EtcdStore) *auctioneerStore {
	return &auctioneerStore{
		state:     DefaultState{},
		EtcdStore: db,
	}
}

// UpdateAuctionState updates the current state of the auction.
//
// NOTE: This state doesn't need to be persisted, but it should be
// durable during the lifetime of this interface. This method is use
// mainly to make testing state transition in the auction easier.
func (a *auctioneerStore) UpdateAuctionState(newState AuctionState) error {
	a.stateMtx.Lock()
	a.state = newState
	a.stateMtx.Unlock()
	return nil
}

// AuctionState returns the current state of the auction. If no state
// modification have been made, then this method should return the default
// state.
//
// NOTE: This state doesn't need to be persisted. This method is use
// mainly to make testing state transition in the auction easier.
func (a *auctioneerStore) AuctionState() (AuctionState, error) {
	a.stateMtx.Lock()
	state := a.state
	a.stateMtx.Unlock()

	return state, nil
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

type activeTradersMap struct {
	activeTraders map[matching.AccountID]*venue.ActiveTrader
	sync.RWMutex
}

// RegisterTrader registers a new trader as being active. An active traders is
// eligible to join execution of a batch that they're a part of.
func (a *activeTradersMap) RegisterTrader(t *venue.ActiveTrader) error {
	a.Lock()
	defer a.Unlock()

	_, ok := a.activeTraders[t.AccountKey]
	if ok {
		return fmt.Errorf("trader %x already registered",
			t.AccountKey)
	}
	a.activeTraders[t.AccountKey] = t

	log.Infof("Registering new trader: %x", t.AccountKey[:])

	return nil
}

// UnregisterTrader removes a registered trader from the batch.
func (a *activeTradersMap) UnregisterTrader(t *venue.ActiveTrader) error {
	a.Lock()
	defer a.Unlock()

	delete(a.activeTraders, t.AccountKey)

	log.Infof("Disconnecting trader: %x", t.AccountKey[:])
	return nil
}

// IsActive returns true if the given key is among the active traders.
func (a *activeTradersMap) IsActive(acctKey [33]byte) bool {
	a.RLock()
	defer a.RUnlock()

	_, ok := a.activeTraders[acctKey]
	return ok
}

// GetTrades returns the current set of active traders.
func (a *activeTradersMap) GetTraders() map[matching.AccountID]*venue.ActiveTrader {
	a.RLock()
	defer a.RUnlock()

	c := make(map[matching.AccountID]*venue.ActiveTrader, len(a.activeTraders))

	for k, v := range a.activeTraders {
		c[k] = v
	}

	return c
}

var _ venue.ExecutorStore = (*executorStore)(nil)

// Server is the main auction auctioneer server.
type Server struct {
	rpcServer   *rpcServer
	adminServer *adminRPCServer

	lnd            *lndclient.GrpcLndServices
	identityPubkey [33]byte

	cfg *Config

	store subastadb.Store

	accountManager *account.Manager

	orderBook *order.Book

	batchExecutor *venue.BatchExecutor

	// activeTraders is a map of all the current active traders. An active
	// trader is one that's online and has a live communication channel
	// with the BatchExecutor.
	activeTraders *activeTradersMap

	auctioneer *Auctioneer

	channelEnforcer *chanenforcement.ChannelEnforcer

	ratingsDB ratings.NodeRatingsDatabase

	durationBuckets *order.DurationBuckets

	quit chan struct{}

	wg sync.WaitGroup

	startOnce sync.Once
	stopOnce  sync.Once
}

// NewServer returns a new auctioneer server that is started in daemon mode,
// listens for gRPC connections and executes commands.
func NewServer(cfg *Config) (*Server, error) {
	ctx := context.Background()

	// First, we'll set up our logging infrastructure so all operations
	// below will properly be logged.
	if err := initLogging(cfg); err != nil {
		return nil, fmt.Errorf("unable to init logging: %w", err)
	}

	// Print the version before we do any more set up to ensure we output
	// it.
	log.Infof("Version: %v", Version())

	// With our logging set up, we'll now establish our initial connection
	// to the backing lnd instance.
	network := lndclient.Network(cfg.Network)
	lnd, err := lndclient.NewLndServices(&lndclient.LndServicesConfig{
		LndAddress:  cfg.Lnd.Host,
		Network:     network,
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

	// Instantiate our fee schedule and other terms now that will be used
	// by different parts during the batch execution.
	auctionTerms := &terms.AuctioneerTerms{
		MaxAccountValue:  btcutil.Amount(cfg.MaxAcctValue),
		OrderExecBaseFee: btcutil.Amount(cfg.ExecFeeBase),
		OrderExecFeeRate: btcutil.Amount(cfg.ExecFeeRate),
	}

	// We also need to keep some shared state between the auctioneer/match
	// maker and the executor. Partial rejects from the trader need to be
	// taken into account for the next match making attempt.
	fundingConflicts := matching.NewNodeConflictPredicate()
	traderRejected := matching.NewNodeConflictPredicate()

	// Continuing, we create the batch executor which will communicate
	// between the trader's an auctioneer for each batch epoch.
	exeStore := &executorStore{
		Store: store,
	}
	activeTraders := &activeTradersMap{
		activeTraders: make(map[matching.AccountID]*venue.ActiveTrader),
	}
	batchExecutor := venue.NewBatchExecutor(&venue.ExecutorConfig{
		Store:            exeStore,
		Signer:           lnd.Signer,
		BatchStorer:      venue.NewExeBatchStorer(store),
		AccountWatcher:   accountManager,
		TraderMsgTimeout: defaultMsgTimeout,
		ActiveTraders:    activeTraders.GetTraders,
	})

	durationBuckets := order.NewDurationBuckets()
	orderBook := order.NewBook(&order.BookConfig{
		Store:           store,
		Signer:          lnd.Signer,
		DurationBuckets: durationBuckets,
	})

	channelEnforcer := chanenforcement.New(&chanenforcement.Config{
		ChainNotifier: lnd.ChainNotifier,
		PackageSource: store,
	})

	if cfg.BatchConfTarget < 1 {
		return nil, fmt.Errorf("conf target must be greater than 0")
	}

	var (
		ratingsAgency ratings.Agency
		ratingsDB     ratings.NodeRatingsDatabase
	)

	// We'll always use an in-memory ratings DB that writes through to the
	// etcd store.
	log.Infof("Initializing in-memory RatingsAgency")
	nodeRatings, err := store.NodeRatings(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve stored node "+
			"ratings: %v", err)
	}
	ratingsDB = ratings.NewMemRatingsDatabase(store, nodeRatings)
	ratingsAgency = ratings.NewNodeTierAgency(ratingsDB)

	// We'll only activate the BOS score backed ratings agency if it has
	// been flipped on in the config. In contexts like testnet or regtest,
	// we don't have an instance of bos scores to point to but we can still
	// manually edit the ratings through the admin RPC.
	if cfg.NodeRatingsActive && cfg.BosScoreWebURL != "" {
		log.Infof("Initializing BosScore backed RatingsAgency")

		bosScoreWebScore := &ratings.BosScoreWebRatings{
			URL: cfg.BosScoreWebURL,
		}
		ratingsDB = ratings.NewBosScoreRatingsDatabase(
			bosScoreWebScore, cfg.NodeRatingsRefreshInterval,
			ratingsDB,
		)

		ratingsAgency = ratings.NewNodeTierAgency(ratingsDB)
	}

	server := &Server{
		cfg:            cfg,
		lnd:            lnd,
		store:          store,
		accountManager: accountManager,
		orderBook:      orderBook,
		batchExecutor:  batchExecutor,
		activeTraders:  activeTraders,
		auctioneer: NewAuctioneer(AuctioneerConfig{
			DB:            newAuctioneerStore(store),
			ChainNotifier: lnd.ChainNotifier,
			Wallet: &auctioneerWallet{
				WalletKitClient: lnd.WalletKit,
				LightningClient: lnd.Client,
			},
			StartingAcctValue: 1_000_000,
			BatchTicker: NewIntervalAwareForceTicker(
				defaultBatchTickInterval,
			),
			CallMarket: matching.NewUniformPriceCallMarket(
				&matching.LastAcceptedBid{},
				auctionTerms.FeeSchedule(), durationBuckets,
			),
			OrderFeed:           orderBook,
			BatchExecutor:       batchExecutor,
			FeeSchedule:         auctionTerms.FeeSchedule(),
			ChannelEnforcer:     channelEnforcer,
			ConfTarget:          cfg.BatchConfTarget,
			AccountExpiryOffset: cfg.AccountExpiryOffset,
			AccountFetcher: func(acctID matching.AccountID) (
				*account.Account, error) {

				acctKey, err := btcec.ParsePubKey(
					acctID[:], btcec.S256(),
				)
				if err != nil {
					return nil, err
				}

				// We retrieve the pending diff of the account,
				// if any, to ensure matchmaking can determine
				// whether it is ready to participate in a
				// batch.
				return store.Account(
					context.Background(), acctKey, true,
				)
			},
			FundingConflicts:              fundingConflicts,
			FundingConflictsResetInterval: cfg.FundingConflictResetInterval,
			TraderRejected:                traderRejected,
			TraderRejectResetInterval:     cfg.TraderRejectResetInterval,
			TraderOnline:                  matching.NewTraderOnlineFilter(activeTraders.IsActive),
			RatingsAgency:                 ratingsAgency,
		}),
		channelEnforcer: channelEnforcer,
		ratingsDB:       ratingsDB,
		durationBuckets: durationBuckets,
		quit:            make(chan struct{}),
	}

	// With all our other initialization complete, we'll now create the
	// main RPC server.
	//
	// First, we'll set up the series of interceptors for our gRPC server
	// which we'll initialize shortly below.
	var interceptor ServerInterceptor = &lsat.ServerInterceptor{}
	if cfg.FakeAuth && cfg.Network == "mainnet" {
		return nil, fmt.Errorf("cannot use fake LSAT auth for mainnet")
	}
	if cfg.FakeAuth {
		interceptor = &regtestInterceptor{}
	}
	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			interceptor.UnaryInterceptor,
			errorLogUnaryServerInterceptor(rpcLog),
		),
		grpc.ChainStreamInterceptor(
			interceptor.StreamInterceptor,
			errorLogStreamServerInterceptor(rpcLog),
		),
	}

	// Prometheus itself needs a gRPC interceptor to measure performance of
	// the API calls. We chain them together with the LSAT interceptor.
	if cfg.Prometheus.Active {
		cfg.Prometheus.Store = store
		cfg.Prometheus.Lnd = lnd.LndServices
		cfg.Prometheus.FundingConflicts = fundingConflicts

		serverOpts = []grpc.ServerOption{
			grpc.ChainUnaryInterceptor(
				interceptor.UnaryInterceptor,
				errorLogUnaryServerInterceptor(rpcLog),
				grpc_prometheus.UnaryServerInterceptor,
			),
			grpc.ChainStreamInterceptor(
				interceptor.StreamInterceptor,
				errorLogStreamServerInterceptor(rpcLog),
				grpc_prometheus.StreamServerInterceptor,
			),
		}
	}

	// Append TLS configuration to server options.
	serverTLS, clientCertOpt, err := getTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	serverOpts = append(
		serverOpts, grpc.Creds(credentials.NewTLS(serverTLS)),
	)

	// Next, create our listeners, and initialize the primary gRPC and
	// REST proxy server for HTTP/2 connections.
	log.Info("Starting gRPC listener")
	grpcListener := cfg.RPCListener
	if grpcListener == nil {
		grpcListener, err = net.Listen("tcp", cfg.RPCListen)
		if err != nil {
			return nil, fmt.Errorf("RPC server unable to listen "+
				"on %s: %v", cfg.RPCListen, err)
		}
	}
	log.Info("Starting REST listener")
	restListener, err := net.Listen("tcp", cfg.RESTListen)
	if err != nil {
		return nil, fmt.Errorf("REST proxy unable to listen on %s: %v",
			cfg.RESTListen, err)
	}
	restListener = tls.NewListener(restListener, serverTLS)

	auctioneerServer := newRPCServer(
		store, lnd.Signer, accountManager, server.auctioneer.BestHeight,
		server.orderBook, batchExecutor, server.auctioneer,
		auctionTerms, ratingsAgency, ratingsDB, grpcListener,
		restListener, serverOpts, clientCertOpt, cfg.SubscribeTimeout,
		activeTraders,
	)
	server.rpcServer = auctioneerServer
	cfg.Prometheus.PublicRPCServer = auctioneerServer.grpcServer
	cfg.Prometheus.NumActiveTraders = func() int {
		auctioneerServer.connectedStreamsMutex.Lock()
		numTraders := len(auctioneerServer.connectedStreams)
		defer auctioneerServer.connectedStreamsMutex.Unlock()
		return numTraders
	}
	cfg.Prometheus.BatchConfTarget = cfg.BatchConfTarget
	cfg.Prometheus.SnapshotSource = auctioneerServer.lookupSnapshot

	auctioneerrpc.RegisterChannelAuctioneerServer(
		auctioneerServer.grpcServer, auctioneerServer,
	)

	// Finally, create our admin RPC that is by default only exposed on the
	// local loopback interface.
	log.Infof("Starting admin gRPC listener")
	adminListener := cfg.AdminRPCListener
	if adminListener == nil {
		adminListener, err = net.Listen("tcp", cfg.AdminRPCListen)
		if err != nil {
			return nil, fmt.Errorf("admin RPC server unable to "+
				"listen on %s", cfg.AdminRPCListen)
		}
	}

	var adminServerOpts []grpc.ServerOption
	if cfg.Prometheus.Active {
		adminServerOpts = []grpc.ServerOption{
			grpc.ChainUnaryInterceptor(
				grpc_prometheus.UnaryServerInterceptor,
			),
			grpc.ChainStreamInterceptor(
				grpc_prometheus.StreamServerInterceptor,
			),
		}
	}

	chainParams, err := network.ChainParams()
	if err != nil {
		return nil, err
	}

	server.adminServer, err = newAdminRPCServer(
		chainParams, auctioneerServer, adminListener, adminServerOpts,
		server.auctioneer, store, durationBuckets, lnd.WalletKit,
	)
	if err != nil {
		return nil, err
	}

	cfg.Prometheus.AdminRPCServer = server.adminServer.grpcServer

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

		// Load the currently stored lease duration buckets. If this is
		// the first time we start with the lease durations code, the
		// above Init will have added the default bucket.
		buckets, err := s.store.LeaseDurations(ctx)
		if err != nil {
			startErr = fmt.Errorf("unable to load lease duration "+
				"buckets: %v", err)
			return
		}
		for duration, marketState := range buckets {
			s.durationBuckets.PutMarket(duration, marketState)
		}

		if s.ratingsDB != nil {
			// Now that the DB has been initialized, we'll actually
			// index the set of ratings.
			err := s.ratingsDB.IndexRatings(ctx)
			if err != nil {
				startErr = fmt.Errorf("unable to index ratings: %v",
					err)
			}
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

		// Start the prometheus exporter if activated in the config.
		if s.cfg.Prometheus.Active {
			// Now let's open a persistent connection to the chain backend
			// that we use to query transactions.
			s.cfg.Prometheus.BitcoinClient, err = chain.NewClient(
				s.cfg.Bitcoin,
			)
			if err != nil {
				startErr = fmt.Errorf("unable to start chain "+
					"client: %v", err)
				return
			}

			promClient := monitoring.NewPrometheusExporter(
				s.cfg.Prometheus,
			)
			log.Infof("Starting Prometheus exporter: @%v",
				s.cfg.Prometheus.ListenAddr)
			if err := promClient.Start(); err != nil {
				startErr = fmt.Errorf("unable to start "+
					"Prometheus exporter: %v", err)
				return
			}
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

		if s.cfg.Prometheus.Active {
			s.cfg.Prometheus.BitcoinClient.Shutdown()
		}
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
	idCtx := lsat.AddToContext(ctx, lsat.KeyTokenID, *id)
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

	idCtx := lsat.AddToContext(ctx, lsat.KeyTokenID, *id)
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

	// If this is a request coming from the REST proxy, it might not have
	// a real token attached since we might decide to put those requests on
	// the whitelist in aperture. We return the dummy ID in that case so we
	// can still kind of identify those non-authenticated requests.
	authHeader := md.Get(lsat.HeaderAuthorization)
	restProxyHeader := md.Get(headerRESTProxyAccept)
	if len(authHeader) == 0 && len(restProxyHeader) > 0 {
		log.Debugf("Got REST proxy request with no token")
		return &lsatTokenREST, nil
	} else if len(authHeader) == 0 {
		return nil, fmt.Errorf("request contains no auth header")
	}

	log.Debugf("Auth header present in request: %s", authHeader[0])
	if !dummyRex.MatchString(authHeader[0]) {
		log.Debugf("Auth header didn't match dummy ID")
		return nil, nil
	}
	matches := dummyRex.FindStringSubmatch(authHeader[0])
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
