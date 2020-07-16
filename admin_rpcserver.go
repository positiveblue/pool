package subasta

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lightninglabs/subasta/adminrpc"
	"google.golang.org/grpc"
)

// adminRPCServer is a server that implements the admin server RPC interface and
// serves administrative and super user content.
type adminRPCServer struct {
	grpcServer *grpc.Server

	listener net.Listener
	serveWg  sync.WaitGroup

	started uint32 // To be used atomically.
	stopped uint32 // To be used atomically.

	quit chan struct{}
	wg   sync.WaitGroup

	mainRPCServer *rpcServer
	auctioneer    *Auctioneer
}

// newAdminRPCServer creates a new adminRPCServer.
func newAdminRPCServer(mainRPCServer *rpcServer, listener net.Listener,
	serverOpts []grpc.ServerOption, auctioneer *Auctioneer) *adminRPCServer {

	return &adminRPCServer{
		grpcServer:    grpc.NewServer(serverOpts...),
		listener:      listener,
		quit:          make(chan struct{}),
		mainRPCServer: mainRPCServer,
		auctioneer:    auctioneer,
	}
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

	close(s.quit)
	s.wg.Wait()

	log.Info("Stopping admin gRPC server and listener")
	s.grpcServer.Stop()
	s.serveWg.Wait()

	log.Info("Admin server stopped")
}

func (s *adminRPCServer) MasterAccount(ctx context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.MasterAccountResponse, error) {

	masterAcct, err := s.mainRPCServer.store.FetchAuctioneerAccount(ctx)
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
	}, nil
}

func (s *adminRPCServer) ConnectedTraders(_ context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.ConnectedTradersResponse, error) {

	result := &adminrpc.ConnectedTradersResponse{
		Streams: make(map[string]*adminrpc.PubKeyList),
	}
	streams := s.mainRPCServer.ConnectedStreams()
	for lsatID, stream := range streams {
		acctList := &adminrpc.PubKeyList{RawKeyBytes: make(
			[][]byte, 0, len(stream.Subscriptions),
		)}
		for acctKey := range stream.Subscriptions {
			acctList.RawKeyBytes = append(
				acctList.RawKeyBytes, acctKey[:],
			)
		}
		result.Streams[lsatID.String()] = acctList
	}

	return result, nil
}

func (s *adminRPCServer) BatchTick(_ context.Context,
	_ *adminrpc.EmptyRequest) (*adminrpc.EmptyResponse, error) {

	// Force a new batch ticker event in the main auctioneer state machine.
	s.auctioneer.cfg.BatchTicker.Force <- time.Now()

	return &adminrpc.EmptyResponse{}, nil
}
