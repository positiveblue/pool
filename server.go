package subasta

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightninglabs/subasta/ban"
	"github.com/lightninglabs/subasta/chain"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/ratings"
	"github.com/lightninglabs/subasta/status"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/traderterms"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/cluster"
	"github.com/lightningnetwork/lnd/kvdb/etcd"
	"github.com/lightningnetwork/lnd/lncfg"
	"github.com/lightningnetwork/lnd/lnrpc/verrpc"
	"github.com/lightningnetwork/lnd/signal"
	"go.etcd.io/etcd/server/v3/embed"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

const (
	// headerRESTProxyAccept is the name of a gRPC metadata field that is
	// set only if the request was forwarded by the REST proxy.
	headerRESTProxyAccept = "grpcgateway-accept"

	// ShutdownCompleteLogMessage is the log message that is printed once
	// the server has fully and completely finished its shutdown process.
	ShutdownCompleteLogMessage = "full server shutdown complete"
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

	// minimalCompatibleVersion is the minimum version and build tags
	// required in lnd to run subasta.
	minimalCompatibleVersion = &verrpc.Version{
		AppMajor: 0,
		AppMinor: 13,
		AppPatch: 0,

		// We don't actually require the invoicesrpc calls. But if we
		// try to use lndclient on an lnd that doesn't have it enabled,
		// the library will try to load the invoices.macaroon anyway and
		// fail. So until that bug is fixed in lndclient, we require the
		// build tag to be active.
		BuildTags: []string{
			"signrpc", "walletrpc", "chainrpc", "invoicesrpc",
		},
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

	subastadb.Store
}

func newAuctioneerStore(db subastadb.Store) *auctioneerStore {
	return &auctioneerStore{
		state: DefaultState{},
		Store: db,
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
	store              traderterms.Store
	activeTraders      map[matching.AccountID]*venue.ActiveTrader
	termsCache         map[lsat.TokenID]*traderterms.Custom
	defaultFeeSchedule terms.FeeSchedule
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

// GetTraders returns the current set of active traders.
func (a *activeTradersMap) GetTraders() map[matching.AccountID]*venue.ActiveTrader {
	a.RLock()
	defer a.RUnlock()

	c := make(map[matching.AccountID]*venue.ActiveTrader, len(a.activeTraders))

	for k, v := range a.activeTraders {
		c[k] = v
	}

	return c
}

// GetTrader returns the ActiveTrader information if it exists.
func (a *activeTradersMap) GetTrader(id matching.AccountID) *venue.ActiveTrader {
	a.RLock()
	defer a.RUnlock()

	if trader, ok := a.activeTraders[id]; ok {
		return trader
	}

	return nil
}

// AccountFeeSchedule returns the fee schedule for a specific account.
func (a *activeTradersMap) AccountFeeSchedule(
	acctKey [33]byte) terms.FeeSchedule {

	// Can't use the defer pattern here because TraderFeeSchedule also tries
	// to acquire the lock.
	a.RLock()
	trader, ok := a.activeTraders[acctKey]
	a.RUnlock()

	if !ok {
		return a.defaultFeeSchedule
	}

	return a.TraderFeeSchedule(trader.TokenID)
}

// TraderFeeSchedule returns the fee schedule for a trader identified by
// their LSAT ID.
func (a *activeTradersMap) TraderFeeSchedule(id [32]byte) terms.FeeSchedule {
	a.Lock()
	defer a.Unlock()

	// Anything in the cache for this trader? Can use that directly, we
	// know the cache is invalidated on terms mutation.
	t, ok := a.termsCache[id]
	if !ok {
		// Need to fetch the latest terms from the DB so we can populate
		// the cache.
		var err error
		t, err = a.store.GetTraderTerms(context.Background(), id)
		if err != nil {
			// If there is no record in the store, we create an
			// empty one that will fall back to the default schedule
			// because no custom terms are set on it. That way we
			// don't need to query the store for this trader again.
			t = &traderterms.Custom{
				TraderID: id,
			}
		}

		a.termsCache[id] = t
	}

	return t.FeeSchedule(a.defaultFeeSchedule)
}

// invalidateCache removes the cache entry for a given trader if it exists.
func (a *activeTradersMap) invalidateCache(id [32]byte) {
	a.Lock()
	defer a.Unlock()

	delete(a.termsCache, id)
}

var _ venue.ExecutorStore = (*executorStore)(nil)

// waitUntilLeader will create a leader elector (if needed) and block until
// this instance is elected as a leader or the interceptor receives a shutdown
// message.
//
// Note: If we join a cluster with a leader instance running, we will block
// until we are selected. If the leader shuts down properly, that means calling
// server.Stop() another instance will be selected "instantly". On the other
// hand, tha instance just panics, etcd will wait until the connection times up
// before selecting the next leader(currently ~60s).
func waitUntilLeader(ctx context.Context, cfg *Config,
	interceptor signal.Interceptor) (cluster.LeaderElector, error) {

	// Play safe.
	if !cfg.Cluster.EnableLeaderElection {
		return nil, nil
	}

	// done is a chan used to ensure that we do not leak any goroutines.
	done := make(chan struct{})
	defer func() {
		done <- struct{}{}
	}()

	electionCtx, cancelElection := context.WithCancel(ctx)
	// Cancel the context if the program gets killed before we are done.
	go func() {
		select {
		// done is used whenever the function finishes.
		case <-done:
			return
		// If we are shutting down, and we were not elected as a leader yet
		// cancel the leader election context.
		case <-interceptor.ShutdownChannel():
			cancelElection()
		}
	}()

	log.Infof("Using %v leader elector", cfg.Cluster.LeaderElector)

	leaderElector, err := cfg.Cluster.MakeLeaderElector(
		electionCtx, &lncfg.DB{
			Backend: cluster.EtcdLeaderElector,
			Etcd: &etcd.Config{
				Host:       cfg.Etcd.Host,
				User:       cfg.Etcd.User,
				Pass:       cfg.Etcd.Password,
				DisableTLS: true,
			},
		},
	)
	if err != nil {
		return nil, err
	}

	log.Infof("Starting leadership campaign (%v)", cfg.Cluster.ID)

	if err := leaderElector.Campaign(electionCtx); err != nil {
		return nil, fmt.Errorf("leadership campaign failed: %v", err)
	}

	log.Infof("Elected as leader (%v)", cfg.Cluster.ID)

	return leaderElector, nil
}

// Server is the main auction auctioneer server.
type Server struct {
	rpcServer   *rpcServer
	adminServer *adminRPCServer

	hashMailServer *hashMailServer

	lnd            *lndclient.GrpcLndServices
	identityPubkey [33]byte

	cfg *Config

	statusReporter *status.Reporter

	leaderElector cluster.LeaderElector

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

	startOnce sync.Once
	stopOnce  sync.Once
}

// NewServer returns a new auctioneer server that is started in daemon mode,
// listens for gRPC connections and executes commands.
func NewServer(cfg *Config, // nolint:gocyclo
	interceptor signal.Interceptor) (*Server, error) {

	ctx := context.Background()

	// First, we'll set up our logging infrastructure so all operations
	// below will properly be logged.
	if err := initLogging(cfg); err != nil {
		return nil, fmt.Errorf("unable to init logging: %w", err)
	}

	// Print the version before we do any more set up to ensure we output
	// it.
	log.Infof("Version: %v", Version())

	statusReporter := status.NewReporter(cfg.Status)
	if err := statusReporter.Start(); err != nil {
		return nil, fmt.Errorf("unable to start status server: %v", err)
	}

	var (
		leaderElector cluster.LeaderElector
		err           error
	)
	if cfg.Cluster.EnableLeaderElection {
		// The subasta server does not support multiple instaces running
		// at the same time. We use etcd to ensure that there are never
		// more than one instance running. Continuation will be blocked
		// until this instance is elected as the current leader or it
		// is shutting down.
		_ = statusReporter.SetStatus(status.WaitingLeaderElection)
		leaderElector, err = waitUntilLeader(ctx, cfg, interceptor)
		if err != nil {
			return nil, err
		}
	}

	// With our logging set up, we'll now establish our initial connection
	// to the backing lnd instance.
	network := lndclient.Network(cfg.Network)
	lnd, err := lndclient.NewLndServices(&lndclient.LndServicesConfig{
		LndAddress:   cfg.Lnd.Host,
		Network:      network,
		MacaroonDir:  cfg.Lnd.MacaroonDir,
		TLSPath:      cfg.Lnd.TLSPath,
		CheckVersion: minimalCompatibleVersion,
	})
	if err != nil {
		return nil, err
	}

	// Attempt to create a new SQL connection for data optional mirroring.
	var sqlStore *subastadb.SQLGORMStore
	if cfg.SQLMirror {
		sqlStore, err = subastadb.NewSQLGORMStore(cfg.SQL)
		if err != nil {
			return nil, err
		}
	}

	// If we're in regtest only mode, spin up an embedded etcd server.
	if cfg.Network == networkRegtest && cfg.Etcd.User == etcdUserEmbedded {
		etcdCfg := embed.NewConfig()
		etcdCfg.Logger = "zap"
		etcdCfg.LogLevel = "error"
		etcdCfg.Dir = path.Join(cfg.BaseDir, "etcd")
		etcdCfg.LCUrls = []url.URL{{Host: cfg.Etcd.Host}}
		etcdCfg.LPUrls = []url.URL{{Host: "127.0.0.1:9126"}}

		// Set empty username and password to avoid an error being
		// being logged about authentication not being enabled.
		cfg.Etcd.User = ""
		cfg.Etcd.Password = ""

		etcdServer, err := embed.StartEtcd(etcdCfg)
		if err != nil {
			return nil, err
		}

		select {
		case <-etcdServer.Server.ReadyNotify():
		case <-time.After(5 * time.Second):
			etcdServer.Close()
			return nil, fmt.Errorf("etcd server took too long to" +
				"start")
		}
	}

	// Next, we'll open our primary connection to the main backing
	// database.
	store, err := subastadb.NewEtcdStore(
		*lnd.ChainParams, cfg.Etcd.Host, cfg.Etcd.User,
		cfg.Etcd.Password, sqlStore,
	)
	if err != nil {
		return nil, err
	}

	banManager := ban.NewManager(
		&ban.ManagerConfig{
			Store: store,
		},
	)

	// With our database open, we can set up the manager which watches over
	// all the trader accounts.
	accountManager, err := account.NewManager(&account.ManagerConfig{
		BanManager:    banManager,
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
		store: store,
		activeTraders: make(
			map[matching.AccountID]*venue.ActiveTrader,
		),
		termsCache:         make(map[lsat.TokenID]*traderterms.Custom),
		defaultFeeSchedule: auctionTerms.FeeSchedule(),
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
		BanManager:      banManager,
		Store:           store,
		Signer:          lnd.Signer,
		DurationBuckets: durationBuckets,
	})

	packageSource := chanenforcement.NewDefaultSource(banManager, store)
	channelEnforcer := chanenforcement.New(&chanenforcement.Config{
		ChainNotifier: lnd.ChainNotifier,
		PackageSource: packageSource,
	})

	if cfg.BatchConfTarget < 1 {
		return nil, fmt.Errorf("conf target must be greater than 0")
	}

	var ratingsDB ratings.NodeRatingsDatabase

	// We'll always use an in-memory ratings DB that writes through to the
	// etcd store.
	log.Infof("Initializing in-memory RatingsAgency")
	nodeRatings, err := store.NodeRatings(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve stored node "+
			"ratings: %v", err)
	}
	ratingsDB = ratings.NewMemRatingsDatabase(
		store, nodeRatings, cfg.DefaultNodeTier, nil,
	)

	// We'll only activate the BOS score backed ratings agency if it has
	// been flipped on in the config. In contexts like testnet or regtest,
	// we don't have an instance of bos scores to point to but we can still
	// manually edit the ratings through the admin RPC.
	if cfg.ExternalNodeRatingsActive && cfg.BosScoreWebURL != "" {
		log.Infof("Initializing BosScore backed RatingsAgency")

		bosScoreWebScore := &ratings.BosScoreWebRatings{
			URL: cfg.BosScoreWebURL,
		}
		ratingsDB = ratings.NewBosScoreRatingsDatabase(
			bosScoreWebScore, cfg.NodeRatingsRefreshInterval,
			cfg.DefaultNodeTier, ratingsDB,
		)
	}
	ratingsAgency := ratings.NewNodeTierAgency(ratingsDB)

	server := &Server{
		cfg:            cfg,
		lnd:            lnd,
		store:          store,
		statusReporter: statusReporter,
		leaderElector:  leaderElector,
		accountManager: accountManager,
		orderBook:      orderBook,
		batchExecutor:  batchExecutor,
		activeTraders:  activeTraders,
		auctioneer: NewAuctioneer(AuctioneerConfig{
			BanManager:    banManager,
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
				activeTraders, durationBuckets,
			),
			OrderFeed:              orderBook,
			BatchExecutor:          batchExecutor,
			FeeScheduler:           activeTraders,
			ChannelEnforcer:        channelEnforcer,
			ConfTarget:             cfg.BatchConfTarget,
			AccountExpiryExtension: cfg.AccountExpiryExtension,
			AccountExpiryOffset:    cfg.AccountExpiryOffset,
			AccountFetcher: func(acctID matching.AccountID) (
				*account.Account, error) {

				acctKey, err := btcec.ParsePubKey(acctID[:])
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
			GetActiveTrader:               activeTraders.GetTrader,
			FundingConflicts:              fundingConflicts,
			FundingConflictsResetInterval: cfg.FundingConflictResetInterval,
			TraderRejected:                traderRejected,
			TraderRejectResetInterval:     cfg.TraderRejectResetInterval,
			TraderOnline: matching.NewTraderOnlineFilter(
				activeTraders.IsActive,
			),
			RatingsAgency: ratingsAgency,
		}),
		channelEnforcer: channelEnforcer,
		ratingsDB:       ratingsDB,
		durationBuckets: durationBuckets,
	}

	// With all our other initialization complete, we'll now create the
	// main RPC server.
	//
	// First, we'll set up the series of interceptors for our gRPC server
	// which we'll initialize shortly below.
	if cfg.AllowFakeTokens && cfg.Network == "mainnet" {
		return nil, fmt.Errorf("cannot use fake LSAT tokens for mainnet")
	}

	// We'll always use the main (production) LSAT interceptor. But in case
	// we also want to allow fake tokens to be set (itest only), we add the
	// regtest interceptor in front of the main one.
	var (
		mainInterceptor   = &lsat.ServerInterceptor{}
		unaryInterceptors = []grpc.UnaryServerInterceptor{
			mainInterceptor.UnaryInterceptor,
			errorLogUnaryServerInterceptor(rpcLog),
		}
		streamInterceptors = []grpc.StreamServerInterceptor{
			mainInterceptor.StreamInterceptor,
			errorLogStreamServerInterceptor(rpcLog),
		}
	)
	if cfg.AllowFakeTokens {
		fakeTokenInterceptor := &regtestInterceptor{}
		unaryInterceptors = append([]grpc.UnaryServerInterceptor{
			fakeTokenInterceptor.UnaryInterceptor,
		}, unaryInterceptors...)
		streamInterceptors = append([]grpc.StreamServerInterceptor{
			fakeTokenInterceptor.StreamInterceptor,
		}, streamInterceptors...)
	}

	// Prometheus itself needs a gRPC interceptor to measure performance of
	// the API calls. We chain them together with the LSAT interceptor.
	if cfg.Prometheus.Active {
		cfg.Prometheus.Store = store
		cfg.Prometheus.Lnd = lnd.LndServices
		cfg.Prometheus.FundingConflicts = fundingConflicts

		unaryInterceptors = append(
			unaryInterceptors,
			grpc_prometheus.UnaryServerInterceptor,
		)
		streamInterceptors = append(
			streamInterceptors,
			grpc_prometheus.StreamServerInterceptor,
		)
	}

	// Append TLS configuration to server options.
	serverTLS, clientCertOpt, err := getTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryInterceptors...),
		grpc.ChainStreamInterceptor(streamInterceptors...),
		grpc.Creds(credentials.NewTLS(serverTLS)),
	}

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

	server.hashMailServer = newHashMailServer(hashMailServerConfig{
		IsAccountActive: func(ctx context.Context,
			acctKey *btcec.PublicKey) bool {

			acct, err := store.Account(ctx, acctKey, false)
			if err != nil {
				return false
			}

			switch acct.State {
			case account.StatePendingUpdate,
				account.StatePendingBatch, account.StateOpen:

				return true
			default:
				return false
			}
		},
		Signer: lnd.Signer,
	})
	auctioneerrpc.RegisterHashMailServer(
		auctioneerServer.grpcServer, server.hashMailServer,
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
		lnd.Client, statusReporter,
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
		// Use one timeout context for the store initialization. This
		// is a context with a long timeout to allow enough time for
		// filling up caches as well as mirroring things to SQL.
		storeInitCtx, storeInitCancel := context.WithTimeout(
			ctx, initTimeout,
		)
		defer storeInitCancel()
		if err := s.store.Init(storeInitCtx); err != nil {
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
				startErr = fmt.Errorf("unable to index "+
					"ratings: %v", err)
				return
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

			log.Infof("Starting initial metrics collection to " +
				"fill cache...")
			start := time.Now()
			promClient.CollectAll()
			log.Infof("Finished initial metrics collection in %v",
				time.Since(start))
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

		// Notify the status reporter that we are ready to receive
		// requests.
		_ = s.statusReporter.SetStatus(status.UpAndRunning)
	})

	return startErr
}

// Stop shuts down the server, including all client connections and network
// listeners.
func (s *Server) Stop() error {
	log.Info("Main server: Received shutdown signal, stopping server")

	var stopErr error

	s.stopOnce.Do(func() {
		s.adminServer.Stop()
		s.rpcServer.Stop()
		s.hashMailServer.Stop()

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

		if s.cfg.Prometheus.Active {
			s.cfg.Prometheus.BitcoinClient.Shutdown()
		}

		if s.cfg.Cluster.EnableLeaderElection && s.leaderElector != nil {
			err := s.leaderElector.Resign()
			if err != nil {
				log.Errorf("Leader elector failed to resign: %v", err)
			}
		}

		err := s.statusReporter.Stop(context.Background())
		if err != nil {
			stopErr = fmt.Errorf("unable to stop status service: %w",
				err)
			return
		}
	})

	log.Infof("Main server: %s", ShutdownCompleteLogMessage)

	return stopErr
}

// ServerInterceptor is an LSAT interceptor that reads the dummy LSAT
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
	interface{}, error) {

	id, err := idFromContext(ctx)
	if err != nil {
		log.Debugf("No ID extracted, error was: %v", err)
		return handler(ctx, req)
	}
	idCtx := lsat.AddToContext(ctx, lsat.KeyTokenID, *id)
	return handler(idCtx, req)
}

// StreamInterceptor intercepts streaming requests and reads the dummy LSAT
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
		return nil, fmt.Errorf("request contains no fake LSAT")
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
