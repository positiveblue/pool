package agora

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
)

const (
	// initTimeout is the maximum time we allow for the etcd store to be
	// initialized.
	initTimeout = 5 * time.Second

	// getInfoTimeout is the maximum time we allow for the GetInfo call to
	// the backing lnd node.
	getInfoTimeout = 5 * time.Second
)

type Server struct {
	started uint32 // To be used atomically.
	stopped uint32 // To be used atomically.

	// bestHeight is the best known height of the main chain. This MUST be
	// used atomically.
	bestHeight uint32

	lnd            *lndclient.LndServices
	identityPubkey [33]byte
	store          agoradb.Store
	accountManager *account.Manager

	quit chan struct{}
	wg   sync.WaitGroup
}

func NewEtcdServer(lnd *lndclient.LndServices,
	etcdHost, user, pass string) (*Server, error) {

	store, err := agoradb.NewEtcdStore(
		*lnd.ChainParams, etcdHost, user, pass,
	)
	if err != nil {
		return nil, err
	}

	accountManager, err := account.NewManager(&account.ManagerConfig{
		Store:         store,
		Wallet:        lnd.WalletKit,
		ChainNotifier: lnd.ChainNotifier,
	})
	if err != nil {
		return nil, err
	}

	return &Server{
		lnd:            lnd,
		store:          store,
		accountManager: accountManager,
		quit:           make(chan struct{}),
	}, nil
}

// Start starts the Server, making it ready to accept incoming requests.
func (s *Server) Start() error {
	if !atomic.CompareAndSwapUint32(&s.started, 0, 1) {
		return nil
	}

	log.Infof("Starting auction server")

	ctx := context.Background()
	etcdCtx, etcdCancel := context.WithTimeout(ctx, initTimeout)
	defer etcdCancel()
	if err := s.store.Init(etcdCtx); err != nil {
		return fmt.Errorf("unable to initialize etcd store: %v", err)
	}

	lndCtx, lndCancel := context.WithTimeout(ctx, getInfoTimeout)
	defer lndCancel()
	infoResp, err := s.lnd.Client.GetInfo(lndCtx)
	if err != nil {
		return fmt.Errorf("unable to retrieve lnd node public key: %v",
			err)
	}
	s.identityPubkey = infoResp.IdentityPubkey

	blockEpochChan, blockErrorChan, err := s.lnd.
		ChainNotifier.RegisterBlockEpochNtfn(ctx)
	if err != nil {
		return err
	}

	// Before finishing Start(), make sure we have an up to date block
	// height.
	var height int32
	select {
	case height = <-blockEpochChan:
	case err := <-blockErrorChan:
		return fmt.Errorf("RegisterBlockEpochNtfn: %v", err)
	case <-ctx.Done():
		return nil
	}

	s.updateHeight(height)

	if err := s.accountManager.Start(); err != nil {
		return fmt.Errorf("unable to start account manager: %v", err)
	}

	s.wg.Add(1)
	go s.serverHandler(blockEpochChan, blockErrorChan)

	log.Infof("Auction server is now active")

	return nil
}

// Stop stops the server.
func (s *Server) Stop() error {
	if !atomic.CompareAndSwapUint32(&s.stopped, 0, 1) {
		return nil
	}

	log.Info("Stopping auction server")

	s.accountManager.Stop()

	close(s.quit)
	s.wg.Wait()

	log.Info("Auction server stopped")
	return nil
}

// serverHandler is the main event loop of the server.
func (s *Server) serverHandler(blockChan chan int32, blockErrChan chan error) {
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

		// In case the server is shutting down.
		case <-s.quit:
			return
		}
	}
}

func (s *Server) updateHeight(height int32) {
	// Store height atomically so the incoming request handler can access it
	// without locking.
	atomic.StoreUint32(&s.bestHeight, uint32(height))
}

func (s *Server) ReserveAccount(ctx context.Context,
	req *clmrpc.ReserveAccountRequest) (*clmrpc.ReserveAccountResponse, error) {

	// TODO(wilmer): Extract token ID from LSAT.
	var tokenID lsat.TokenID

	var traderKey [33]byte
	copy(traderKey[:], req.UserSubKey)

	ourKey, err := s.accountManager.ReserveAccount(ctx, tokenID, traderKey)
	if err != nil {
		return nil, err
	}

	return &clmrpc.ReserveAccountResponse{
		AuctioneerKey: ourKey[:],
	}, nil
}

func (s *Server) InitAccount(ctx context.Context,
	req *clmrpc.ServerInitAccountRequest) (*clmrpc.ServerInitAccountResponse, error) {

	// TODO(wilmer): Extract token ID from LSAT.
	var tokenID lsat.TokenID

	var txid chainhash.Hash
	copy(txid[:], req.AccountPoint.Txid)
	accountPoint := wire.OutPoint{
		Hash:  txid,
		Index: req.AccountPoint.OutputIndex,
	}

	var traderKey [33]byte
	copy(traderKey[:], req.UserSubKey)

	err := s.accountManager.InitAccount(
		ctx, tokenID, accountPoint, btcutil.Amount(req.AccountValue),
		req.AccountScript, req.AccountExpiry, traderKey,
		atomic.LoadUint32(&s.bestHeight),
	)
	if err != nil {
		return nil, err
	}

	return &clmrpc.ServerInitAccountResponse{}, nil
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
