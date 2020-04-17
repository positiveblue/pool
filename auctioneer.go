package agora

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
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/agora/client/clmscript"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
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

	// DeriveKey derives the key specified by the target KeyLocator.
	DeriveKey(context.Context, *keychain.KeyLocator) (
		*keychain.KeyDescriptor, error)
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
}

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

	quit chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once

	wg sync.WaitGroup

	watchAccountConfOnce sync.Once

	blockNtfnCancel func()
}

// NewAuctioneer returns a new instance of the auctioneer given a fully
// populated config struct.
func NewAuctioneer(cfg AuctioneerConfig) *Auctioneer {
	return &Auctioneer{
		cfg:           cfg,
		auctionEvents: make(chan EventTrigger),
		quit:          make(chan struct{}),
	}
}

// Start launcehs all goroutines the auctioneer needs to perform its duties.
func (a *Auctioneer) Start() error {
	var startErr error

	a.startOnce.Do(func() {
		log.Infof("Starting main Auctioneer State Machine")

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
		go a.auctionCoordinator(newBlockChan, blockErrChan)
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

// auctionCoordinator is the primary event loop for the auction. In this
// goroutine, we'll process external events, and trigger our own events as well
// to progress the through the phases of the auction.
func (a *Auctioneer) auctionCoordinator(newBlockChan chan int32,
	blockErrChan chan error) {

	defer a.wg.Done()

	// TODO(roasbeef): batch tick timer
	//  * OrderSubmitState -> (tick trigger) -> matching phase
	//  * OrderMatchingPhase -> BatchClearingPhase
	//  * match all orders, generate diff for clearing
	//  * BatchClearingPhase -> BatchExecutionPhase
	//  * if something goes wrong, go back to OrderMatchingPhase
	//      * trigger tells you what to remove and who went weong?
	//  * otherwise, batch commit the updates, broadcast the batch tx, and
	//    go back to the order submit phase

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()

		for {
			select {
			case newBlock := <-newBlockChan:
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
	}()

	for {
		select {

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
					continue
				}

				// Given the prior (current state), and this
				// event, we'll attempt to step forward our
				// auctioneer state machine by one.
				//
				// TODO(rosabeef): allow to emit own event?
				nextState, err := a.stateStep(
					prevState, auctionEvent,
				)
				if err != nil {
					log.Errorf("Unable to advance "+
						"auction state: %v", err)
					continue
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
					continue
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

// stateStep attempts to step forward the auction given a single base trigger.
// We return the _next_ state we're meant to transition to.
//
// TODO(roasbeef): also pass in some intermediate state as well?
//
// TODO(roasbeef): split out account init into a distinct, smaller state
// machine?
func (a *Auctioneer) stateStep(currentState AuctionState,
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
		_, err := a.cfg.DB.FetchAuctioneerAccount(
			context.Background(),
		)
		switch {
		// If there's no account, then we'll transition to the
		// NoMasterAcctState, meaning we need to obtain one somehow. In
		// this state (the first time the system starts up), we'll rely
		// on the block notification moving us to the next state.
		case err == agoradb.ErrNoAuctioneerAccount:
			log.Infof("No Master Account found, starting genesis " +
				"transaction creation")

			return NoMasterAcctState, nil

		case err != nil:
			return 0, err

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

		// It's possible that when we were last online, we actually
		// already sent the transaction to init the system, so we'll
		// check now, and if so, skip straight to the MasterAcctPending
		// state.
		genTx, err := a.locateTxByOutput(ctxb, acctOutput)
		switch {
		// If we can't find the transaction, then we'll proceed with
		// broadcasting the genesis transaction as normal.
		case err == errTxNotFound:
			break

		case err != nil:
			return 0, err

		default:
			log.Infof("Genesis txn (txid=%v) for Master Account "+
				"already broadcast, waiting for "+
				"confirmation...", genTx.TxHash())

			return MasterAcctPending, nil
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

		// Now that we know we have enough coins, we'll instruct the
		// wallet to create an output on-chain that pays to our master
		// account.
		//
		// TODO(roasbeef): need to handle case of someone else sending
		// to our account early or w/e too many funds? notifier as is
		// doesn't handle duplicate addrs?
		//
		// TODO(roasbeef): what fee to use? rely on manual anchor down
		// if not confirming? need admin RPC get current txid and
		// anchor down if needed?
		tx, err := a.cfg.Wallet.SendOutputs(
			ctxb, []*wire.TxOut{acctOutput}, chainfee.FeePerKwFloor,
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
		startingAcct, err := a.baseAuctioneerAcct(
			ctxb, a.cfg.StartingAcctValue,
		)
		if err != nil {
			return 0, err
		}
		output, err := startingAcct.Output()
		if err != nil {
			return 0, err
		}
		genesisTx, err := a.locateTxByOutput(ctxb, output)
		if err != nil {
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

	case OrderSubmitState:
		return OrderSubmitState, nil

	case BatchClearingState:
		return BatchClearingState, nil
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
	auctioneerKey, err := a.cfg.Wallet.DeriveKey(
		ctx, &keychain.KeyLocator{
			Family: account.AuctioneerKeyFamily,
		},
	)
	if err != nil {
		return nil, err
	}

	startingAcct := account.Auctioneer{
		AuctioneerKey: auctioneerKey,
		Balance:       startingBalance,
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
