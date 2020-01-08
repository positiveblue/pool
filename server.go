package agora

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/agora/client/clmrpc"
	clientorder "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/lnwire"
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
	orderManager   *order.Book

	quit chan struct{}
	wg   sync.WaitGroup
}

func NewEtcdServer(lnd *lndclient.LndServices, etcdHost, user, pass string,
	orderSubmitFee btcutil.Amount) (*Server, error) {

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
		orderManager: order.NewBook(&order.BookConfig{
			Store:     store,
			Signer:    lnd.Signer,
			SubmitFee: orderSubmitFee,
		}),
		quit: make(chan struct{}),
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

	// Start managers.
	if err := s.accountManager.Start(); err != nil {
		return fmt.Errorf("unable to start account manager: %v", err)
	}
	if err := s.orderManager.Start(); err != nil {
		return fmt.Errorf("unable to start order manager: %v", err)
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
	s.orderManager.Stop()

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

// SubmitOrder parses a client's request to submit an order, validates it and
// if successful, stores it to the database and hands it over to the manager
// for further processing.
func (s *Server) SubmitOrder(ctx context.Context,
	req *clmrpc.ServerSubmitOrderRequest) (
	*clmrpc.ServerSubmitOrderResponse, error) {

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
		clientAsk := &clientorder.Ask{
			Kit:         *clientKit,
			MaxDuration: uint32(a.MaxDurationBlocks),
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
		clientBid := &clientorder.Bid{
			Kit:         *clientKit,
			MinDuration: uint32(b.MinDurationBlocks),
		}
		o = &order.Bid{
			Bid: *clientBid,
			Kit: *serverKit,
		}

	default:
		return nil, fmt.Errorf("invalid order request")
	}

	// Formally everything seems OK, hand over the order to the manager for
	// further validation and processing.
	err := s.orderManager.PrepareOrder(ctx, o)
	return mapOrderResp(o.Nonce(), err)
}

// CancelOrder tries to remove an order from the order book and mark it as
// revoked by the user.
func (s *Server) CancelOrder(ctx context.Context,
	req *clmrpc.ServerCancelOrderRequest) (
	*clmrpc.ServerCancelOrderResponse, error) {

	var nonce clientorder.Nonce
	copy(nonce[:], req.OrderNonce)
	err := s.orderManager.CancelOrder(ctx, nonce)
	if err != nil {
		return nil, err
	}
	return &clmrpc.ServerCancelOrderResponse{}, nil
}

func (s *Server) SubscribeBatchAuction(
	stream clmrpc.ChannelAuctioneerServer_SubscribeBatchAuctionServer) error {

	return fmt.Errorf("unimplemented")
}

// OrderState returns the of an order as it is currently known to the order
// store.
func (s *Server) OrderState(ctx context.Context,
	req *clmrpc.ServerOrderStateRequest) (*clmrpc.ServerOrderStateResponse,
	error) {

	var nonce clientorder.Nonce
	copy(nonce[:], req.OrderNonce)

	// The state of an order should be reflected in the database so we don't
	// need to ask the manager about it.
	o, err := s.store.GetOrder(ctx, nonce)
	if err != nil {
		return nil, err
	}
	return &clmrpc.ServerOrderStateResponse{
		State: clmrpc.ServerOrderStateResponse_OrderState(
			o.Details().State,
		),
		UnitsUnfulfilled: uint32(o.Details().UnitsUnfulfilled),
	}, nil
}

// parseRPCOrder parses the incoming raw RPC order into the go native data
// types used in the order struct.
func (s *Server) parseRPCOrder(ctx context.Context, version uint32,
	details *clmrpc.ServerOrder) (*clientorder.Kit, *order.Kit, error) {

	var (
		nonce clientorder.Nonce
		err   error
	)

	// Parse the nonce first so we can create the client order kit.
	copy(nonce[:], details.OrderNonce)
	clientKit := clientorder.NewKit(nonce)
	clientKit.Version = clientorder.Version(version)
	clientKit.FixedRate = uint32(details.RateFixed)
	clientKit.Amt = btcutil.Amount(details.Amt)
	clientKit.Units = clientorder.NewSupplyFromSats(clientKit.Amt)
	clientKit.FundingFeeRate = chainfee.SatPerKWeight(
		details.FundingFeeRate,
	)

	// Parse the account key next so we can make sure it exists.
	acctKey, err := btcec.ParsePubKey(details.UserSubKey, btcec.S256())
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse account key: %v",
			err)
	}
	copy(clientKit.AcctKey[:], acctKey.SerializeCompressed())
	_, err = s.store.Account(ctx, clientKit.AcctKey)
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
	nodePubKey, err := btcec.ParsePubKey(details.NodePub, btcec.S256())
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse node pub key: %v",
			err)
	}
	copy(serverKit.NodeKey[:], nodePubKey.SerializeCompressed())
	if len(details.NodeAddr) == 0 {
		return nil, nil, fmt.Errorf("invalid node addresses")
	}
	serverKit.NodeAddrs = make([]net.Addr, 0, len(details.NodeAddr))
	for _, rpcAddr := range details.NodeAddr {
		addr, err := net.ResolveTCPAddr(rpcAddr.Network, rpcAddr.Addr)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to parse node "+
				"ddr: %v", err)
		}
		serverKit.NodeAddrs = append(serverKit.NodeAddrs, addr)
	}
	multiSigPubkey, err := btcec.ParsePubKey(
		details.MultiSigKey, btcec.S256(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse multi sig pub "+
			"key: %v", err)
	}
	copy(serverKit.MultiSigKey[:], multiSigPubkey.SerializeCompressed())
	serverKit.ChanType = order.ChanType(details.ChanType)

	return clientKit, serverKit, nil
}

// mapOrderResp maps the error returned from the order manager into the correct
// RPC return type.
func mapOrderResp(orderNonce clientorder.Nonce, err error) (
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
