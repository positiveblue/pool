package agora

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightninglabs/loop/lndclient"
)

const (
	// initTimeout is the maximum time we allow for the etcd store to be
	// initialized.
	initTimeout = 30 * time.Second
)

type Server struct {
	// bestHeight is the best known height of the main chain. The links will
	// be used this information to govern decisions based on HTLC timeouts.
	// This will be retrieved by the registered links atomically.
	bestHeight uint32

	lnd            *lndclient.LndServices
	identityPubkey [33]byte
	store          agoradb.Store

	started uint32 // To be used atomically.
	stopped uint32 // To be used atomically.
	quit    chan struct{}
	wg      sync.WaitGroup
}

func NewEtcdServer(lnd *lndclient.LndServices,
	etcdHost, user, pass string) (*Server, error) {

	store, err := agoradb.NewEtcdStore(
		*lnd.ChainParams, etcdHost, user, pass,
	)
	if err != nil {
		return nil, err
	}

	return &Server{
		lnd:   lnd,
		store: store,
		quit:  make(chan struct{}),
	}, nil
}

// Start starts the Server, making it ready to accept incoming requests.
func (s *Server) Start() error {
	if !atomic.CompareAndSwapUint32(&s.started, 0, 1) {
		return nil
	}

	log.Infof("Starting auction server")
	s.wg.Add(1)

	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	if err := s.store.Init(ctx); err != nil {
		return err
	}

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	infoResp, err := s.lnd.Client.GetInfo(ctx)
	if err != nil {
		return err
	}
	s.identityPubkey = infoResp.IdentityPubkey

	blockEpochChan, blockErrorChan, err := s.lnd.
		ChainNotifier.RegisterBlockEpochNtfn(ctx)
	if err != nil {
		return err
	}

	// Before finishing Start(), make sure we have an up to date block
	// height.
	log.Infof("Wait for first block ntfn")

	var height int32
	select {
	case height = <-blockEpochChan:
	case err := <-blockErrorChan:
		return fmt.Errorf("RegisterBlockEpochNtfn: %v", err)
	case <-ctx.Done():
		return nil
	}

	s.updateHeight(height)

	log.Infof("Auction server started at height %v", height)

	go s.serverHandler()

	return nil
}

// Stop stops the server.
func (s *Server) Stop() error {
	if !atomic.CompareAndSwapUint32(&s.stopped, 0, 1) {
		return nil
	}

	log.Info("Auction server terminating")
	close(s.quit)
	s.wg.Wait()

	log.Info("Auction server terminated")
	return nil
}

// serverHandler is the main event loop of the server.
func (s *Server) serverHandler() {
	defer s.wg.Done()

	for { // nolint
		// TODO(guggero): Implement main event loop.
		select {

		// In case the server is shutting down.
		case <-s.quit:
			return
		}
	}
}

func (s *Server) updateHeight(height int32) {
	log.Infof("Received block %v", height)

	// Store height atomically so the incoming request handler can access it
	// without locking.
	atomic.StoreUint32(&s.bestHeight, uint32(height))
}

func (s *Server) InitAccount(ctx context.Context,
	req *clmrpc.ServerInitAccountRequest) (
	*clmrpc.ServerInitAccountResponse, error) {

	return nil, fmt.Errorf("unimplemented")
}

func (s *Server) CloseAccount(ctx context.Context,
	req *clmrpc.ServerCloseAccountRequest) (
	*clmrpc.ServerCloseAccountResponse, error) {

	return nil, fmt.Errorf("unimplemented")
}

func (s *Server) ModifyAccount(ctx context.Context,
	req *clmrpc.ServerModifyAccountRequest) (
	*clmrpc.ServerModifyAccountResponse, error) {

	return nil, fmt.Errorf("unimplemented")
}

func (s *Server) SubmitOrder(ctx context.Context,
	req *clmrpc.ServerOrderRequest) (*clmrpc.ServerOrderResponse, error) {

	return nil, fmt.Errorf("unimplemented")
}

func (s *Server) CancelOrder(ctx context.Context,
	req *clmrpc.ServerOrderRequest) (*clmrpc.ServerOrderResponse, error) {

	return nil, fmt.Errorf("unimplemented")
}

func (s *Server) SubscribeBatchAuction(
	stream clmrpc.ChannelAuctioneerServer_SubscribeBatchAuctionServer) error {

	return fmt.Errorf("unimplemented")
}
