package chanenforcement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/chanbackup"
	"github.com/lightningnetwork/lnd/input"
)

var (
	// ErrChannelEnforcerShuttingDown is an error returned when we attempt
	// to perform an operation on the channel enforcer, but it has already
	// been terminated.
	ErrChannelEnforcerShuttingDown = errors.New("channel enforcer " +
		"shutting down")
)

// enforceLifetimeMsg is an internal message we'll send to the ChannelEnforcer
// to begin enforcing the service lifetime for a series of channels. A
// synchronous reply is sent through errChan.
type enforceLifetimeMsg struct {
	pkgs    []*LifetimePackage
	errChan chan error
}

// Config contains all of the required dependencies of the ChannelEnforcer for
// it to carry out its duties.
type Config struct {
	// ChainNotifier is responsible for keeping track of the best height in
	// the chain and detecting channel spends.
	ChainNotifier lndclient.ChainNotifierClient

	// PackageSource is responsible for retrieving any existing channel
	// lifetime enforcement packages and pruning them accordingly once
	// they're no longer actionable.
	//
	// NOTE: This interface does not need to be concerned with the storage
	// of channel lifetime enforcement packages, as that's the
	// responsibility of another subsystem (the auction venue upon a
	// successful batch).
	PackageSource PackageSource
}

// ChannelEnforcer is responsible for enforcing channel service lifetime
// packages that result from an auction's matched order pair. These channels
// each specify a maturity height. Violations to the channel's service lifetime
// occur upon a confirmed commitment broadcast before said height, assuming that
// the bidder (buyer of the channel) rejects any cooperative close attempts from
// the asker (seller of the channel). To determine the offender of said
// violations, the ChannelEnforcer derives the expected to_remote outputs
// scripts of each trader's commitment transaction and compares it with the
// to_remote output script found in the confirmed commitment.
type ChannelEnforcer struct {
	// bestHeight tracks the current height in the chain.
	//
	// NOTE: This must be used atomically.
	bestHeight uint32

	started sync.Once
	stopped sync.Once

	cfg Config

	// enforceLifetimes is a channel we'll use within the ChannelEnforcer's
	// requestHandler to handle incoming channel lifetime enforcement
	// requests.
	enforceLifetimes chan *enforceLifetimeMsg

	blockNtfnCancel func()

	quit chan struct{}
	wg   sync.WaitGroup
}

// New returns a ChannelEnforcer instance backed by the given configuration.
func New(cfg *Config) *ChannelEnforcer {
	return &ChannelEnforcer{
		cfg:              *cfg,
		enforceLifetimes: make(chan *enforceLifetimeMsg),
		quit:             make(chan struct{}),
	}
}

// Start begins the enforcement of any existing channel service lifetime
// packages and handling of client requests.
//
// NOTE: Due to a limitation of the ChainNotifier API, to ensure proper
// enforcement of channel lifetime packages, the transaction backing each
// channel should _at least_ be found within the mempool.
func (e *ChannelEnforcer) Start() error {
	var err error
	e.started.Do(func() {
		err = e.start()
	})
	return err
}

// start begins the enforcement of any existing channel service lifetime
// packages and handling of client requests.
func (e *ChannelEnforcer) start() error {
	// Register a block subscription to receive up-to-date notifications
	// about the chain.
	ctx := context.Background()
	blockCtx, blockCancel := context.WithCancel(ctx)
	e.blockNtfnCancel = blockCancel
	blockChan, blockErrChan, err := e.cfg.ChainNotifier.RegisterBlockEpochNtfn(
		blockCtx,
	)
	if err != nil {
		return err
	}

	// Wait for the block notification upon registration to be delivered.
	select {
	case bestHeight := <-blockChan:
		e.setBestHeight(uint32(bestHeight))
	case err := <-blockErrChan:
		return fmt.Errorf("unable to receive initial block "+
			"notification: %v", err)
	case <-e.quit:
		return ErrChannelEnforcerShuttingDown
	}

	// Load all existing channel lifetime enforcement packages to begin
	// enforcing them.
	lifetimePackages, err := e.cfg.PackageSource.LifetimePackages(ctx)
	if err != nil {
		return err
	}

	// registerLifetimeEnforcement will register a spend notification, which
	// at times can take a while if a historical rescan is performed. To
	// speed things up, we'll launch our channel spend subscriptions in
	// parallel.
	var wg sync.WaitGroup
	wg.Add(len(lifetimePackages))
	errChan := make(chan error, len(lifetimePackages))
	for _, pkg := range lifetimePackages {
		// TODO(wilmer): Perform a UTXO lookup on the channel to
		// determine if it still exists, and if it does, then prune it
		// if we're already past the channel's maturity height?
		go func(pkg *LifetimePackage) {
			defer wg.Done()

			select {
			case errChan <- e.registerLifetimePackage(pkg):
			case <-e.quit:
				errChan <- ErrChannelEnforcerShuttingDown
			}
		}(pkg)
	}

	go func() {
		wg.Wait()
		close(errChan)
	}()

	for err := range errChan {
		if err != nil {
			return err
		}
	}

	// Begin accepting client requests.
	e.wg.Add(1)
	go e.requestHandler(blockChan, blockErrChan)

	return nil
}

// Stop safely stops watching for any channel enforcement violations.
func (e *ChannelEnforcer) Stop() {
	e.stopped.Do(func() {
		close(e.quit)
		e.wg.Wait()

		if e.blockNtfnCancel != nil {
			e.blockNtfnCancel()
		}
	})
}

// EnforceChannelLifetimes enforces the service lifetime for a series of
// channels. If a premature spend happens for any of these, then the responsible
// trader is punished according to the heuristic used by
// PackageSource.EnforceLifetimeViolation.
func (e *ChannelEnforcer) EnforceChannelLifetimes(
	pkgs ...*LifetimePackage) error {

	errChan := make(chan error, 1)
	select {
	case e.enforceLifetimes <- &enforceLifetimeMsg{
		pkgs:    pkgs,
		errChan: errChan,
	}:
	case <-e.quit:
		return ErrChannelEnforcerShuttingDown
	}

	select {
	case err := <-errChan:
		return err
	case <-e.quit:
		return ErrChannelEnforcerShuttingDown
	}
}

// requestHandler handles all incoming requests to the ChannelEnforcer, while
// also keeping track of the best height in the chain.
func (e *ChannelEnforcer) requestHandler(blockChan chan int32,
	blockErrChan chan error) {

	defer e.wg.Done()

	for {
		select {
		case newBlock := <-blockChan:
			e.setBestHeight(uint32(newBlock))

			// TODO(wilmer): Prune all unspent mature packages as of
			// this height?

		case err := <-blockErrChan:
			log.Errorf("Unable to receive block notification: %v",
				err)

		case msg := <-e.enforceLifetimes:
			for _, pkg := range msg.pkgs {
				err := e.registerLifetimePackage(pkg)
				if err != nil {
					log.Errorf("Unable to register "+
						"lifetime package for channel "+
						"%v: %v", pkg.ChannelPoint, err)
					continue
				}
			}
			msg.errChan <- nil

		case <-e.quit:
			return
		}
	}
}

// setBestHeight atomically sets the best known height.
func (e *ChannelEnforcer) setBestHeight(height uint32) {
	atomic.StoreUint32(&e.bestHeight, height)
}

// getBestHeight atomically retrieves the best known height.
func (e *ChannelEnforcer) getBestHeight() uint32 {
	return atomic.LoadUint32(&e.bestHeight)
}

// registerLifetimePackage begins enforcing a channel's lifetime by registering
// a spend notification for the channel and acting accordingly upon a dispatched
// notification.
func (e *ChannelEnforcer) registerLifetimePackage(pkg *LifetimePackage) error {
	ctx, cancel := context.WithCancel(context.Background())
	spendChan, errChan, err := e.cfg.ChainNotifier.RegisterSpendNtfn(
		ctx, &pkg.ChannelPoint, pkg.ChannelScript,
		int32(pkg.HeightHint),
	)
	if err != nil {
		cancel()
		return err
	}

	e.wg.Add(1)
	go e.enforceOnPrematureSpend(pkg, spendChan, errChan, cancel)

	return nil
}

// enforceOnPrematureSpend waits for a notification of a channel being spent and
// acts accordingly. If the spending height exceeds the maturity height, then
// the channel's lifetime enforcement package is pruned. Otherwise, the offender
// is identified and punished. We assume a spend before the maturity height will
// always be due to a commitment broadcast, since the bidder _should_ reject any
// cooperative closes.
func (e *ChannelEnforcer) enforceOnPrematureSpend(pkg *LifetimePackage,
	spendChan chan *chainntnfs.SpendDetail, errChan chan error,
	cancelSpend func()) {

	defer e.wg.Done()
	defer cancelSpend()

	// TODO(wilmer): Prune if the best chain height has exceeded the
	// maturity and we know a spend hasn't happened before then? This would
	// likely require an additional event being sent from the subscription.
	//
	// TODO(wilmer): With anchors, nodes can broadcast their commitment and
	// not bump its fees until the maturity height is reached. How should we
	// handle this?
	select {
	case spend := <-spendChan:
		if uint32(spend.SpendingHeight) >= pkg.MaturityHeight {
			if err := e.pruneMatureLifetimePackage(pkg); err != nil {
				log.Errorf("Unable to prune mature lifetime "+
					"enforcement package for channel %v: "+
					"%v", pkg.ChannelPoint, err)
			}

			return
		}

		log.Infof("Found lifetime violation for channel %v",
			pkg.ChannelPoint)

		if err := e.enforceLifetimeViolation(pkg, spend); err != nil {
			log.Errorf("Unable to enforce lifetime violation for "+
				"channel %v: %v", pkg.ChannelPoint, err)
		}

		return

	case err := <-errChan:
		log.Errorf("Unable to receive spend notification for channel "+
			"%v: %v", pkg.ChannelPoint, err)
		return

	case <-e.quit:
		return
	}
}

// pruneMatureLifetimePackage removes all references to a channel lifetime
// enforcement package which has matured. Matured in this context means once the
// chain height has surpassed the channel's maturity height without seeing a
// spend of the channel.
func (e *ChannelEnforcer) pruneMatureLifetimePackage(pkg *LifetimePackage) error {
	return e.cfg.PackageSource.PruneLifetimePackage(context.Background(), pkg)
}

// enforceLifetimeViolation punishes the channel initiator for violating a
// channel lifetime agreement with their counterparty. This can only occur if a
// commitment transaction is broadcast before the channel's maturity height.
func (e *ChannelEnforcer) enforceLifetimeViolation(pkg *LifetimePackage,
	spendDetails *chainntnfs.SpendDetail) error {

	channelInitiator, err := isChannelInitiatorOffender(pkg, spendDetails)
	if err != nil {
		return fmt.Errorf("unable to determine punishable trader: %v",
			err)
	}

	ctx := context.Background()
	if !channelInitiator {
		err := e.cfg.PackageSource.PruneLifetimePackage(ctx, pkg)
		if err != nil {
			return fmt.Errorf("unable to prune lifetime package "+
				"of channel %v without violation: %v",
				pkg.ChannelPoint, err)
		}

		return nil
	}

	err = e.cfg.PackageSource.EnforceLifetimeViolation(
		ctx, pkg, e.getBestHeight(),
	)
	if err != nil {
		return fmt.Errorf("unable to punish lifetime violation "+
			"offender of channel %v: %v", pkg.ChannelPoint, err)
	}

	return nil
}

// isChannelInitiatorOffender determines if the channel initiator is the
// offender of a channel lifetime violation in the event of a force close. This
// is done by deriving the expected to_remote output scripts for each channel
// party and checking for their existence in the broadcast commitment
// transaction. If the channel was cooperatively closed, we'll assume the
// non channel-initiator initiated the request and should not punish them, as
// they should reject any requests initiated by the channel initiator.
func isChannelInitiatorOffender(pkg *LifetimePackage,
	spendDetails *chainntnfs.SpendDetail) (bool, error) {

	// In the event of a cooperative close, we'll assume the non
	// channel-initiator initiated the request and should not punish them,
	// as they should reject any requests initiated by the channel
	// initiator. A cooperative close is identified by the only input having
	// a maximum sequence value.
	if spendDetails.SpendingTx.TxIn[0].Sequence == wire.MaxTxInSequenceNum {
		log.Debugf("Channel %v was closed cooperatively at height %v",
			pkg.ChannelPoint, spendDetails.SpendingHeight)
		return false, nil
	}

	// Derive the expected to_remote output script for both the channel
	// initiator and non-initiator according to the channel version.
	var (
		askNonDelayedWitnessScript []byte
		bidNonDelayedWitnessScript []byte
		err                        error
	)
	switch pkg.Version {
	// If this is a tweakless channel, the to_remote output scripts should
	// just be a P2WPKH of the remote payment basepoint.
	case chanbackup.TweaklessCommitVersion:
		askNonDelayedWitnessScript, err = input.CommitScriptUnencumbered(
			pkg.AskPaymentBasePoint,
		)
		if err != nil {
			return false, err
		}
		bidNonDelayedWitnessScript, err = input.CommitScriptUnencumbered(
			pkg.BidPaymentBasePoint,
		)
		if err != nil {
			return false, err
		}

	// If this is a channel that supports anchor outputs, the to_remote
	// output scripts should be a P2WSH that wraps a P2WPKH of the remote
	// payment basepoint with a CSV delay of 1.
	case chanbackup.AnchorsCommitVersion:
		askNonDelayedWitnessScript, err = input.CommitScriptToRemoteConfirmed(
			pkg.AskPaymentBasePoint,
		)
		if err != nil {
			return false, err
		}
		bidNonDelayedWitnessScript, err = input.CommitScriptToRemoteConfirmed(
			pkg.BidPaymentBasePoint,
		)
		if err != nil {
			return false, err
		}

	default:
		return false, fmt.Errorf("unknown backup version %v", pkg.Version)
	}

	askNonDelayed, err := input.WitnessScriptHash(askNonDelayedWitnessScript)
	if err != nil {
		return false, err
	}
	bidNonDelayed, err := input.WitnessScriptHash(bidNonDelayedWitnessScript)
	if err != nil {
		return false, err
	}

	// We'll then attempt to locate either script in the commitment
	// transaction broadcast.
	for _, txOut := range spendDetails.SpendingTx.TxOut {
		// If we're able to find the non-initiator's non-delayed script
		// in the commitment transaction, then the initiator force
		// closed.
		if bytes.Equal(bidNonDelayed, txOut.PkScript) {
			log.Infof("Found node %x with account %x responsible "+
				"for lifetime violation of channel %v",
				pkg.AskNodeKey.SerializeCompressed(),
				pkg.AskAccountKey.SerializeCompressed(),
				pkg.ChannelPoint)

			return true, nil
		}

		// Similarly, if we're able to find the initiator's non-delayed
		// script in the commitment transaction, then the non-initiator
		// force closed.
		if bytes.Equal(askNonDelayed, txOut.PkScript) {
			log.Infof("Found node %x with account %x responsible "+
				"for lifetime violation of channel %v",
				pkg.BidNodeKey.SerializeCompressed(),
				pkg.BidAccountKey.SerializeCompressed(),
				pkg.ChannelPoint)

			return false, nil
		}
	}

	// At this point, we couldn't find a to_remote output for either party.
	// The initiator of the channel should always be the seller, so they'll
	// always have an output from the non-initiator's perspective due to the
	// channel reserve. Therefore, we can safely assume that the initiator
	// force closed.
	log.Debugf("Commitment transaction broadcast for channel %v does not "+
		"include to_remote output", pkg.ChannelPoint)

	log.Infof("Punishing node %x with account %x due to a lifetime violation "+
		"for channel %v", pkg.AskNodeKey.SerializeCompressed(),
		pkg.AskAccountKey.SerializeCompressed(), pkg.ChannelPoint)

	return true, nil
}
