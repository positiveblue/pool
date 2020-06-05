package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/agora/client/account"
	"github.com/lightninglabs/agora/client/auctioneer"
	"github.com/lightninglabs/agora/client/clientdb"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"golang.org/x/sync/errgroup"
)

const (
	// getInfoTimeout is the maximum time we allow for the initial getInfo
	// call to the connected lnd node.
	getInfoTimeout = 5 * time.Second
)

// rpcServer implements the gRPC server on the client side and answers RPC calls
// from an end user client program like the command line interface.
type rpcServer struct {
	started uint32 // To be used atomically.
	stopped uint32 // To be used atomically.

	// bestHeight is the best known height of the main chain. This MUST be
	// used atomically.
	bestHeight uint32

	server         *Server
	lndServices    *lndclient.LndServices
	lndClient      lnrpc.LightningClient
	auctioneer     *auctioneer.Client
	accountManager *account.Manager
	orderManager   *order.Manager

	quit            chan struct{}
	wg              sync.WaitGroup
	blockNtfnCancel func()
}

// accountStore is a clientdb.DB wrapper to implement the account.Store
// interface.
type accountStore struct {
	*clientdb.DB
}

var _ account.Store = (*accountStore)(nil)

func (s *accountStore) PendingBatch() error {
	_, _, err := s.DB.PendingBatch()
	return err
}

// newRPCServer creates a new client-side RPC server that uses the given
// connection to the trader's lnd node and the auction server. A client side
// database is created in `serverDir` if it does not yet exist.
func newRPCServer(server *Server) *rpcServer {
	accountStore := &accountStore{server.db}
	lnd := &server.lndServices.LndServices
	return &rpcServer{
		server:      server,
		lndServices: lnd,
		lndClient:   server.lndClient,
		auctioneer:  server.AuctioneerClient,
		accountManager: account.NewManager(&account.ManagerConfig{
			Store:         accountStore,
			Auctioneer:    server.AuctioneerClient,
			Wallet:        lnd.WalletKit,
			Signer:        lnd.Signer,
			ChainNotifier: lnd.ChainNotifier,
			TxSource:      lnd.Client,
		}),
		orderManager: order.NewManager(&order.ManagerConfig{
			Store:     server.db,
			AcctStore: accountStore,
			Lightning: lnd.Client,
			Wallet:    lnd.WalletKit,
			Signer:    lnd.Signer,
		}),
		quit: make(chan struct{}),
	}
}

// Start starts the rpcServer, making it ready to accept incoming requests.
func (s *rpcServer) Start() error {
	if !atomic.CompareAndSwapUint32(&s.started, 0, 1) {
		return nil
	}

	log.Infof("Starting trader server")

	ctx := context.Background()

	lndCtx, lndCancel := context.WithTimeout(ctx, getInfoTimeout)
	defer lndCancel()
	info, err := s.lndServices.Client.GetInfo(lndCtx)
	if err != nil {
		return fmt.Errorf("error in GetInfo: %v", err)
	}

	log.Infof("Connected to lnd node %v with pubkey %v", info.Alias,
		hex.EncodeToString(info.IdentityPubkey[:]))

	var blockCtx context.Context
	blockCtx, s.blockNtfnCancel = context.WithCancel(ctx)
	chainNotifier := s.lndServices.ChainNotifier
	blockChan, blockErrChan, err := chainNotifier.RegisterBlockEpochNtfn(
		blockCtx,
	)
	if err != nil {
		return err
	}

	var height int32
	select {
	case height = <-blockChan:
	case err := <-blockErrChan:
		return fmt.Errorf("unable to receive first block "+
			"notification: %v", err)
	case <-ctx.Done():
		return nil
	}

	s.updateHeight(height)

	// Start the auctioneer client first to establish a connection.
	if err := s.auctioneer.Start(); err != nil {
		return fmt.Errorf("unable to start auctioneer client: %v", err)
	}

	// Start managers.
	if err := s.accountManager.Start(); err != nil {
		return fmt.Errorf("unable to start account manager: %v", err)
	}
	if err := s.orderManager.Start(); err != nil {
		return fmt.Errorf("unable to start order manager: %v", err)
	}

	s.wg.Add(1)
	go s.serverHandler(blockChan, blockErrChan)

	log.Infof("Trader server is now active")

	return nil
}

// Stop stops the server.
func (s *rpcServer) Stop() error {
	if !atomic.CompareAndSwapUint32(&s.stopped, 0, 1) {
		return nil
	}

	log.Info("Trader server stopping")
	s.accountManager.Stop()
	s.orderManager.Stop()
	if err := s.auctioneer.Stop(); err != nil {
		log.Errorf("Error closing server stream: %v")
	}

	close(s.quit)
	s.wg.Wait()
	s.blockNtfnCancel()

	log.Info("Stopped trader server")
	return nil
}

// serverHandler is the main event loop of the server.
func (s *rpcServer) serverHandler(blockChan chan int32, blockErrChan chan error) {
	defer s.wg.Done()

	for {
		select {
		case msg := <-s.auctioneer.FromServerChan:
			// An empty message means the client is shutting down.
			if msg == nil {
				continue
			}

			log.Debugf("Received message from the server: %v", msg)
			err := s.handleServerMessage(msg)
			if err != nil {
				log.Errorf("Error handling server message: %v",
					err)
				err := s.server.Stop()
				if err != nil {
					log.Errorf("Error shutting down: %v",
						err)
				}
			}

		case err := <-s.auctioneer.StreamErrChan:
			// If the server is shutting down, then the client has
			// already scheduled a restart. We only need to handle
			// other errors here.
			if err != nil && err != auctioneer.ErrServerShutdown {
				log.Errorf("Error in server stream: %v", err)
				err := s.auctioneer.HandleServerShutdown(err)
				if err != nil {
					log.Errorf("Error closing stream: %v",
						err)
				}
			}

		case height := <-blockChan:
			log.Infof("Received new block notification: height=%v",
				height)
			s.updateHeight(height)

		case err := <-blockErrChan:
			if err != nil {
				log.Errorf("Unable to receive block "+
					"notification: %v", err)
				err := s.server.Stop()
				if err != nil {
					log.Errorf("Error shutting down: %v",
						err)
				}
			}

		// In case the server is shutting down.
		case <-s.quit:
			return
		}
	}
}

func (s *rpcServer) updateHeight(height int32) {
	// Store height atomically so the incoming request handler can access it
	// without locking.
	atomic.StoreUint32(&s.bestHeight, uint32(height))
}

// connectToMatchedTrader attempts to connect to a trader that we've had an
// order matched with. We'll attempt to establish a permanent connection as
// well, so we can use the connection for any batch retries that may happen.
//
// TODO(roasbeef): if entire batch cancelled, then should remove these
// connections
func connectToMatchedTrader(lndClient lnrpc.LightningClient,
	matchedOrder *order.MatchedOrder) error {

	ctxb := context.Background()
	nodeKey := hex.EncodeToString(matchedOrder.NodeKey[:])

	for _, addr := range matchedOrder.NodeAddrs {
		_, err := lndClient.ConnectPeer(ctxb, &lnrpc.ConnectPeerRequest{
			Addr: &lnrpc.LightningAddress{
				Pubkey: nodeKey,
				Host:   addr.String(),
			},
			Perm: true,
		})

		if err != nil {
			// If we're already connected, then we can stop now.
			if strings.Contains(err.Error(), "already connected") {
				return nil
			}

			log.Warnf("unable to connect to trader at %v@%v",
				nodeKey, addr)

			continue
		}
	}

	// Since we specified perm, the error is async, and not fully
	// communicated to the caller, so we return nil here. Later on, if we
	// can't fund the channel, then we'll fail with a hard error.
	return nil
}

// deriveFundingShim generates the proper funding shim that should be used by
// the maker or taker to properly make a channel that stems off the main batch
// funding transaction.
func (s *rpcServer) deriveFundingShim(ourOrder order.Order,
	matchedOrder *order.MatchedOrder,
	batchTx *wire.MsgTx) (*lnrpc.FundingShim, error) {

	// First, we'll compute the pending channel ID key which will be unique
	// to this order pair.
	var (
		askNonce, bidNonce order.Nonce

		thawHeight uint32
	)
	if ourOrder.Type() == order.TypeBid {
		bidNonce = ourOrder.Nonce()
		askNonce = matchedOrder.Order.Nonce()

		thawHeight = ourOrder.(*order.Bid).MinDuration
	} else {
		bidNonce = matchedOrder.Order.Nonce()
		askNonce = ourOrder.Nonce()

		thawHeight = matchedOrder.Order.(*order.Bid).MinDuration
	}

	// The thaw height is absolute, so we'll need to offset it relative to
	// the current best block from the PoV of our backing node.
	//
	// TODO(roasbeef): this is racey, should have it based on an absolute
	// height?
	nodeInfo, err := s.lndServices.Client.GetInfo(context.Background())
	if err != nil {
		return nil, fmt.Errorf("unable to get best height: %v", err)
	}
	thawHeight += nodeInfo.BlockHeight

	pendingChanID := order.PendingChanKey(
		askNonce, bidNonce,
	)
	chanSize := matchedOrder.UnitsFilled.ToSatoshis()

	// Next, we'll need to find the location of this channel output on the
	// funding transaction, so we'll re-compute the funding script from
	// scratch.
	ctxb := context.Background()
	ourKeyLocator := ourOrder.Details().MultiSigKeyLocator
	ourMultiSigKey, err := s.lndServices.WalletKit.DeriveKey(
		ctxb, &ourKeyLocator,
	)
	if err != nil {
		return nil, err
	}
	_, fundingOutput, err := input.GenFundingPkScript(
		ourMultiSigKey.PubKey.SerializeCompressed(),
		matchedOrder.MultiSigKey[:], int64(chanSize),
	)
	if err != nil {
		return nil, err
	}

	// Now that we have the funding script, we'll find the output index
	// within the batch execution transaction. We ignore the first
	// argument, as earlier during validation, we would've rejected the
	// batch if it wasn't found.
	batchTxID := batchTx.TxHash()
	_, chanOutputIndex := input.FindScriptOutputIndex(
		batchTx, fundingOutput.PkScript,
	)
	chanPoint := &lnrpc.ChannelPoint{
		FundingTxid: &lnrpc.ChannelPoint_FundingTxidBytes{
			FundingTxidBytes: batchTxID[:],
		},
		OutputIndex: chanOutputIndex,
	}

	// With all the components assembled, we'll now create the chan point
	// shim, and register it so we use the proper funding key when we
	// receive the marker's incoming funding request.
	chanPointShim := &lnrpc.ChanPointShim{
		Amt:       int64(chanSize),
		ChanPoint: chanPoint,
		LocalKey: &lnrpc.KeyDescriptor{
			RawKeyBytes: ourMultiSigKey.PubKey.SerializeCompressed(),
			KeyLoc: &lnrpc.KeyLocator{
				KeyFamily: int32(ourKeyLocator.Family),
				KeyIndex:  int32(ourKeyLocator.Index),
			},
		},
		RemoteKey:     matchedOrder.MultiSigKey[:],
		PendingChanId: pendingChanID[:],
		ThawHeight:    thawHeight,
	}

	return &lnrpc.FundingShim{
		Shim: &lnrpc.FundingShim_ChanPointShim{
			ChanPointShim: chanPointShim,
		},
	}, nil
}

// registerFundingShim is used when we're on the taker (our bid was executed)
// side of a new matched order. To prepare ourselves for their incoming funding
// request, we'll register a shim with all the expected parameters.
func (s *rpcServer) registerFundingShim(ourOrder order.Order,
	matchedOrder *order.MatchedOrder, batchTx *wire.MsgTx) error {

	ctxb := context.Background()

	fundingShim, err := s.deriveFundingShim(ourOrder, matchedOrder, batchTx)
	if err != nil {
		return err
	}
	_, err = s.lndClient.FundingStateStep(
		ctxb, &lnrpc.FundingTransitionMsg{
			Trigger: &lnrpc.FundingTransitionMsg_ShimRegister{
				ShimRegister: fundingShim,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("unable to register funding shim: %v", err)
	}

	return nil
}

// prepChannelFunding preps the backing node to either receive or initiate a
// channel funding based on the items in the order batch.
//
// TODO(roasbeef): move?
func (s *rpcServer) prepChannelFunding(batch *order.Batch) error {
	// Now that we know this batch passes our sanity checks, we'll register
	// all the funding shims we need to be able to respond
	for ourOrderNonce, matchedOrders := range batch.MatchedOrders {
		ourOrder, err := s.server.db.GetOrder(ourOrderNonce)
		if err != nil {
			return err
		}

		orderIsAsk := ourOrder.Type() == order.TypeAsk

		// Depending on if this is a ask or not, we'll either just try
		// to connect out, or register the full funding shim.
		for _, matchedOrder := range matchedOrders {
			// We only need to create a shim if we're the taker, so
			// if we had an ask matched, then we can skip this
			// step, as we'll be the ones creating the channel.
			if orderIsAsk {
				// However, if we're the one that needs to make
				// the channel, then we'll kick off a
				// persistent connection request here, so we
				// know a connection should be established by
				// the time we actually need to fund the
				// channel.
				//
				// TODO(roasbeef): info leaks?
				err := connectToMatchedTrader(
					s.lndClient, matchedOrder,
				)
				if err != nil {
					return err
				}

				continue
			}

			// At this point, one of our bids was matched with a
			// series of asks, so we'll now register all the
			// expected funding shims so we can execute the next
			// phase w/o any issues.
			err := s.registerFundingShim(
				ourOrder, matchedOrder, batch.BatchTX,
			)
			if err != nil {
				return fmt.Errorf("unable to register "+
					"funding shim: %v", err)
			}

		}
	}

	return nil
}

// batchChannelSetup will attempt to establish new funding flows with all
// matched takers (people buying our channels) in the passed batch. This method
// will block until the channel is considered pending. Once this phase is
// complete, and the batch execution transaction broadcast, the channel will be
// finalized and locked in.
func (s *rpcServer) batchChannelSetup(batch *order.Batch) error {
	var eg errgroup.Group

	// For each ask order of ours that's matched, we'll make a new funding
	// flow, blocking until they all progress to the final state.
	for ourOrderNonce, matchedOrders := range batch.MatchedOrders {
		ourOrder, err := s.server.db.GetOrder(ourOrderNonce)
		if err != nil {
			return err
		}

		// If this is a bid order, then we don't need to do anything,
		// as we should've already registered the funding shim during
		// the prior phase.
		orderIsBid := ourOrder.Type() == order.TypeBid
		if orderIsBid {
			continue
		}

		// Otherwise, we'll now initiate the funding request to
		// establish all the channels generated by this order with the
		// remote parties.
		for _, matchedOrder := range matchedOrders {
			// Before we make the funding request, we'll make sure
			// that we're connected to the other party.
			err := connectToMatchedTrader(
				s.lndClient, matchedOrder,
			)
			if err != nil {
				return fmt.Errorf("unable to connect to "+
					"trader: %v", err)
			}

			fundingShim, err := s.deriveFundingShim(
				ourOrder, matchedOrder, batch.BatchTX,
			)
			if err != nil {
				return err
			}

			// Now that we know we're connected, we'll launch off
			// the request to initiate channel funding with the
			// remote peer.
			//
			// TODO(roasbeef): sat per byte from order?
			//  * also other params to set as well
			chanAmt := int64(matchedOrder.UnitsFilled.ToSatoshis())
			fundingReq := &lnrpc.OpenChannelRequest{
				NodePubkey:         matchedOrder.NodeKey[:],
				LocalFundingAmount: chanAmt,
				FundingShim:        fundingShim,
			}
			chanStream, err := s.lndClient.OpenChannel(
				context.Background(),
				fundingReq,
			)
			if err != nil {
				return err
			}

			// We'll launch a new goroutine to wait until chan
			// pending (funding flow finished) update has been
			// sent.
			eg.Go(func() error {
				for {
					select {

					case <-s.quit:
						return fmt.Errorf("server " +
							"shutting down")
					default:
					}

					msg, err := chanStream.Recv()
					if err != nil {
						log.Errorf("unable to read "+
							"chan open update event: %v", err)
						return err
					}

					_, ok := msg.Update.(*lnrpc.OpenStatusUpdate_ChanPending)
					if ok {
						return nil
					}
				}
			})
		}
	}

	return eg.Wait()
}

// handleServerMessage reads a gRPC message received in the stream from the
// auctioneer server and passes it to the correct manager.
func (s *rpcServer) handleServerMessage(rpcMsg *clmrpc.ServerAuctionMessage) error {
	switch msg := rpcMsg.Msg.(type) {
	// A new batch has been assembled with some of our orders.
	case *clmrpc.ServerAuctionMessage_Prepare:
		// Parse and formally validate what we got from the server.
		log.Tracef("Received prepare msg from server, batch_id=%x: %v",
			msg.Prepare.BatchId, spew.Sdump(msg))
		batch, err := order.ParseRPCBatch(msg.Prepare)
		if err != nil {
			return fmt.Errorf("error parsing RPC batch: %v", err)
		}

		// Do an in-depth verification of the batch.
		err = s.orderManager.OrderMatchValidate(batch)
		if err != nil {
			// We can't accept the batch, something went wrong.
			log.Errorf("Error validating batch: %v", err)
			return s.sendRejectBatch(batch, err)
		}

		// TODO(roasbeef): make sure able to connect out to peers
		// before sending accept?
		//  * also need to handle reject on the server-side

		// Before we accept the batch, we'll finish preparations on our
		// end which include connecting out to peers, and registering
		// funding shim.
		err = s.prepChannelFunding(batch)
		if err != nil {
			log.Errorf("Error preparing channel funding: %v", err)
			return s.sendRejectBatch(batch, err)
		}

		// Accept the match now.
		//
		// TODO(guggero): Give user the option to bail out of any order
		// up to this point?
		err = s.sendAcceptBatch(batch)
		if err != nil {
			log.Errorf("Error sending accept msg: %v", err)
			return s.sendRejectBatch(batch, err)
		}

	case *clmrpc.ServerAuctionMessage_Sign:
		// We were able to accept the batch. Inform the auctioneer,
		// then start negotiating with the remote peers. We'll sign
		// once all channel partners have responded.
		batch := s.orderManager.PendingBatch()
		err := s.batchChannelSetup(batch)
		if err != nil {
			log.Errorf("Error setting up channels: %v", err)
			return s.sendRejectBatch(batch, err)
		}

		// Sign for the accounts in the batch.
		sigs, err := s.orderManager.BatchSign()
		if err != nil {
			log.Errorf("Error signing batch: %v", err)
			return s.sendRejectBatch(batch, err)
		}
		err = s.sendSignBatch(batch, sigs)
		if err != nil {
			log.Errorf("Error sending sign msg: %v", err)
			return s.sendRejectBatch(batch, err)
		}

	// The previously prepared batch has been executed and we can finalize
	// it by opening the channel and persisting the account and order diffs.
	case *clmrpc.ServerAuctionMessage_Finalize:
		log.Tracef("Received finalize msg from server, batch_id=%x: %v",
			msg.Finalize.BatchId, spew.Sdump(msg))

		var batchID order.BatchID
		copy(batchID[:], msg.Finalize.BatchId)
		return s.orderManager.BatchFinalize(batchID)

	default:
		return fmt.Errorf("unknown server message: %v", msg)
	}

	return nil
}

func (s *rpcServer) InitAccount(ctx context.Context,
	req *clmrpc.InitAccountRequest) (*clmrpc.Account, error) {

	account, err := s.accountManager.InitAccount(
		ctx, btcutil.Amount(req.AccountValue), req.AccountExpiry,
		atomic.LoadUint32(&s.bestHeight),
	)
	if err != nil {
		return nil, err
	}

	return marshallAccount(account)
}

func (s *rpcServer) ListAccounts(ctx context.Context,
	req *clmrpc.ListAccountsRequest) (*clmrpc.ListAccountsResponse, error) {

	accounts, err := s.server.db.Accounts()
	if err != nil {
		return nil, err
	}

	rpcAccounts := make([]*clmrpc.Account, 0, len(accounts))
	for _, account := range accounts {
		rpcAccount, err := marshallAccount(account)
		if err != nil {
			return nil, err
		}
		rpcAccounts = append(rpcAccounts, rpcAccount)
	}

	return &clmrpc.ListAccountsResponse{
		Accounts: rpcAccounts,
	}, nil
}

func marshallAccount(a *account.Account) (*clmrpc.Account, error) {
	var rpcState clmrpc.AccountState
	switch a.State {
	case account.StateInitiated, account.StatePendingOpen:
		rpcState = clmrpc.AccountState_PENDING_OPEN

	case account.StatePendingUpdate:
		rpcState = clmrpc.AccountState_PENDING_UPDATE

	case account.StateOpen:
		rpcState = clmrpc.AccountState_OPEN

	case account.StateExpired:
		rpcState = clmrpc.AccountState_EXPIRED

	case account.StatePendingClosed:
		rpcState = clmrpc.AccountState_PENDING_CLOSED

	case account.StateClosed:
		rpcState = clmrpc.AccountState_CLOSED

	default:
		return nil, fmt.Errorf("unknown state %v", a.State)
	}

	var closeTxHash chainhash.Hash
	if a.CloseTx != nil {
		closeTxHash = a.CloseTx.TxHash()
	}

	return &clmrpc.Account{
		TraderKey: a.TraderKey.PubKey.SerializeCompressed(),
		Outpoint: &clmrpc.OutPoint{
			Txid:        a.OutPoint.Hash[:],
			OutputIndex: a.OutPoint.Index,
		},
		Value:            uint64(a.Value),
		ExpirationHeight: a.Expiry,
		State:            rpcState,
		CloseTxid:        closeTxHash[:],
	}, nil
}

// WithdrawAccount handles a trader's request to withdraw funds from the
// specified account by spending the current account output to the specified
// outputs.
func (s *rpcServer) WithdrawAccount(ctx context.Context,
	req *clmrpc.WithdrawAccountRequest) (*clmrpc.WithdrawAccountResponse, error) {

	// Ensure the trader key is well formed.
	traderKey, err := btcec.ParsePubKey(req.TraderKey, btcec.S256())
	if err != nil {
		return nil, err
	}

	// Ensure the outputs we'll withdraw to are well formed.
	if len(req.Outputs) == 0 {
		return nil, errors.New("missing outputs for withdrawal")
	}
	outputs, err := s.parseRPCOutputs(req.Outputs)
	if err != nil {
		return nil, err
	}

	// Enforce a minimum fee rate of 1 sat/vbyte.
	feeRate := chainfee.SatPerKVByte(req.SatPerVbyte * 1000).FeePerKWeight()
	if feeRate < chainfee.FeePerKwFloor {
		log.Infof("Manual fee rate input of %d sat/kw is too low, "+
			"using %d sat/kw instead", feeRate,
			chainfee.FeePerKwFloor)
		feeRate = chainfee.FeePerKwFloor
	}

	// Proceed to process the withdrawal and map its response to the RPC's
	// response.
	modifiedAccount, tx, err := s.accountManager.WithdrawAccount(
		ctx, traderKey, outputs, feeRate,
		atomic.LoadUint32(&s.bestHeight),
	)
	if err != nil {
		return nil, err
	}

	rpcModifiedAccount, err := marshallAccount(modifiedAccount)
	if err != nil {
		return nil, err
	}
	txHash := tx.TxHash()

	return &clmrpc.WithdrawAccountResponse{
		Account:      rpcModifiedAccount,
		WithdrawTxid: txHash[:],
	}, nil
}

// CloseAccount handles a trader's request to close the specified account.
func (s *rpcServer) CloseAccount(ctx context.Context,
	req *clmrpc.CloseAccountRequest) (*clmrpc.CloseAccountResponse, error) {

	traderKey, err := btcec.ParsePubKey(req.TraderKey, btcec.S256())
	if err != nil {
		return nil, err
	}

	var closeOutputs []*wire.TxOut
	if len(req.Outputs) > 0 {
		closeOutputs, err = s.parseRPCOutputs(req.Outputs)
		if err != nil {
			return nil, err
		}
	}

	closeTx, err := s.accountManager.CloseAccount(
		ctx, traderKey, closeOutputs, atomic.LoadUint32(&s.bestHeight),
	)
	if err != nil {
		return nil, err
	}
	closeTxHash := closeTx.TxHash()

	return &clmrpc.CloseAccountResponse{
		CloseTxid: closeTxHash[:],
	}, nil
}

// parseRPCOutputs maps []*clmrpc.Output -> []*wire.TxOut.
func (s *rpcServer) parseRPCOutputs(outputs []*clmrpc.Output) ([]*wire.TxOut,
	error) {

	res := make([]*wire.TxOut, 0, len(outputs))
	for _, output := range outputs {
		// Decode each address, make sure it's valid for the current
		// network, and derive its output script.
		addr, err := btcutil.DecodeAddress(
			output.Address, s.lndServices.ChainParams,
		)
		if err != nil {
			return nil, err
		}
		if !addr.IsForNet(s.lndServices.ChainParams) {
			return nil, fmt.Errorf("invalid address %v for %v "+
				"network", addr.String(),
				s.lndServices.ChainParams.Name)
		}
		outputScript, err := txscript.PayToAddrScript(addr)
		if err != nil {
			return nil, err
		}

		res = append(res, &wire.TxOut{
			Value:    int64(output.Value),
			PkScript: outputScript,
		})
	}

	return res, nil
}

func (s *rpcServer) RecoverAccounts(_ context.Context,
	_ *clmrpc.RecoverAccountsRequest) (*clmrpc.RecoverAccountsResponse, error) {

	return nil, fmt.Errorf("unimplemented")
}

// SubmitOrder assembles all the information that is required to submit an order
// from the trader's lnd node, signs it and then sends the order to the server
// to be included in the auctioneer's order book.
func (s *rpcServer) SubmitOrder(ctx context.Context,
	req *clmrpc.SubmitOrderRequest) (*clmrpc.SubmitOrderResponse, error) {

	var o order.Order
	switch requestOrder := req.Details.(type) {
	case *clmrpc.SubmitOrderRequest_Ask:
		a := requestOrder.Ask
		kit, err := order.ParseRPCOrder(a.Version, a.Details)
		if err != nil {
			return nil, err
		}
		o = &order.Ask{
			Kit:         *kit,
			MaxDuration: a.MaxDurationBlocks,
		}

	case *clmrpc.SubmitOrderRequest_Bid:
		b := requestOrder.Bid
		kit, err := order.ParseRPCOrder(b.Version, b.Details)
		if err != nil {
			return nil, err
		}
		o = &order.Bid{
			Kit:         *kit,
			MinDuration: b.MinDurationBlocks,
		}

	default:
		return nil, fmt.Errorf("invalid order request")
	}

	// Verify that the account exists.
	acctKey, err := btcec.ParsePubKey(
		o.Details().AcctKey[:], btcec.S256(),
	)
	if err != nil {
		return nil, err
	}
	acct, err := s.server.db.Account(acctKey)
	if err != nil {
		return nil, fmt.Errorf("cannot accept order: %v", err)
	}

	// Collect all the order data and sign it before sending it to the
	// auction server.
	serverParams, err := s.orderManager.PrepareOrder(ctx, o, acct)
	if err != nil {
		return nil, err
	}

	// Send the order to the server. If this fails, then the order is
	// certain to never get into the order book. We don't need to keep it
	// around in that case.
	err = s.auctioneer.SubmitOrder(ctx, o, serverParams)
	if err != nil {
		// TODO(guggero): Put in state failed instead of removing?
		if err2 := s.server.db.DelOrder(o.Nonce()); err2 != nil {
			log.Errorf("Could not delete failed order: %v", err2)
		}

		// If there was something wrong with the information the user
		// provided, then return this as a nice string instead of an
		// error type.
		if userErr, ok := err.(*order.UserError); ok {
			log.Warnf("Invalid order details: %v", userErr)

			return &clmrpc.SubmitOrderResponse{
				Details: &clmrpc.SubmitOrderResponse_InvalidOrder{
					InvalidOrder: userErr.Details,
				},
			}, nil
		}

		// Any other error we return normally as a gRPC status level
		// error.
		return nil, fmt.Errorf("error submitting order to auctioneer: "+
			"%v", err)
	}

	log.Infof("New order submitted: nonce=%v, type=%v", o.Nonce(), o.Type())

	// ServerOrder is accepted.
	orderNonce := o.Nonce()
	return &clmrpc.SubmitOrderResponse{
		Details: &clmrpc.SubmitOrderResponse_AcceptedOrderNonce{
			AcceptedOrderNonce: orderNonce[:],
		},
	}, nil
}

// ListOrders returns a list of all orders that is currently known to the trader
// client's local store. The state of each order is queried on the auction
// server and returned as well.
func (s *rpcServer) ListOrders(ctx context.Context, _ *clmrpc.ListOrdersRequest) (
	*clmrpc.ListOrdersResponse, error) {

	// Get all orders from our local store first.
	dbOrders, err := s.server.db.GetOrders()
	if err != nil {
		return nil, err
	}

	// The RPC is split by order type so we have to separate them now.
	asks := make([]*clmrpc.Ask, 0, len(dbOrders))
	bids := make([]*clmrpc.Bid, 0, len(dbOrders))
	for _, dbOrder := range dbOrders {
		nonce := dbOrder.Nonce()

		// Ask the server about the order's current status.
		orderStateResp, err := s.auctioneer.OrderState(ctx, nonce)
		if err != nil {
			return nil, fmt.Errorf("unable to query order state on"+
				"server for order %v: %v", nonce.String(), err)
		}

		dbDetails := dbOrder.Details()
		details := &clmrpc.Order{
			TraderKey:        dbDetails.AcctKey[:],
			RateFixed:        dbDetails.FixedRate,
			Amt:              uint64(dbDetails.Amt),
			FundingFeeRate:   uint64(dbDetails.FundingFeeRate),
			OrderNonce:       nonce[:],
			State:            orderStateResp.State,
			Units:            uint32(dbDetails.Units),
			UnitsUnfulfilled: orderStateResp.UnitsUnfulfilled,
		}

		switch o := dbOrder.(type) {
		case *order.Ask:
			rpcAsk := &clmrpc.Ask{
				Details:           details,
				MaxDurationBlocks: o.MaxDuration,
				Version:           uint32(o.Version),
			}
			asks = append(asks, rpcAsk)

		case *order.Bid:
			rpcBid := &clmrpc.Bid{
				Details:           details,
				MinDurationBlocks: o.MinDuration,
				Version:           uint32(o.Version),
			}
			bids = append(bids, rpcBid)

		default:
			return nil, fmt.Errorf("unknown order type: %v",
				o)
		}
	}
	return &clmrpc.ListOrdersResponse{
		Asks: asks,
		Bids: bids,
	}, nil
}

// CancelOrder cancels the order on the server and updates the state of the
// local order accordingly.
func (s *rpcServer) CancelOrder(ctx context.Context,
	req *clmrpc.CancelOrderRequest) (*clmrpc.CancelOrderResponse, error) {

	var nonce order.Nonce
	copy(nonce[:], req.OrderNonce)
	err := s.auctioneer.CancelOrder(ctx, nonce)
	if err != nil {
		return nil, err
	}
	return &clmrpc.CancelOrderResponse{}, nil
}

// sendRejectBatch sends a reject message to the server with the properly
// decoded reason code and the full reason message as a string.
func (s *rpcServer) sendRejectBatch(batch *order.Batch, failure error) error {
	msg := &clmrpc.ClientAuctionMessage_Reject{
		Reject: &clmrpc.OrderMatchReject{
			BatchId: batch.ID[:],
			Reason:  failure.Error(),
		},
	}

	// Attach the status code to the message to give a bit more context.
	switch {
	case errors.Is(failure, order.ErrVersionMismatch):
		msg.Reject.ReasonCode = clmrpc.OrderMatchReject_BATCH_VERSION_MISMATCH

	case errors.Is(failure, order.ErrMismatchErr):
		msg.Reject.ReasonCode = clmrpc.OrderMatchReject_SERVER_MISBEHAVIOR

	default:
		msg.Reject.ReasonCode = clmrpc.OrderMatchReject_UNKNOWN
	}
	log.Infof("Sending batch rejection message for batch %x with "+
		"code %v and message: %v", batch.ID, msg.Reject.ReasonCode,
		failure)

	// Send the message to the server. If a new error happens we return that
	// one because we know the causing error has at least been logged at
	// some point before.
	err := s.auctioneer.SendAuctionMessage(&clmrpc.ClientAuctionMessage{
		Msg: msg,
	})
	if err != nil {
		return fmt.Errorf("error sending reject message: %v", err)
	}
	return failure
}

// sendAcceptBatch sends an accept message to the server with the list of order
// nonces that we accept in the batch.
func (s *rpcServer) sendAcceptBatch(batch *order.Batch) error {
	// Prepare the list of nonces we accept by serializing them to a slice
	// of byte slices.
	nonces := make([][]byte, 0, len(batch.MatchedOrders))
	for nonce := range batch.MatchedOrders {
		nonces = append(nonces, nonce[:])
	}

	// Send the message to the server.
	return s.auctioneer.SendAuctionMessage(&clmrpc.ClientAuctionMessage{
		Msg: &clmrpc.ClientAuctionMessage_Accept{
			Accept: &clmrpc.OrderMatchAccept{
				BatchId:    batch.ID[:],
				OrderNonce: nonces,
			},
		},
	})
}

// sendSignBatch sends a sign message to the server with the witness stacks of
// all accounts that are involved in the batch.
func (s *rpcServer) sendSignBatch(batch *order.Batch,
	sigs order.BatchSignature) error {

	// Prepare the list of witness stack messages and send them to the
	// server.
	rpcSigs := make(map[string][]byte)
	for acctKey, sig := range sigs {
		key := hex.EncodeToString(acctKey[:])
		rpcSigs[key] = sig.Serialize()
	}
	return s.auctioneer.SendAuctionMessage(&clmrpc.ClientAuctionMessage{
		Msg: &clmrpc.ClientAuctionMessage_Sign{
			Sign: &clmrpc.OrderMatchSign{
				BatchId:     batch.ID[:],
				AccountSigs: rpcSigs,
			},
		},
	})
}
