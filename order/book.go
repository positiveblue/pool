package order

import (
	"context"
	"fmt"
	"sync"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/subasta/account"
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

	// SubmitFee is the fee the auctioneer server takes for offering its
	// services of keeping an order book and matching orders. This
	// represents a flat, one-time fee in satoshis.
	SubmitFee btcutil.Amount

	// MaxDuration is the maximum value for a bid's min duration or an ask's
	// max duration.
	MaxDuration uint32
}

// Book is the representation of the auctioneer's order book and is responsible
// for accepting, matching and executing orders.
type Book struct {
	started sync.Once
	stopped sync.Once

	cfg BookConfig

	ntfnServer *subscribe.Server

	wg   sync.WaitGroup
	quit chan struct{}
}

// NewBook instantiates a new Book backed by the given config.
func NewBook(cfg *BookConfig) *Book {
	return &Book{
		ntfnServer: subscribe.NewServer(),
		cfg:        *cfg,
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

// PrepareOrder validates an incoming order and stores it to the database.
func (b *Book) PrepareOrder(ctx context.Context, o ServerOrder,
	bestHeight uint32) error {

	err := b.validateOrder(ctx, o, bestHeight)
	if err != nil {
		return err
	}

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

// CancelOrder sets an order's state to canceled if it has not yet been
// archived yet and is still pending.
func (b *Book) CancelOrder(ctx context.Context, nonce order.Nonce) error {
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

// validateOrder makes sure the order is formally correct, has a correct
// signature and that the account has enough balance to actually execute the
// order.
func (b *Book) validateOrder(ctx context.Context, srvOrder ServerOrder,
	bestHeight uint32) error {

	kit := srvOrder.ServerDetails()
	kit.ChanType = ChanTypeDefault
	srvOrder.Details().State = order.StateSubmitted

	// Anything below the supply unit size cannot be filled anyway so we
	// don't allow any order size that's not dividable by the supply size.
	amt := srvOrder.Details().Amt
	if amt == 0 || amt%btcutil.Amount(order.BaseSupplyUnit) != 0 {
		return fmt.Errorf("order amount must be multiple of %d sats",
			order.BaseSupplyUnit)
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

	// Now parse the order type specific fields and validate that the
	// account has enough balance for the requested order.
	var balanceNeeded btcutil.Amount
	switch o := srvOrder.(type) {
	case *Ask:
		if o.MaxDuration() == 0 {
			return fmt.Errorf("invalid max duration")
		}
		if o.MaxDuration() > b.cfg.MaxDuration {
			return fmt.Errorf("maximum allowed value for max "+
				"duration is %d", b.cfg.MaxDuration)
		}
		balanceNeeded = o.Amt + b.cfg.SubmitFee

	case *Bid:
		if o.MinDuration() == 0 {
			return fmt.Errorf("invalid min duration")
		}
		if o.MinDuration() > b.cfg.MaxDuration {
			return fmt.Errorf("maximum allowed value for min "+
				"duration is %d", b.cfg.MaxDuration)
		}
		rate := order.FixedRatePremium(o.FixedRate)
		orderFee := rate.LumpSumPremium(o.Amt, o.MinDuration())
		balanceNeeded = orderFee + b.cfg.SubmitFee
	}

	acctKey, err := btcec.ParsePubKey(
		srvOrder.Details().AcctKey[:], btcec.S256(),
	)
	if err != nil {
		return err
	}
	acct, err := b.cfg.Store.Account(ctx, acctKey, false)
	if err != nil {
		return fmt.Errorf("unable to locate account with key %x: %v",
			acctKey.SerializeCompressed(), err)
	}
	if acct.Value < balanceNeeded {
		return ErrInvalidAmt
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

// Subscribe returns a new subscription to the order book. Client will receive
// events each time an order is added, or cancelled.
func (b *Book) Subscribe() (*subscribe.Client, error) {
	return b.ntfnServer.Subscribe()
}
