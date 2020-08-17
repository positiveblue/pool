package subasta

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/llm/clmscript"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/subscribe"
)

var (
	// errTxNotFound is an error returned when we attempt to locate a
	// transaction but we are unable to find it.
	errTxNotFound = errors.New("transaction not found")
)

// AuctioneerDatabase is the primary database that will be used by the
// auctioneer. It contains all the read/write methods the auctioneer needs to
// conduct the auction.
type AuctioneerDatabase interface {
	// UpdateAuctioneerAccount inserts the latest auctioneer account state
	// into the database.
	UpdateAuctioneerAccount(context.Context, *account.Auctioneer) error

	// FetchAuctioneerAccount fetches the latest auctioneer account from
	// disk. If the account isn't found, then ErrNoAuctioneerAccount should
	// be returned.
	FetchAuctioneerAccount(context.Context) (*account.Auctioneer, error)

	// BatchKey returns the current per-batch key.
	BatchKey(context.Context) (*btcec.PublicKey, error)

	// UpdateAuctionState updates the current state of the auction.
	//
	// NOTE: This state doesn't need to be persisted, but it should be
	// durable during the lifetime of this interface. This method is used
	// mainly to make testing state transition in the auction easier.
	UpdateAuctionState(AuctionState) error

	// AuctionState returns the current state of the auction. If no state
	// modification have been made, then this method should return the
	// default state.
	//
	// NOTE: This state doesn't need to be persisted. This method is used
	// mainly to make testing state transition in the auction easier.
	AuctionState() (AuctionState, error)

	// GetOrders reads all the current active orders from disk.
	GetOrders(context.Context) ([]order.ServerOrder, error)

	// GetOrder attempts to retrieve an order from disk based on the order
	// nonce.
	GetOrder(context.Context, orderT.Nonce) (order.ServerOrder, error)

	// ConfirmBatch marks the batch as finalized, meaning we no longer need
	// to attempt to re-broadcast it. A batch is only finalized after its
	// batch execution transaction has confirmed.
	ConfirmBatch(context.Context, orderT.BatchID) error

	// BatchConfirmed returns true if the target batch has been marked finalized
	// (confirmed) on disk.
	BatchConfirmed(context.Context, orderT.BatchID) (bool, error)

	// GetBatchSnapshot returns the self-contained snapshot of a batch with
	// the given ID as it was recorded at the time.
	GetBatchSnapshot(context.Context,
		orderT.BatchID) (*matching.OrderBatch, *wire.MsgTx, error)

	// BanAccount attempts to ban the account associated with a trader
	// starting from the current height of the chain. The duration of the
	// ban will depend on how many times the node has been banned before and
	// grows exponentially, otherwise it is 144 blocks.
	BanAccount(context.Context, *btcec.PublicKey, uint32) error
}

// Wallet is an interface that contains all the methods necessary for the
// auctioneer to carry out its duties.
type Wallet interface {
	// SendOutputs creates a transaction creating the specified set of
	// outputs at the target fee rate.
	SendOutputs(context.Context, []*wire.TxOut,
		chainfee.SatPerKWeight) (*wire.MsgTx, error)

	// ConfirmedWalletBalance returns the total amount of confirmed coins
	// in the wallet.
	ConfirmedWalletBalance(context.Context) (btcutil.Amount, error)

	// ListTransactions returns the set of confirmed transactions in the
	// wallet.
	ListTransactions(context.Context) ([]*wire.MsgTx, error)

	// DeriveNextKey derives the next key specified of the given family.
	DeriveNextKey(context.Context, int32) (*keychain.KeyDescriptor, error)

	// PublishTransaction attempts to publish the target transaction.
	PublishTransaction(ctx context.Context, tx *wire.MsgTx) error

	// EstimateFee gets a fee rate estimate for the confirmation target.
	EstimateFee(context.Context, int32) (chainfee.SatPerKWeight, error)
}

// OrderFeed is an interface that represents a live feed to the order book.
// We'll use this to subscribe to any updates so we can ensure we're always
// attempting to clear the market with the latest set of orders.
type OrderFeed interface {
	// Subscribe returns a new subscription client that will send out
	// updates each time an order is canceled or removed.
	Subscribe() (*subscribe.Client, error)
}

// BatchExecutor is an interface that represent the subs-system which will
// contact all the traders to obtain signatures for a valid batch execution
// transaction.
type BatchExecutor interface {
	// Submit submits the target batch for execution. If the batch is
	// invalid, then an error should be returned.
	Submit(*matching.OrderBatch, orderT.FeeSchedule,
		chainfee.SatPerKWeight) (chan *venue.ExecutionResult, error)
}

// ChannelEnforcer is responsible for enforcing channel service lifetime
// packages that result from an auction's matched order pair. These channels
// each specify a relative maturity height from the channel's confirmation
// height. Violations to the channel's service lifetime occur upon a confirmed
// commitment broadcast before said height, assuming that the bidder (buyer of
// the channel) rejects any cooperative close attempts from the asker (seller of
// the channel).
type ChannelEnforcer interface {
	// EnforceChannelLifetimes enforces the service lifetime for a series of
	// channels. If a premature spend happens for any of these, then the
	// responsible trader is punished accordingly.
	EnforceChannelLifetimes(pkgs ...*chanenforcement.LifetimePackage) error
}

// AuctioneerConfig contains all the interfaces the auctioneer needs to carry
// out its duties.
type AuctioneerConfig struct {
	// DB is the primary database of the auctioneer.
	DB AuctioneerDatabase

	// ChainNotifier is used to register for confirmations and watch for
	// spends of the main auctioneer account.
	ChainNotifier lndclient.ChainNotifierClient

	// Wallet will be used to fund the auctioneer's account and also
	// monitor the total balance of the system.
	Wallet Wallet

	// StartingAcctValue is the amount of coins we'll use to fund our
	// account if we find that no account exists on disk and in the chain.
	StartingAcctValue btcutil.Amount

	// BatchTicker is a ticker that ticks each time we should attempt to
	// make a batch clearing attempt. We use a force ticker here as it lets
	// us execute ticks at will, both in tests and by the system admin.
	BatchTicker *IntervalAwareForceTicker

	// CallMarket is the underlying market that we'll be operating. We'll
	// use this to actually perform making when the time comes.
	CallMarket matching.BatchAuctioneer

	// OrderFeed points to the live order feed. This is used to receive
	// updates each time the order book changes.
	OrderFeed OrderFeed

	// BatchExecutor is the sub-system responsible for actually executing a
	// batch, or gathering all the account signatures we need to actually
	// broadcast a batch execution transaction.
	BatchExecutor BatchExecutor

	// FeeSchedule describes how we charge the traders in an executed
	// batch.
	FeeSchedule orderT.FeeSchedule

	// ChannelEnforcer enforces the service lifetime of channels created as
	// part of a finalized batch.
	ChannelEnforcer ChannelEnforcer

	// ConfTarget is the confirmation target we'll use to get fee estimates
	// for onchain transactions.
	ConfTarget int32
}

// orderFeederState is the current state of the order feeder goroutine. It will
// either deliver new updates for orders, or cache them, until it goes back to
// the deliver state.
type orderFeederState uint8

const (
	// orderFeederDeliver is the starting state of the orderFeeder, in this
	// state it will continue to deliver updates of new orders.
	orderFeederDeliver orderFeederState = iota

	// orderFeederPause is the state that stops the order feeder from
	// sending out new order update notifications. When we go back to the
	// orderFeederDeliver state, any updates we cached should be
	// re-delivered
	orderFeederPause
)

// Auctioneer is the primary state machine of the auction. This struct manages
// the entire lifestyle of the auction from order submission to clearing, and
// finally execution.
type Auctioneer struct {
	// bestHeight tracks the current known best height.
	//
	// NOTE: This field MUST be used atomically.
	bestHeight uint32

	// cfg is the primary config of the Auctioneer.
	cfg AuctioneerConfig

	// auctionEvents is a channel where all the new auction related events
	// (externally triggered) will be sent over.
	auctionEvents chan EventTrigger

	// watchAccountConfOnce is used to watch for the confirmation of the
	// master account. This is only ever really used when the system is
	// first initialized.
	watchAccountConfOnce sync.Once

	// blockNtfnCancel is a closure used to cancel our block epoch stream.
	blockNtfnCancel func()

	// eligibleBatch is a pointer used to store the current pending
	// eligible batch. This may be over-written if we need to create new
	// sub-batches due to errors. If there're no issues, then this will
	// become the finalizedBatch.
	eligibleBatch *matching.OrderBatch

	// batchFeeRate is the fee rate we have decided to use for the eligible
	// batch.
	batchFeeRate chainfee.SatPerKWeight

	// pendingBatchID is the batch ID for the batch we're attempting to
	// execute.
	pendingBatchID matching.BatchID

	// pendingBatchIDMtx is a mutex that guards the pending batch ID from
	// concurrent access.
	pendingBatchIDMtx sync.Mutex

	// finalizedBatch points to the final batch of this epoch. Once this is
	// non-nil, then we'll advance to the BatchCommitState where we'll
	// conclude this batch run, and wait for another tick.
	finalizedBatch *venue.ExecutionResult

	// orderFeederSignals is a channel used to control the actions of the
	// orderFeeder goroutine. We'll toggle to either a mode where it
	// continually delivers need modifications, or stash them to deliver a
	// backlog once we go back to the main delivery mode.
	orderFeederSignals chan orderFeederState

	// removedOrders is a map used to store the nonces of orders removed
	// between attempts to execute a batch. Once the batch is successfully
	// executed, we'll restore these to the main call market.
	removedOrders map[orderT.Nonce]struct{}

	// batchRetry is a bool that indicates a special state transition from
	// match making state to the order submit state. This allows us to
	// avoid an infinite loop between these two states when there isn't a
	// market to clear.
	//
	// TODO(roasbeef): other solutions: emit own triggers, pre-check?
	batchRetry bool

	quit chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once

	wg sync.WaitGroup
}

// NewAuctioneer returns a new instance of the auctioneer given a fully
// populated config struct.
func NewAuctioneer(cfg AuctioneerConfig) *Auctioneer {
	return &Auctioneer{
		cfg:                cfg,
		auctionEvents:      make(chan EventTrigger),
		quit:               make(chan struct{}),
		orderFeederSignals: make(chan orderFeederState),
		removedOrders:      make(map[orderT.Nonce]struct{}),
	}
}

// loadDiwe shouskOrders loads all the orders disk, and adds them to the current call
// market.
func (a *Auctioneer) loadDiskOrders() error {
	// We'll read all the active order from disk and add them one by one to
	// the current active call market.
	activeOrders, err := a.cfg.DB.GetOrders(context.Background())
	if err != nil {
		return err
	}

	for _, activeOrder := range activeOrders {
		switch o := activeOrder.(type) {

		case *order.Bid:
			if err := a.cfg.CallMarket.ConsiderBids(o); err != nil {
				return err
			}

		case *order.Ask:
			if err := a.cfg.CallMarket.ConsiderAsks(o); err != nil {
				return err
			}
		}
	}

	return nil
}

// Start launches all goroutines the auctioneer needs to perform its duties.
func (a *Auctioneer) Start() error {
	var startErr error

	a.startOnce.Do(func() {
		log.Infof("Starting main Auctioneer State Machine")

		// First, we'll rebroadcast the batch execution transactions of
		// any pending batches to ensure they'll eventually be
		// confirmed.
		if err := a.rebroadcastPendingBatches(); err != nil {
			startErr = err
			return
		}

		// Before we start up any other sub-system, we'll load all the
		// orders from disk, and register them with the internal
		// discrete call market.
		if err := a.loadDiskOrders(); err != nil {
			startErr = err
			return
		}

		// First, we'll need to grab a new subscription from the order book
		// itself, which will remain active until we exit.
		subscription, err := a.cfg.OrderFeed.Subscribe()
		if err != nil {
			log.Errorf("unable to get new orders: %v", err)
			startErr = err
			return
		}

		// Next, we'll create out block epoch stream subscription to be
		// used for telling block time.
		notifier := a.cfg.ChainNotifier
		ctx, blockNtfnCancel := context.WithCancel(context.Background())
		newBlockChan, blockErrChan, err := notifier.RegisterBlockEpochNtfn(
			ctx,
		)
		a.blockNtfnCancel = blockNtfnCancel
		if err != nil {
			startErr = err
			return
		}

		// To conclude, we'll now kick off the main state machine and
		// launch the primary auction coordinator.
		//
		// TODO(roasbeef): no need to try and state step here?
		dbState, err := a.cfg.DB.AuctionState()
		if err != nil {
			startErr = err
			return
		}
		startingState, err := a.stateStep(dbState, &initEvent{})
		if err != nil {
			startErr = err
			return
		}

		log.Infof("Auctioneer starting at state: %v", startingState)

		err = a.cfg.DB.UpdateAuctionState(startingState)
		if err != nil {
			startErr = err
			return
		}

		a.wg.Add(1)
		go a.auctionCoordinator(newBlockChan, blockErrChan, subscription)
	})

	return startErr
}

// Stop signals the auctioneer to halt all actions, and enter a graceful
// shutdown phase.
func (a *Auctioneer) Stop() error {
	var stopErr error

	a.stopOnce.Do(func() {
		log.Infof("Stopping main Auctioneer State Machine")

		close(a.quit)

		a.wg.Wait()

		if a.blockNtfnCancel != nil {
			a.blockNtfnCancel()
		}
	})

	return stopErr
}

// BestHeight returns the current known best height from the PoV of the
// auctioneer.
func (a *Auctioneer) BestHeight() uint32 {
	return atomic.LoadUint32(&a.bestHeight)
}

// publishBatchTx attempts to publish (or re-publish) the batch execution
// transaction. Was confirmed, a goroutine will mark the batch as confirmed on
// disk.
func (a *Auctioneer) publishBatchTx(ctx context.Context, batchTx *wire.MsgTx,
	batchID orderT.BatchID) error {

	log.Infof("Publishing batch transaction (txid=%v) for Batch(%x)",
		batchTx.TxHash(), batchID[:])

	// First, we'll publish the batch transaction, so it'll be eligible to
	// be included in a block.
	err := a.cfg.Wallet.PublishTransaction(ctx, batchTx)
	if err != nil {
		return fmt.Errorf("unable to publish batch tx: %v",
			err)
	}

	// With the transaction broadcast, we'll now register for a
	// confirmation of the BET.
	batchTxHash := batchTx.TxHash()
	confChan, errChan, err := a.cfg.ChainNotifier.RegisterConfirmationsNtfn(
		ctx, &batchTxHash, batchTx.TxOut[0].PkScript, 1, 1,
	)
	if err != nil {
		return fmt.Errorf("unable to get conf event for "+
			"batch tx: %v", err)
	}

	a.wg.Add(1)
	go func() {
		a.wg.Done()

		select {
		case <-a.quit:
			return

		// Now that the batch transaction has confirmed, we'll mark it
		// as finalized on-disk, so we don't try to re-broadcast it any
		// longer.
		case <-confChan:
			log.Infof("Batch(%x) has been confirmed on chain",
				batchID[:])

			err := a.cfg.DB.ConfirmBatch(
				context.Background(), batchID,
			)
			if err != nil {
				log.Errorf("unable to finalize "+
					"batch: %v", err)
			}

		case <-errChan:
			log.Errorf("unable to register for conf: %v", err)
		}
	}()

	return nil
}

// rebroadcastPendingBatches attempts to rebroadcast any pending batches to
// ensure they're confirmed. Once the batch confirms, it'll be marked as
// finalized on disk.
func (a *Auctioneer) rebroadcastPendingBatches() error {
	// First, we'll fetch the current batch key, as we'll use this to walk
	// backwards to find all the batches that are still pending.
	ctxb := context.Background()
	currentBatchKey, err := a.cfg.DB.BatchKey(ctxb)
	if err != nil {
		return err
	}

	// We keep a list of the batches to re-publish. To ensure propagation,
	// we publish them in the order they were created.
	type batch struct {
		tx *wire.MsgTx
		id orderT.BatchID
	}
	var batches []batch

	for {
		// Now that we have the current batch key, we'll walk
		// "backwards" by decrementing the batch key by -G each time.
		currentBatchKey = clmscript.DecrementKey(currentBatchKey)

		// Now for this batch key, we'll check to see if the batch has
		// been marked as finalized on disk or not.
		var batchID orderT.BatchID
		copy(batchID[:], currentBatchKey.SerializeCompressed())
		batchConfirmed, err := a.cfg.DB.BatchConfirmed(ctxb, batchID)
		if err != nil && err != subastadb.ErrNoBatchExists {
			return err
		}

		// If this batch is confirmed (or a batch has never existed),
		// then we can exit early.
		if batchConfirmed || err == subastadb.ErrNoBatchExists {
			break
		}

		// Now that we know this batch isn't finalized, we'll fetch the
		// batch transaction from disk so we can rebroadcast it.
		var priorBatchID orderT.BatchID
		copy(priorBatchID[:], currentBatchKey.SerializeCompressed())
		_, batchTx, err := a.cfg.DB.GetBatchSnapshot(ctxb, priorBatchID)
		if err != nil {
			return err
		}

		batches = append(batches, batch{
			tx: batchTx,
			id: batchID,
		})
	}

	log.Infof("Rebroadcasting %d unconfirmed batch transactions",
		len(batches))

	// Publish them in the order they were originally created.
	for i := len(batches) - 1; i >= 0; i-- {
		b := batches[i]
		if err = a.publishBatchTx(ctxb, b.tx, b.id); err != nil {
			return err
		}
	}

	return nil
}

// blockFeeder is a dedicated goroutine that listens for updates to the chain
// tip height, and atomically updates our internal best height state for each
// event.
//
// TODO(roasbeef): start it in the Start() method?
func (a *Auctioneer) blockFeeder(newBlockChan chan int32,
	blockErrChan chan error) {
	defer a.wg.Done()

	for {
		select {
		case newBlock := <-newBlockChan:
			log.Infof("New block(height=%v)", newBlock)

			atomic.StoreUint32(
				&a.bestHeight, uint32(newBlock),
			)

			select {
			case a.auctionEvents <- &newBlockEvent{
				bestHeight: uint32(newBlock),
			}:

			case <-a.quit:
				return
			}

		case err := <-blockErrChan:
			log.Errorf("Unable to get new blocks: %v", err)

		case <-a.quit:
			return
		}
	}
}

// resumeBatchTicker sets the batchTicker which will fire off an event which
// instructs us to try and clear a market after BatchTickInterval has passed.
func (a *Auctioneer) resumeBatchTicker() {
	log.Debugf("Resuming BatchTicker")

	a.cfg.BatchTicker.Resume()
}

// pauseBatchTicker pauses the main batch ticker by stopping the underling
// ticker and setting the ticker channels to nil.
func (a *Auctioneer) pauseBatchTicker() {
	log.Debugf("Pausing BatchTicker")

	a.cfg.BatchTicker.Pause()
}

// refreshOrderFeeder sends a signal to the order feeder that it should
// continue to send new updates from the order book, and also deliver a backlog
// of all order updates that happened while it was paused.
func (a *Auctioneer) resumeOrderFeeder() {
	log.Debugf("Resuming OrderFeeder")

	select {

	case a.orderFeederSignals <- orderFeederDeliver:
		return

	case <-a.quit:
		return
	}
}

// pauseOrderFeeder sends a signal to the order feeder that it should cache all
// updates (not delivering them) until it gets another signal to deliver the
// backlog and resume normal notifications.
func (a *Auctioneer) pauseOrderFeeder() {
	log.Debugf("Pausing OrderFeeder")

	select {

	case a.orderFeederSignals <- orderFeederPause:
		return

	case <-a.quit:
		return
	}
}

// orderFeeder is a dedicated goroutine that we'll listen to all changes to the
// primary order book, to dispatch an update to the main state machine loop.
func (a *Auctioneer) orderFeeder(orderSubscription *subscribe.Client) {

	defer a.wg.Done()

	// dispatchUpdate is a helper function that will apply a new
	// subscription event directly to the call market.
	dispatchUpdate := func(update interface{}) {
		switch u := update.(type) {
		case *order.NewOrderUpdate:
			switch o := u.Order.(type) {
			case *order.Bid:
				err := a.cfg.CallMarket.ConsiderBids(o)
				if err != nil {
					log.Errorf("unable to add Bid(%v) to "+
						"call market: %v", o.Nonce(), err)
				}

			case *order.Ask:
				err := a.cfg.CallMarket.ConsiderAsks(o)
				if err != nil {
					log.Errorf("unable to add Ask(%v) to "+
						"call market: %v", o.Nonce(), err)
				}
			}

		case *order.CancelledOrderUpdate:
			if u.Ask {
				err := a.cfg.CallMarket.ForgetAsks(u.Nonce)
				if err != nil {
					log.Errorf("unable to remove Ask(%v) "+
						"from call market: %v",
						u.Nonce, err)
				}
				return
			}

			err := a.cfg.CallMarket.ForgetBids(u.Nonce)
			if err != nil {
				log.Errorf("unable to remove Bid(%v) from "+
					"call market: %v", u.Nonce, err)
			}
		}
	}

	var (
		feederState   orderFeederState
		updateBacklog []interface{}
	)
	for {
		select {

		// A new signal from the main gorotuine has arrvied, we'll
		// either stop delivering updates, or send out the back log
		// from when we were paused.
		case newFeederState := <-a.orderFeederSignals:

			feederState = newFeederState

			// If we're now meant to pause update delivery, then
			// we'll sit and wait for the next signal.
			if feederState == orderFeederPause {
				log.Infof("OrderFeeder now paused")
				continue
			}

			log.Infof("Dispatching %v orders from backlog",
				len(updateBacklog))

			// Otherwise, we're going back to the delivery mode, so
			// we'll dispatch all updates in our backlog, and then
			// empty it out.
			for _, update := range updateBacklog {
				dispatchUpdate(update)
			}

			updateBacklog = nil

		// A new update has been sent, depending on our state, we'll
		// either cache it or apply it.
		case update := <-orderSubscription.Updates():

			// If we're meant to deliver this update, then do so,
			// otherwise we'll just cache it and wait to flip back
			// to our delivery state.
			if feederState == orderFeederDeliver {
				dispatchUpdate(update)
				continue
			}

			log.Debugf("Adding order to backlog")

			updateBacklog = append(updateBacklog, update)

		case <-orderSubscription.Quit():
			return

		case <-a.quit:
			return
		}
	}
}

// auctionCoordinator is the primary event loop for the auction. In this
// goroutine, we'll process external events, and trigger our own events as well
// to progress the through the phases of the auction.
func (a *Auctioneer) auctionCoordinator(newBlockChan chan int32,
	blockErrChan chan error, orderSubscription *subscribe.Client) {

	defer a.wg.Done()

	a.wg.Add(2)
	go a.blockFeeder(newBlockChan, blockErrChan)
	go a.orderFeeder(orderSubscription)

	a.resumeBatchTicker()
	for {
		select {

		// The batch ticker has just expired, so we'll launch a
		// goroutine to deliver this new signal as a new auction event.
		case <-a.cfg.BatchTicker.Ticks():

			a.wg.Add(1)
			go func() {
				defer a.wg.Done()

				select {
				case a.auctionEvents <- &batchIntervalEvent{}:

				case <-a.quit:
				}
			}()

		// A new internally/externally initiated auction event has
		// arrived. We'll process this event, advancing our state
		// machine until no new state transitions are possible.
		case auctionEvent := <-a.auctionEvents:
			log.Infof("New auction event: %T (trigger=%v)",
				auctionEvent, auctionEvent.Trigger())

			for {
				select {
				case <-a.quit:
					return
				default:
				}

				// We'll log the prior state now, as this will
				// be used to evaluate our termination
				// condition.
				prevState, err := a.cfg.DB.AuctionState()
				if err != nil {
					log.Errorf("Unable to get auction "+
						"state: %v", err)
					break
				}

				// Given the prior (current state), and this
				// event, we'll attempt to step forward our
				// auctioneer state machine by one.
				nextState, err := a.stateStep(
					prevState, auctionEvent,
				)
				if err != nil {
					log.Errorf("Unable to advance "+
						"auction state: %v", err)
					break
				}

				log.Infof("Transitioning States: %v -> %v",
					prevState, nextState)

				// If this state is the same as our prior
				// state, then we'll break out and wait for a
				// new event.
				if nextState == prevState {
					break
				}

				// Now that we've completed this state
				// transition, we'll update our current state.
				err = a.cfg.DB.UpdateAuctionState(nextState)
				if err != nil {
					log.Errorf("Unable to update "+
						"state: %v", err)
					break
				}
			}

		case <-a.quit:
			return
		}
	}
}

// accountConfNotifier is a goroutine that's launched to wait and listen for
// the master account confirmation event. It will then send a new auction event
// to the main state machine loop so we can progress our state.
func (a *Auctioneer) accountConfNotifier(expectedOutput *wire.TxOut,
	genTXID chainhash.Hash, startingAcct *account.Auctioneer) {

	defer a.wg.Done()

	log.Infof("Waiting for Genesis TX (txid=%v) to confirm", genTXID)

	// Now that we know the genesis transaction, we'll wait for it to
	// confirm on-chain
	//
	// TODO(roasbeef): what height hint? log earliest height of system
	// init? height at time of broadcast?
	ctxb := context.Background()
	confChan, errChan, err := a.cfg.ChainNotifier.RegisterConfirmationsNtfn(
		ctxb, &genTXID, expectedOutput.PkScript, 1, 1,
	)
	if err != nil {
		log.Errorf("Unable obtain conf event: %v", err)
		return
	}

	select {

	// The master account has been confirmed, we'll now transition to the
	// MasterAcctConfirmed state so we can commit this to disk, and open up
	// the auction venue!
	case confEvent := <-confChan:
		outputIndex, _ := clmscript.LocateOutputScript(
			confEvent.Tx, expectedOutput.PkScript,
		)
		acctOutPoint := wire.OutPoint{
			Hash:  genTXID,
			Index: outputIndex,
		}

		log.Infof("Genesis txn confirmed in block=%v, full_tx=%v",
			confEvent.BlockHeight, spew.Sdump(confEvent.Tx))

		startingAcct.OutPoint = acctOutPoint
		startingAcct.IsPending = false

		select {
		case a.auctionEvents <- &masterAcctReady{
			acct: startingAcct,
		}:

		case <-a.quit:
			return
		}

	case <-a.quit:
		return

	case err := <-errChan:
		log.Errorf("Unable to dispatch gen tx conf: %v", err)
		return
	}
}

// getPendingBatchID synchronously returns the current pending batch ID.
func (a *Auctioneer) getPendingBatchID() matching.BatchID {
	a.pendingBatchIDMtx.Lock()
	defer a.pendingBatchIDMtx.Unlock()
	return a.pendingBatchID
}

// updatePendingBatchID synchronously updates the current pending batch ID.
func (a *Auctioneer) updatePendingBatchID(newBatchID matching.BatchID) {
	a.pendingBatchIDMtx.Lock()
	defer a.pendingBatchIDMtx.Unlock()
	a.pendingBatchID = newBatchID
}

// banTrader bans the account associated with a trader starting from the current
// height of the chain. The duration of the ban will depend on how many times
// the node has been banned before and grows exponentially, otherwise it is 144
// blocks.
func (a *Auctioneer) banTrader(trader matching.AccountID) {
	accountKey, err := btcec.ParsePubKey(trader[:], btcec.S256())
	if err != nil {
		log.Errorf("Unable to ban account %x: %v", trader[:], err)
		return
	}
	err = a.cfg.DB.BanAccount(
		context.Background(), accountKey, a.BestHeight(),
	)
	if err != nil {
		log.Errorf("Unable to ban account %x: %v", trader[:], err)
	}
}

// removeIneligibleOrders attempts to remove a set of orders that are no longer
// eligible for this batch from the
func (a *Auctioneer) removeIneligibleOrders(orders []orderT.Nonce) {
	for _, order := range orders {
		_ = a.cfg.CallMarket.ForgetBids(order)
		_ = a.cfg.CallMarket.ForgetAsks(order)

		a.removedOrders[order] = struct{}{}

		log.Debugf("Removing Order(%x) from Batch(%v)", order[:],
			a.getPendingBatchID())
	}
}

// restoreIneligibleOrders will re-add any orders we removed during our
// execution loop to the main call market.
func (a *Auctioneer) restoreIneligibleOrders() error {
	log.Infof("Restoring %v removed orders during Batch(%v) execution",
		len(a.removedOrders), a.getPendingBatchID())

	for removedOrderNonce := range a.removedOrders {
		diskOrder, err := a.cfg.DB.GetOrder(
			context.Background(), removedOrderNonce,
		)
		if err != nil {
			return fmt.Errorf("unable to read order for "+
				"nonce=%x: %v", removedOrderNonce[:], err)
		}

		switch o := diskOrder.(type) {
		case *order.Bid:
			if err := a.cfg.CallMarket.ConsiderBids(o); err != nil {
				return err
			}

		case *order.Ask:
			if err := a.cfg.CallMarket.ConsiderAsks(o); err != nil {
				return err
			}
		}
	}

	a.removedOrders = make(map[orderT.Nonce]struct{})

	return nil
}

// stateStep attempts to step forward the auction given a single base trigger.
// We return the _next_ state we're meant to transition to.
func (a *Auctioneer) stateStep(currentState AuctionState, // nolint:gocyclo
	event EventTrigger) (AuctionState, error) {

	ctxb := context.Background()

	switch currentState {

	// In the default state, we'll either go to create our new account, or
	// jump straight to order submission if nothing needs our attention.
	case DefaultState:

		// This is the normal system init. We'll now run a few sanity
		// checks to ensure that we can run the system as normal. If
		// any of these fail, then we won't be able to proceed.
		//
		// First, we'll check if we have a master account in the
		// database or not.
		acct, err := a.cfg.DB.FetchAuctioneerAccount(
			context.Background(),
		)
		switch {
		// If there's no account, then we'll transition to the
		// NoMasterAcctState, meaning we need to obtain one somehow. In
		// this state (the first time the system starts up), we'll rely
		// on the block notification moving us to the next state.
		case err == account.ErrNoAuctioneerAccount:
			log.Infof("No Master Account found, starting genesis " +
				"transaction creation")

			return NoMasterAcctState, nil

		case err != nil:
			return 0, err
		}

		// The account is still pending its confirmation, so we'll go
		// straight to MasterAcctPending.
		if acct.IsPending {
			log.Info("Waiting for confirmation of Master Account")
			return MasterAcctPending, nil
		}

		// Otherwise, we don't need to do anything special, and can
		// start accepting orders immediately.
		log.Infof("Master Account present, moving to accept orders")
		return OrderSubmitState, nil

	// In this state, we don't yet have a master account, so we'll need to
	// first ask the backing wallet to create one for us.
	case NoMasterAcctState:
		// As we don't yet have an account, we'll create the starting
		// account state according to the value in our configuration.
		startingAcct, err := a.baseAuctioneerAcct(
			ctxb, a.cfg.StartingAcctValue,
		)
		if err != nil {
			return 0, err
		}
		acctOutput, err := startingAcct.Output()
		if err != nil {
			return 0, err
		}

		// At this point, we know we don't have a master account yet,
		// so we'll need to create one. However, we can't if we don't
		// have any coins in the wallet, so we'll postpone things if we
		// have no sats.
		//
		// TODO(roasbeef): add wallet balance to WalletKit
		walletBalance, err := a.cfg.Wallet.ConfirmedWalletBalance(
			ctxb,
		)
		if err != nil {
			return 0, err
		}
		if walletBalance <= a.cfg.StartingAcctValue {
			log.Infof("Need %v coins for Master Account, only "+
				"have %v, waiting for new block...",
				a.cfg.StartingAcctValue, walletBalance)

			return NoMasterAcctState, nil
		}

		// Store a pending version of the account before we broadcast
		// the genesis transaction. We'll still recover properly within
		// MasterAcctPending if we shut down before broadcast.
		err = a.cfg.DB.UpdateAuctioneerAccount(ctxb, startingAcct)
		if err != nil {
			return 0, fmt.Errorf("unable to update auctioneer account: %v", err)
		}

		// Now that we know we have enough coins, we'll instruct the
		// wallet to create an output on-chain that pays to our master
		// account.
		//
		// TODO(roasbeef): need to handle case of someone else sending
		// to our account early or w/e too many funds? notifier as is
		// doesn't handle duplicate addrs?
		//
		// TODO(roasbeef): rely on manual anchor down if not
		// confirming? need admin RPC get current txid and anchor down
		// if needed?
		feeRate, err := a.cfg.Wallet.EstimateFee(
			ctxb, a.cfg.ConfTarget,
		)
		if err != nil {
			return 0, err
		}

		log.Debugf("Sending genesis transaction to output %v using "+
			"fee rate %v", acctOutput, feeRate)

		tx, err := a.cfg.Wallet.SendOutputs(
			ctxb, []*wire.TxOut{acctOutput}, feeRate,
		)
		if err != nil {
			return 0, fmt.Errorf("unable to send funds to master "+
				"acct output: %w", err)
		}

		log.Infof("Sent genesis transaction txid=%v", tx.TxHash())

		return MasterAcctPending, nil

	// In this state, the master account is still pending so we'll wait
	// until it has been fully confirmed.
	case MasterAcctPending:
		// If we get a confirmation event trigger, then we know that
		// the master account has been confirmed, so we'll go to that
		// state.
		if event.Trigger() == ConfirmationEvent {
			log.Infof("Genesis transaction confirmed, processing " +
				"block")
			return MasterAcctConfirmed, nil
		}

		// At this point, we know that we've broadcast the master
		// account, so we'll find the transaction in the wallet's
		// store, so we can watch for its confirmation on-chain.
		startingAcct, err := a.cfg.DB.FetchAuctioneerAccount(ctxb)
		if err != nil {
			return 0, err
		}
		output, err := startingAcct.Output()
		if err != nil {
			return 0, err
		}

		// It's possible that when we were last online, we actually
		// already sent the transaction to init the system, so we'll
		// check now, and if so, skip straight to the MasterAcctPending
		// state.
		genesisTx, err := a.locateTxByOutput(ctxb, output)
		switch {
		// If we can't find the transaction, then we'll proceed with
		// creating one now. This can happen if we shut down after
		// storing our pending state and before broadcasting our
		// transaction.
		case err == errTxNotFound:
			feeRate, err := a.cfg.Wallet.EstimateFee(
				ctxb, a.cfg.ConfTarget,
			)
			if err != nil {
				return 0, err
			}

			log.Infof("Genesis transaction not found, resending "+
				"to output %v using fee rate %v", output,
				feeRate)

			genesisTx, err = a.cfg.Wallet.SendOutputs(
				ctxb, []*wire.TxOut{output}, feeRate,
			)
			if err != nil {
				return 0, fmt.Errorf("unable to send funds to master "+
					"acct output: %w", err)
			}

		case err != nil:
			return 0, err
		}

		// As we want to be able to process any new events, we launch a
		// goroutine to send us a confirmation event once the
		// transaction has confirmed on-disk.
		a.watchAccountConfOnce.Do(func() {
			a.wg.Add(1)
			go a.accountConfNotifier(
				output, genesisTx.TxHash(), startingAcct,
			)
		})

		return MasterAcctPending, nil

	// At this point, we know the master account has confirmed, so we'll
	// commit it to disk, then open up the system for order matching!
	case MasterAcctConfirmed:
		acctReadyEvent, _ := event.(*masterAcctReady)

		log.Infof("Master Account ready: out_point=%v, value=%v",
			acctReadyEvent.acct.OutPoint,
			acctReadyEvent.acct.Balance)

		err := a.cfg.DB.UpdateAuctioneerAccount(
			ctxb, acctReadyEvent.acct,
		)
		if err != nil {
			return 0, err
		}

		return OrderSubmitState, nil

	// From the order submit state, we'll wait and accept new orders from
	// traders until we get a batch tick. From here, we'll either attempt
	// to make+clear+execute the market, or just wait for another interval
	// if we were unable to make a market.
	case OrderSubmitState:
		// If this is a batch tick event, then we'll attempt to perform
		// matching making as long as the prior state wasn't just the
		// MatchMakingState.
		if event.Trigger() == BatchTickEvent && !a.batchRetry {
			// Before starting a new batch, we'll restore any orders
			// we removed due to errors in a previous run, as they
			// may be eligible again now.
			if err := a.restoreIneligibleOrders(); err != nil {
				return 0, err
			}

			// As we're now attempting to perform match making,
			// we'll pause the order book subscription so we only
			// look at the set of orders created before now.
			a.pauseOrderFeeder()
			a.pauseBatchTicker()
			return MatchMakingState, nil
		}

		// We can clear our retry flag now as we're terminating at this
		// state.
		a.batchRetry = false

		// Otherwise, we'll just stay in this state and continue
		// accepting orders.
		return OrderSubmitState, nil

	// In the match making state, we'll attempt to make a market if
	// possible. If we can't then we'll go back to accepting orders.
	case MatchMakingState:
		// Before we attempt to clear the market, we need to obtain the
		// current batch key.
		batchKey, err := a.cfg.DB.BatchKey(context.Background())
		if err != nil {
			return 0, err
		}

		// To avoid an infinite loop, we'll set the retry flag to
		// ensure we don't go back to this state, thereby avoiding an
		// infinite loop.
		a.batchRetry = true

		var batchID matching.BatchID
		copy(batchID[:], batchKey.SerializeCompressed())

		// Get a fee estimate for our batch. We'll use this to only
		// include orders with a max fee rate below this value during
		// matchmaking.
		feeRate, err := a.cfg.Wallet.EstimateFee(
			ctxb, a.cfg.ConfTarget,
		)
		if err != nil {
			return 0, err
		}

		log.Debugf("Using fee rate %v for batch transaction", feeRate)

		// Now that we have the batch key, we'll attempt to make this
		// market.
		orderBatch, err := a.cfg.CallMarket.MaybeClear(batchID, feeRate)
		switch {
		// If we can't make a market at this instance, then we'll
		// go back to the OrderSubmitState to wait for more orders.
		case err == matching.ErrNoMarketPossible:
			a.resumeBatchTicker()
			a.resumeOrderFeeder()

			log.Infof("No market possible at this time")

			return OrderSubmitState, nil

		case err != nil:
			return 0, err
		}

		// At this point we have a batch that we can now go to execute,
		// so we'll add it to the current environment of the state
		// machine.
		//
		// TODO(roasbeef): allow to send own triggers instead, or we
		// add this to the running state instead
		a.eligibleBatch = orderBatch
		a.batchFeeRate = feeRate
		a.updatePendingBatchID(batchID)

		log.Infof("Market has been made for Batch(%x)", batchID[:])

		// With the batch stored, we'll now transition to the
		// BatchExecutionState where we'll actually kick off the
		// signing protocol needed to make this batch valid.
		return BatchExecutionState, nil

	// In this phase, we'll attempt to execute the order by entering into a
	// multi-party signing protocol with all the relevant traders.
	case BatchExecutionState:
		log.Infof("Attempting to execute Batch(%v)",
			a.getPendingBatchID())

		// To kick things off, we'll attempt to execute the batch as
		// is.
		executionResult, err := a.cfg.BatchExecutor.Submit(
			a.eligibleBatch, a.cfg.FeeSchedule, a.batchFeeRate,
		)
		if err != nil {
			return 0, err
		}

		select {
		// We've received a response back, this batch is either good to
		// go, or we need to make some changes to attempt to re-submit
		// it.
		case result := <-executionResult:
			// If we have a non-nil error, then this means there
			// was an issue with the batch, so we'll try to see if
			// we can fix the issue to re-submit.
			if result.Err != nil {
				log.Warnf("Encountered error during Batch(%v) "+
					"execution: %v", a.getPendingBatchID(),
					result.Err)

				switch exeErr := result.Err.(type) {
				case *venue.ErrMissingTraders:
					nonces := make(
						[]orderT.Nonce,
						0,
						len(exeErr.OrderNonces),
					)
					for nonce := range exeErr.OrderNonces {
						nonces = append(nonces, nonce)
					}
					a.removeIneligibleOrders(nonces)

				case *venue.ErrInvalidWitness:
					a.removeIneligibleOrders(exeErr.OrderNonces)

				case *venue.ErrMissingChannelInfo:
					a.removeIneligibleOrders(exeErr.OrderNonces)

				case *venue.ErrNonMatchingChannelInfo:
					a.banTrader(exeErr.Trader1)
					a.banTrader(exeErr.Trader2)
					a.removeIneligibleOrders(exeErr.OrderNonces)

				case *venue.ErrMsgTimeout:
					a.removeIneligibleOrders(exeErr.OrderNonces)

				default:
					// If we get down to this state, then
					// we had an unexpected error, meaning
					// we can't continue so we'll exit out.
					//
					// TODO(roasbeef): just go back to
					// default state?
					return 0, fmt.Errorf("terminal "+
						"execution error: %v", result.Err)
				}

				return MatchMakingState, nil
			}

			log.Infof("Batch(%v) successfully executed!!!",
				a.getPendingBatchID())

			a.finalizedBatch = result
			return BatchCommitState, nil

		case <-a.quit:
			return 0, fmt.Errorf("server shutting down")
		}

	// In the batch commit state, we'll broadcast the current finalized
	// batch as is and begin enforcing service lifetime for the channels
	// created as part of the batch.
	case BatchCommitState:
		// First, we'll broadcast the current batch execution
		// transaction as it exists now.
		err := a.publishBatchTx(
			ctxb, a.finalizedBatch.BatchTx,
			orderT.BatchID(a.getPendingBatchID()),
		)
		if err != nil {
			return 0, fmt.Errorf("unable to publish batch "+
				"tx: %v", err)
		}

		// Then, we'll begin enforcing the service lifetime of the
		// relevant channels.
		err = a.cfg.ChannelEnforcer.EnforceChannelLifetimes(
			a.finalizedBatch.LifetimePackages...,
		)
		if err != nil {
			return 0, fmt.Errorf("unable to enforce channel "+
				"lifetimes: %v", err)
		}

		// We'll reload the set of orders from disk so we have a
		// consistent view, and then re-start the orderFeeder goroutine.
		a.resumeOrderFeeder()
		a.resumeBatchTicker()

		return OrderSubmitState, nil
	}

	return 0, fmt.Errorf("unknown state: %v", currentState)
}

// baseAuctioneerAcct returns the base auctioneer account (genesis batch
// auction key) give the target size of the initial account.
func (a *Auctioneer) baseAuctioneerAcct(ctx context.Context,
	startingBalance btcutil.Amount) (*account.Auctioneer, error) {

	// First, we'll obtain the two keys we need to generate the account
	// (and its output): the batch key and our long-term auctioneer key.
	batchKey, err := a.cfg.DB.BatchKey(ctx)
	if err != nil {
		return nil, err
	}
	auctioneerKey, err := a.cfg.Wallet.DeriveNextKey(
		ctx, int32(account.AuctioneerKeyFamily),
	)
	if err != nil {
		return nil, err
	}

	startingAcct := account.Auctioneer{
		AuctioneerKey: auctioneerKey,
		Balance:       startingBalance,
		IsPending:     true,
	}
	copy(
		startingAcct.BatchKey[:], batchKey.SerializeCompressed(),
	)

	return &startingAcct, nil
}

// locateTxByOutput locates a transaction from the backing wallet by looking
// for a specific output. If a transaction is not found containing the output,
// then errTxNotFound is returned.
func (a *Auctioneer) locateTxByOutput(ctx context.Context,
	output *wire.TxOut) (*wire.MsgTx, error) {

	txs, err := a.cfg.Wallet.ListTransactions(ctx)
	if err != nil {
		return nil, err
	}

	for _, tx := range txs {
		idx, ok := clmscript.LocateOutputScript(tx, output.PkScript)
		if !ok {
			continue
		}
		if tx.TxOut[idx].Value == output.Value {
			return tx, nil
		}
	}

	return nil, errTxNotFound
}
