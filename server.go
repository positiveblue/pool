package agora

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/cert"
	"github.com/lightningnetwork/lnd/lnrpc"
	"golang.org/x/crypto/acme/autocert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Server is the main agora auctioneer server.
type Server struct {
	auctioneerServer *rpcServer
}

// NewServer returns a new auctioneer server that is started in daemon mode,
// listens for gRPC connections and executes commands.
func NewServer(cfg *Config) (*Server, error) {
	// First, we'll set up our logging infrastructure so all operations
	// below will properly be logged.
	if err := initLogging(cfg); err != nil {
		return nil, fmt.Errorf("unable to init logging: %w", err)
	}

	lndServices, err := lndclient.NewLndServices(
		cfg.Lnd.Host, cfg.Network, cfg.Lnd.MacaroonDir,
		cfg.Lnd.TLSPath,
	)
	if err != nil {
		return nil, err
	}

	store, err := agoradb.NewEtcdStore(
		*lndServices.ChainParams, cfg.Etcd.Host, cfg.Etcd.User,
		cfg.Etcd.Password,
	)
	if err != nil {
		return nil, err
	}

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

	auctioneerServer, err := newRPCServer(
		lndServices, store, grpcListener, serverOpts,
		btcutil.Amount(cfg.OrderSubmitFee), cfg.SubscribeTimeout,
	)
	if err != nil {
		return nil, err
	}

	clmrpc.RegisterChannelAuctioneerServer(
		auctioneerServer.Server, auctioneerServer,
	)

	// Start the auctioneer server itself.
	err = auctioneerServer.Start()
	if err != nil {
		return nil, fmt.Errorf("unable to start agora server: %v", err)
	}
	return &Server{
		auctioneerServer: auctioneerServer,
	}, nil
}

// Stop shuts down the server, including all client connections and network
// listeners.
func (s *Server) Stop() error {
	log.Info("Received shutdown signal, stopping server")
	err := s.auctioneerServer.Stop()
	if err != nil {
		return fmt.Errorf("error shutting down server: %v", err)
	}
	return nil
}

// ConnectedStreams returns all currently connected traders and their
// subscriptions.
func (s *Server) ConnectedStreams() map[lsat.TokenID]*TraderStream {
	return s.auctioneerServer.connectedStreams
}
