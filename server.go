package agora

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/agora/client/clmrpc"
	orderT "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
	"github.com/lightninglabs/agora/venue"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
	"google.golang.org/grpc"
)

// Server is the main agora auctioneer server.
type Server struct {
	// bestHeight is the best known height of the main chain. This MUST be
	// used atomically.
	bestHeight uint32

	rpcServer *rpcServer

	lnd            *lndclient.GrpcLndServices
	identityPubkey [33]byte

	store agoradb.Store

	accountManager *account.Manager

	orderBook *order.Book

	batchExecutor *venue.BatchExecutor

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
	lnd, err := lndclient.NewLndServices(
		cfg.Lnd.Host, cfg.Network, cfg.Lnd.MacaroonDir,
		cfg.Lnd.TLSPath,
	)
	if err != nil {
		return nil, err
	}

	// Next, we'll open our primary connection to the main backing
	// database.
	store, err := agoradb.NewEtcdStore(
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
	batchExecutor, err := venue.NewBatchExecutor()
	if err != nil {
		return nil, err
	}

	server := &Server{
		lnd:            lnd,
		store:          store,
		accountManager: accountManager,
		orderBook: order.NewBook(&order.BookConfig{
			Store:     store,
			Signer:    lnd.Signer,
			SubmitFee: btcutil.Amount(cfg.OrderSubmitFee),
		}),
		batchExecutor: batchExecutor,
		quit:          make(chan struct{}),
	}

	// With all our other initialization complete, we'll now create the
	// main RPC server.
	//
	// First, we'll set up the series of interceptors for our gRPC server
	// which we'll initialize shortly below.
	interceptor := auth.ServerInterceptor{}
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

	// Finally, create our listener, and initialize the primary gRPc server
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
		store, lnd, accountManager, server.fetchBestHeight,
		server.orderBook, batchExecutor, feeSchedule, grpcListener,
		serverOpts, cfg.SubscribeTimeout,
	)
	server.rpcServer = auctioneerServer

	clmrpc.RegisterChannelAuctioneerServer(
		auctioneerServer.grpcServer, auctioneerServer,
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
		}

		lndCtx, lndCancel := context.WithTimeout(ctx, getInfoTimeout)
		defer lndCancel()
		infoResp, err := s.lnd.Client.GetInfo(lndCtx)
		if err != nil {
			startErr = fmt.Errorf("unable to retrieve lnd node "+
				"public key: %v", err)
		}
		s.identityPubkey = infoResp.IdentityPubkey

		blockEpochChan, blockErrorChan, err := s.lnd.
			ChainNotifier.RegisterBlockEpochNtfn(ctx)
		if err != nil {
			startErr = err
		}

		// Before finishing Start(), make sure we have an up to date block
		// height.
		var height int32
		select {
		case height = <-blockEpochChan:
		case err := <-blockErrorChan:
			startErr = fmt.Errorf("RegisterBlockEpochNtfn: %v", err)
		case <-ctx.Done():
			return
		}
		s.updateHeight(height)

		// Start managers.
		if err := s.accountManager.Start(); err != nil {
			startErr = fmt.Errorf("unable to start account "+
				"manager: %v", err)
		}
		if err := s.orderBook.Start(); err != nil {
			startErr = fmt.Errorf("unable to start order "+
				"manager: %v", err)
		}
		if err := s.batchExecutor.Start(); err != nil {
			startErr = fmt.Errorf("unable to start batch "+
				"executor: %v", err)
		}

		s.wg.Add(1)
		go s.auctioneer(blockEpochChan, blockErrorChan)

		// Start the gRPC server itself.
		err = s.rpcServer.Start()
		if err != nil {
			startErr = fmt.Errorf("unable to start agora "+
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

		s.accountManager.Stop()
		s.orderBook.Stop()

		s.lnd.Close()

		err := s.rpcServer.Stop()
		if err != nil {
			stopErr = fmt.Errorf("error shutting down "+
				"server: %w", err)
			return
		}

		s.wg.Wait()
	})

	return stopErr
}

// updateHeight stores the height atomically so the incoming request handler
// can access it without locking.
func (s *Server) updateHeight(height int32) {
	atomic.StoreUint32(&s.bestHeight, uint32(height))
}

// fetchBestHeight returns the current best known block height.
func (s *Server) fetchBestHeight() uint32 {
	return atomic.LoadUint32(&s.bestHeight)
}

// auctioneer is the main control loop of the entire daemon. This goroutine
// will carry out the multi-step batch auction lifecyle.
//
// TODO(roasbeef): move to diff file?
func (s *Server) auctioneer(blockChan chan int32, blockErrChan chan error) {
	defer s.wg.Done()

	for {
		select {

		case height := <-blockChan:
			log.Infof("Received new block notification: height=%v",
				height)
			s.updateHeight(height)

		case err := <-blockErrChan:
			if err != nil {
				log.Errorf("Unable to receive block "+
					"notification: %v", err)
			}

		case <-s.quit:
			return
		}
	}
}

// ConnectedStreams returns all currently connected traders and their
// subscriptions.
func (s *Server) ConnectedStreams() map[lsat.TokenID]*TraderStream {
	return s.rpcServer.connectedStreams
}
