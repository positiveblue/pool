package order

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/multimutex"
	"github.com/lightningnetwork/lnd/subscribe"
)

// NewOrderUpdate is an update sent each time a new order has been added.
type NewOrderUpdate struct {
	// Order is the order that was added.
	Order ServerOrder
}

// CancelledOrderUpdate is an order sent each time an order has been cancelled.
type CancelledOrderUpdate struct {
	// Ask indicates if this order was an ask or not.
	Ask bool

	// Nonce is the nonce of the order that was cancelled.
	Nonce order.Nonce
}

// BookStore is the store interface that the order book needs. We need to be
// able to retrieve accounts as well therefore the order store is not enough.
type BookStore interface {
	Store
	account.Store
}

// BookConfig contains all of the required dependencies for the Book to
// carry out its duties.
type BookConfig struct {
	// Store is responsible for storing and retrieving order information.
	Store BookStore

	// Signer is used to verify order signatures.
	Signer lndclient.SignerClient

	// DurationBuckets should point to the set of active duration buckets
	// for this market.
	DurationBuckets *DurationBuckets
}

// Book is the representation of the auctioneer's order book and is responsible
// for accepting, matching and executing orders.
type Book struct {
	started sync.Once
	stopped sync.Once

	cfg BookConfig

	ntfnServer *subscribe.Server
	acctMutex  *multimutex.HashMutex

	wg   sync.WaitGroup
	quit chan struct{}
}

// NewBook instantiates a new Book backed by the given config.
func NewBook(cfg *BookConfig) *Book {
	return &Book{
		ntfnServer: subscribe.NewServer(),
		cfg:        *cfg,
		acctMutex:  multimutex.NewHashMutex(),
		quit:       make(chan struct{}),
	}
}

// Start starts all concurrent tasks the manager is responsible for.
func (b *Book) Start() error {
	var startErr error
	b.started.Do(func() {
		if err := b.ntfnServer.Start(); err != nil {
			startErr = err
			return
		}
	})
	return startErr
}

// Stop stops all concurrent tasks the manager is responsible for.
func (b *Book) Stop() {
	b.stopped.Do(func() {
		_ = b.ntfnServer.Stop()

		close(b.quit)
		b.wg.Wait()
	})
}

// DurationBuckets returns the set of active duration buckets for this market.
func (b *Book) DurationBuckets() *DurationBuckets {
	return b.cfg.DurationBuckets
}

// PrepareOrder validates an incoming order and stores it to the database.
func (b *Book) PrepareOrder(ctx context.Context, o ServerOrder,
	feeSchedule terms.FeeSchedule, bestHeight uint32) error {

	// Get the account that is making this order.
	rawKey := o.Details().AcctKey
	acctKey, err := btcec.ParsePubKey(rawKey[:])
	if err != nil {
		return err
	}

	acct, err := b.cfg.Store.Account(ctx, acctKey, false)
	if err != nil {
		return fmt.Errorf("unable to locate account with key %x: %v",
			acctKey.SerializeCompressed(), err)
	}

	// First we make sure the account is ready to submit orders.
	err = b.validateAccountState(ctx, acctKey, acct, bestHeight)
	if err != nil {
		return err
	}

	// Now that the account is cleared, validate the order.
	err = b.validateOrder(ctx, o)
	if err != nil {
		return err
	}

	// The order was valid in isolation, but it still might be the case the
	// account has active orders that make the balance too low to accept
	// this additional order. We check the total locked value in case this
	// order is added.
	//
	// To ensure no other order is submitted before we have checked the
	// locked value and submitted this order, we get a mutex exclusive for
	// this account. We use the first 32 bytes as an account identifier.
	var acctID lntypes.Hash
	copy(acctID[:], rawKey[:32])
	b.acctMutex.Lock(acctID)
	defer b.acctMutex.Unlock(acctID)

	totalCost, err := b.LockedValue(ctx, rawKey, feeSchedule, o)
	if err != nil {
		return err
	}

	log.Debugf("Total locked value for account %x (value=%v) after adding "+
		"order %v: %v", rawKey, acct.Value, o.Nonce(), totalCost)

	// Check if the trader can afford this set of orders in the worst case.
	if totalCost > acct.Value {
		return ErrInvalidAmt
	}

	// Order can be safely submitted.
	err = b.cfg.Store.SubmitOrder(ctx, o)
	if err != nil {
		return err
	}

	if err := b.ntfnServer.SendUpdate(&NewOrderUpdate{
		Order: o,
	}); err != nil {
		log.Errorf("unable to send order update: %v", err)
	}

	return nil
}

// CancelOrderWithPreimage sets an order's state to canceled if it has not yet
// been archived yet and is still pending.
func (b *Book) CancelOrderWithPreimage(ctx context.Context,
	noncePreimage lntypes.Preimage) error {

	preimageHash := noncePreimage.Hash()
	var nonce order.Nonce
	copy(nonce[:], preimageHash[:])
	return b.CancelOrder(ctx, nonce)
}

// CancelOrder sets an order's state to canceled if it has not yet been archived
// yet and is still pending.
func (b *Book) CancelOrder(ctx context.Context, nonce order.Nonce) error {
	log.Debugf("Canceling order %v", nonce)

	o, err := b.cfg.Store.GetOrder(ctx, nonce)
	if err != nil {
		return err
	}

	if o.Details().State.Archived() {
		return fmt.Errorf("cannot cancel archived")
	}

	err = b.cfg.Store.UpdateOrder(
		ctx, o.Nonce(), StateModifier(order.StateCanceled),
	)
	if err != nil {
		return err
	}

	_, isAsk := o.(*Ask)
	if err := b.ntfnServer.SendUpdate(&CancelledOrderUpdate{
		Ask:   isAsk,
		Nonce: nonce,
	}); err != nil {
		log.Errorf("unable to send order update: %v", err)
	}

	return err
}

// LockedValue uses the current active orders for the given account to
// calculate an upper bound of how much might be deducted from the account if
// they are matched. newOrders can be added to get this upper bound if
// additional orders are added.
func (b *Book) LockedValue(ctx context.Context, acctKey [33]byte,
	feeSchedule terms.FeeSchedule, newOrders ...ServerOrder) (
	btcutil.Amount, error) {

	// We fetch all existing orders for this account from the store.
	allOrders, err := b.cfg.Store.GetOrders(ctx)
	if err != nil {
		return 0, err
	}

	var reserved btcutil.Amount
	for _, o := range allOrders {
		// Filter by account.
		if o.Details().AcctKey != acctKey {
			continue
		}

		reserved += o.ReservedValue(feeSchedule)
	}

	// Add the new orders to the list if any and return the worst case
	// cost.
	for _, o := range newOrders {
		reserved += o.ReservedValue(feeSchedule)
	}

	return reserved, nil
}

// validateAccountState makes sure the account is in a state where we can
// accept a new order.
func (b *Book) validateAccountState(ctx context.Context,
	acctKey *btcec.PublicKey, acct *account.Account,
	bestHeight uint32) error {

	// Only allow orders to be submitted if the account is open, or open
	// and pending an update (so they can submit orders while the update is
	// confirming).
	switch acct.State {
	case account.StatePendingUpdate, account.StatePendingBatch,
		account.StateOpen:

	default:
		return fmt.Errorf("account must be open or pending open to "+
			"submit orders, instead state=%v", acct.State)
	}

	// Is the account banned? Don't accept the order.
	isBanned, expiration, err := b.cfg.Store.IsAccountBanned(
		ctx, acctKey, bestHeight,
	)
	if err != nil {
		return err
	}
	if isBanned {
		return account.NewErrBannedAccount(expiration)
	}

	return nil
}

// validateOrder makes sure the order is formally correct, has a correct
// signature and that the account has enough balance to actually execute the
// order.
func (b *Book) validateOrder(ctx context.Context, srvOrder ServerOrder) error {
	kit := srvOrder.ServerDetails()
	srvOrder.Details().State = order.StateSubmitted

	// Anything below the supply unit size cannot be filled anyway so we
	// don't allow any order size that's not dividable by the supply size.
	amt := srvOrder.Details().Amt
	if amt <= 0 || amt%order.BaseSupplyUnit != 0 {
		return fmt.Errorf("order amount must be multiple of %d sats",
			order.BaseSupplyUnit)
	}

	// Make sure the amount is consistent with Unit and UnitsUnfulfilled.
	if srvOrder.Details().Units.ToSatoshis() != amt ||
		srvOrder.Details().UnitsUnfulfilled.ToSatoshis() != amt {

		return fmt.Errorf("units and units unfulfilled must " +
			"translate exactly to amount")
	}

	// Verify the minimum units match amount has been properly set.
	minUnitsMatch := order.SupplyUnit(1)
	switch {
	case srvOrder.Details().MinUnitsMatch < minUnitsMatch:
		return fmt.Errorf("minimum units match %v must be above %v",
			srvOrder.Details().MinUnitsMatch, minUnitsMatch)

	case srvOrder.Details().MinUnitsMatch > srvOrder.Details().Units:
		return fmt.Errorf("minimum units match %v is above total "+
			"order units %v", srvOrder.Details().MinUnitsMatch,
			srvOrder.Details().Units)
	}

	// First validate the order signature.
	digest, err := srvOrder.Digest()
	if err != nil {
		return fmt.Errorf("unable to digest message for signature "+
			"verification: %v", err)
	}
	sigValid, err := b.cfg.Signer.VerifyMessage(
		ctx, digest[:], kit.Sig.ToSignatureBytes(),
		srvOrder.Details().AcctKey,
	)
	if err != nil {
		return fmt.Errorf("unable to verify order signature: %v", err)
	}
	if !sigValid {
		return fmt.Errorf("signature not valid for public key %x",
			srvOrder.Details().AcctKey)
	}

	// The max batch fee rate must be sane.
	if srvOrder.Details().MaxBatchFeeRate < chainfee.FeePerKwFloor {
		return fmt.Errorf("invalid max batch feerate")
	}

	// Now parse the order type specific fields and validate that the
	// account has enough balance for the requested order.
	var leaseDuration uint32
	switch o := srvOrder.(type) {
	case *Ask:
		leaseDuration = o.LeaseDuration()

	case *Bid:
		leaseDuration = o.LeaseDuration()

		// Make sure the self channel balance is correct.
		if o.SelfChanBalance > 0 {
			if err := o.ValidateSelfChanBalance(); err != nil {
				return err
			}
		}

		// Check the sidecar parameters if the flag is set on the order.
		if o.IsSidecar {
			if err := b.validateSidecarOrder(ctx, o); err != nil {
				return fmt.Errorf("error validating sidecar "+
					"order: %v", err)
			}
		}
	}

	// Only clients that understand multiple lease buckets are allowed to
	// create orders outside of the default/legacy bucket. Otherwise they
	// wouldn't know how to validate those batches.
	if srvOrder.Details().Version < order.VersionLeaseDurationBuckets &&
		leaseDuration != order.LegacyLeaseDurationBucket {

		return fmt.Errorf("cannot submit order outside of default %d "+
			"duration bucket with old trader client, please "+
			"update your software", order.LegacyLeaseDurationBucket)
	}

	// Next, we'll ensure that the duration is actual part of the current
	// set of duration buckets, and also that this market isn't closed and
	// is currently accepting orders.
	marketState := b.DurationBuckets().QueryMarketState(leaseDuration)
	switch marketState {
	case BucketStateAcceptingOrders, BucketStateClearingMarket:

	default:
		return fmt.Errorf("bucket for duration %v is in state: %v",
			leaseDuration, marketState)
	}

	// Only traders that understand channel types can provide one other than
	// the legacy default.
	if srvOrder.Details().Version < order.VersionChannelType &&
		srvOrder.Details().ChannelType != order.ChannelTypePeerDependent {
		return errors.New("cannot submit channel type preference with " +
			"old trader client, please update your software")
	}

	return nil
}

// validateSidecarOrder makes sure all order parameters are set correctly for
// a sidecar order.
func (b *Book) validateSidecarOrder(ctx context.Context, bid *Bid) error {
	// A sidecar order must have its version set accordingly.
	if bid.Version < order.VersionSidecarChannel {
		return fmt.Errorf("invalid order version %d for order with "+
			"sidecar ticket attached", bid.Version)
	}

	// Sidecar channels with a self channel balance need to have the minimum
	// matched units set to the bid size to avoid an overly complicated
	// execution protocol. We also check the size of the self channel
	// balance in the process.
	if err := bid.ValidateSelfChanBalance(); err != nil {
		return fmt.Errorf("invalid self balance for sidecar order: %v",
			err)
	}

	dbOrders, err := b.cfg.Store.GetOrders(ctx)
	if err != nil {
		return fmt.Errorf("error validating sidecar order against "+
			"existing orders: %v", err)
	}

	for _, dbOrder := range dbOrders {
		dbBid, isBid := dbOrder.(*Bid)
		if !isBid {
			continue
		}

		if dbBid.MultiSigKey == bid.MultiSigKey {
			return fmt.Errorf("an active order for the multisig " +
				"pubkey of the sidecar recipient already " +
				"exists, cancel it first before submitting a " +
				"new one")
		}
	}

	// As far as we can tell everything is in order with the order.
	return nil
}

// Subscribe returns a new subscription to the order book. Client will receive
// events each time an order is added, or cancelled.
func (b *Book) Subscribe() (*subscribe.Client, error) {
	return b.ntfnServer.Subscribe()
}
