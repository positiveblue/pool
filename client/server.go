package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"

	proxy "github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/lightninglabs/agora/client/auctioneer"
	"github.com/lightninglabs/agora/client/clientdb"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/lnrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// Server is the main agora trader server.
type Server struct {
	// AuctioneerClient is the wrapper around the connection from the trader
	// client to the auctioneer server. It is exported so we can replace
	// the connection with a new one in the itest, if the server is
	// restarted.
	AuctioneerClient *auctioneer.Client

	// GetIdentity returns the current LSAT identification of the trader
	// client or an error if none has been established yet.
	GetIdentity func() (*lsat.TokenID, error)

	cfg          *Config
	db           *clientdb.DB
	lndServices  *lndclient.GrpcLndServices
	lndClient    lnrpc.LightningClient
	traderServer *rpcServer
	grpcServer   *grpc.Server
	restProxy    *http.Server
	grpcListener net.Listener
	restListener net.Listener
	wg           sync.WaitGroup
}

// NewServer creates a new trader server.
func NewServer(cfg *Config) (*Server, error) {
	// Append the network type to the log directory so it is
	// "namespaced" per network in the same fashion as the data directory.
	cfg.LogDir = filepath.Join(cfg.LogDir, cfg.Network)

	// Initialize logging at the default logging level.
	err := logWriter.InitLogRotator(
		filepath.Join(cfg.LogDir, DefaultLogFilename),
		cfg.MaxLogFileSize, cfg.MaxLogFiles,
	)
	if err != nil {
		return nil, err
	}
	err = build.ParseAndSetDebugLevels(cfg.DebugLevel, logWriter)
	if err != nil {
		return nil, err
	}

	// Print the version before executing either primary directive.
	log.Infof("Version: %v", Version())

	lndServices, err := getLnd(cfg.Network, cfg.Lnd)
	if err != nil {
		return nil, err
	}

	// If no auction server is specified, use the default addresses for
	// mainnet and testnet.
	if cfg.AuctionServer == "" && len(cfg.AuctioneerDialOpts) == 0 {
		switch cfg.Network {
		case "mainnet":
			cfg.AuctionServer = MainnetServer
		case "testnet":
			cfg.AuctionServer = TestnetServer
		default:
			return nil, errors.New("no auction server address " +
				"specified")
		}
	}

	log.Infof("Auction server address: %v", cfg.AuctionServer)

	// Open the main database.
	networkDir := filepath.Join(cfg.BaseDir, cfg.Network)
	db, err := clientdb.New(networkDir)
	if err != nil {
		return nil, err
	}

	// Setup the LSAT interceptor for the client.
	fileStore, err := lsat.NewFileStore(networkDir)
	if err != nil {
		return nil, err
	}
	var interceptor Interceptor = lsat.NewInterceptor(
		&lndServices.LndServices, fileStore, defaultRPCTimeout,
		defaultLsatMaxCost, defaultLsatMaxFee,
	)

	// getIdentity can be used to determine the current LSAT identification
	// of the trader.
	getIdentity := func() (*lsat.TokenID, error) {
		token, err := fileStore.CurrentToken()
		if err != nil {
			return nil, err
		}
		macID, err := lsat.DecodeIdentifier(
			bytes.NewBuffer(token.BaseMacaroon().Id()),
		)
		if err != nil {
			return nil, err
		}
		return &macID.TokenID, nil
	}

	// For regtest, we create a fixed identity now that is used for the
	// whole runtime of the trader.
	if cfg.Network == "regtest" {
		var tokenID lsat.TokenID
		_, _ = rand.Read(tokenID[:])
		interceptor = &regtestInterceptor{id: tokenID}
		getIdentity = func() (*lsat.TokenID, error) {
			return &tokenID, nil
		}
	}
	cfg.AuctioneerDialOpts = append(
		cfg.AuctioneerDialOpts,
		grpc.WithUnaryInterceptor(interceptor.UnaryInterceptor),
		grpc.WithStreamInterceptor(interceptor.StreamInterceptor),
	)

	// Create an instance of the auctioneer client library.
	clientCfg := &auctioneer.Config{
		ServerAddress: cfg.AuctionServer,
		Insecure:      cfg.Insecure,
		TLSPathServer: cfg.TLSPathAuctSrv,
		DialOpts:      cfg.AuctioneerDialOpts,
		Signer:        lndServices.Signer,
		MinBackoff:    cfg.MinBackoff,
		MaxBackoff:    cfg.MaxBackoff,
		BatchSource:   db,
	}
	auctioneerClient, err := auctioneer.NewClient(clientCfg)
	if err != nil {
		return nil, err
	}

	// As there're some other lower-level operations we may need access to,
	// we'll also make a connection for a "basic client".
	//
	// TODO(roasbeef): more granular macaroons, can ask user to make just
	// what we need
	baseClient, err := lndclient.NewBasicClient(
		cfg.Lnd.Host, cfg.Lnd.TLSPath, cfg.Lnd.MacaroonDir, cfg.Network,
	)
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:              cfg,
		db:               db,
		lndServices:      lndServices,
		lndClient:        baseClient,
		AuctioneerClient: auctioneerClient,
		GetIdentity:      getIdentity,
	}, nil
}

// Start runs agorad in daemon mode. It will listen for grpc connections,
// execute commands and pass back auction status information.
func (s *Server) Start() error {
	var err error

	// Instantiate the agorad gRPC server.
	s.traderServer = newRPCServer(s)

	serverOpts := []grpc.ServerOption{}
	s.grpcServer = grpc.NewServer(serverOpts...)
	clmrpc.RegisterTraderServer(s.grpcServer, s.traderServer)

	// Next, start the gRPC server listening for HTTP/2 connections.
	// If the provided grpcListener is not nil, it means agorad is being
	// used as a library and the listener might not be a real network
	// connection (but maybe a UNIX socket or bufconn). So we don't spin up
	// a REST listener in that case.
	log.Infof("Starting gRPC listener")
	s.grpcListener = s.cfg.RPCListener
	if s.grpcListener == nil {
		s.grpcListener, err = net.Listen("tcp", s.cfg.RPCListen)
		if err != nil {
			return fmt.Errorf("RPC server unable to listen on %s",
				s.cfg.RPCListen)

		}

		// We'll also create and start an accompanying proxy to serve
		// clients through REST.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mux := proxy.NewServeMux()
		proxyOpts := []grpc.DialOption{grpc.WithInsecure()}
		err = clmrpc.RegisterTraderHandlerFromEndpoint(
			ctx, mux, s.cfg.RPCListen, proxyOpts,
		)
		if err != nil {
			return err
		}

		log.Infof("Starting REST proxy listener")
		s.restListener, err = net.Listen("tcp", s.cfg.RESTListen)
		if err != nil {
			return fmt.Errorf("REST proxy unable to listen on %s",
				s.cfg.RESTListen)
		}
		s.restProxy = &http.Server{Handler: mux}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()

			err := s.restProxy.Serve(s.restListener)
			if err != nil && err != http.ErrServerClosed {
				log.Errorf("Could not start rest listener: %v",
					err)
			}
		}()
	}

	// Start the trader server itself.
	err = s.traderServer.Start()
	if err != nil {
		return err
	}

	// Start the grpc server.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		log.Infof("RPC server listening on %s", s.grpcListener.Addr())
		if s.restListener != nil {
			log.Infof("REST proxy listening on %s",
				s.restListener.Addr())
		}

		err = s.grpcServer.Serve(s.grpcListener)
		if err != nil {
			log.Errorf("Unable to server gRPC: %v", err)
		}
	}()

	return nil
}

// Stop shuts down the server, including the auction server connection, all
// client connections and network listeners.
func (s *Server) Stop() error {
	log.Info("Received shutdown signal, stopping server")

	// Don't return any errors yet, give everything else a chance to shut
	// down first.
	err := s.traderServer.Stop()
	s.grpcServer.GracefulStop()
	if s.restProxy != nil {
		err := s.restProxy.Shutdown(context.Background())
		if err != nil {
			log.Errorf("Error shutting down REST proxy: %v", err)
		}
	}
	if err := s.db.Close(); err != nil {
		log.Errorf("Error closing DB: %v", err)
	}
	s.lndServices.Close()
	s.wg.Wait()

	if err != nil {
		return fmt.Errorf("error shutting down server: %v", err)
	}
	return nil
}

// BatchChannelSetup...
func (s *Server) BatchChannelSetup(batch *order.Batch) error {
	// TODO:
	//  - connect to peers of new channels
	//  - register funding shim in lnd
	return nil
}

// getLnd returns an instance of the lnd services proxy.
func getLnd(network string, cfg *LndConfig) (*lndclient.GrpcLndServices, error) {
	return lndclient.NewLndServices(
		&lndclient.LndServicesConfig{
			LndAddress:  cfg.Host,
			Network:     network,
			MacaroonDir: cfg.MacaroonDir,
			TLSPath:     cfg.TLSPath,
		},
	)
}

// Interceptor is the interface a client side gRPC interceptor has to implement.
type Interceptor interface {
	// UnaryInterceptor intercepts normal, non-streaming requests from the
	// client to the server.
	UnaryInterceptor(context.Context, string, interface{}, interface{},
		*grpc.ClientConn, grpc.UnaryInvoker, ...grpc.CallOption) error

	// StreamInterceptor intercepts streaming requests from the client to
	// the server.
	StreamInterceptor(context.Context, *grpc.StreamDesc, *grpc.ClientConn,
		string, grpc.Streamer, ...grpc.CallOption) (grpc.ClientStream,
		error)
}

// regtestInterceptor is a dummy gRPC interceptor that can be used on regtest to
// simulate identification through LSAT.
type regtestInterceptor struct {
	id lsat.TokenID
}

// UnaryInterceptor intercepts non-streaming requests and appends the dummy LSAT
// ID.
func (i *regtestInterceptor) UnaryInterceptor(ctx context.Context, method string,
	req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption) error {

	idStr := fmt.Sprintf("LSATID %x", i.id[:])
	idCtx := metadata.AppendToOutgoingContext(
		ctx, auth.HeaderAuthorization, idStr,
	)
	return invoker(idCtx, method, req, reply, cc, opts...)
}

// StreamingInterceptor intercepts streaming requests and appends the dummy LSAT
// ID.
func (i *regtestInterceptor) StreamInterceptor(ctx context.Context,
	desc *grpc.StreamDesc, cc *grpc.ClientConn, method string,
	streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream,
	error) {

	idStr := fmt.Sprintf("LSATID %x", i.id[:])
	idCtx := metadata.AppendToOutgoingContext(
		ctx, auth.HeaderAuthorization, idStr,
	)
	return streamer(idCtx, desc, cc, method, opts...)
}
