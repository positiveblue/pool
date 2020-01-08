package order

import (
	"context"
	"fmt"
	"sync"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/loop/lndclient"
)

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
}

// Book is the representation of the auctioneer's order book and is responsible
// for accepting, matching and executing orders.
type Book struct {
	started sync.Once
	stopped sync.Once

	cfg BookConfig

	wg   sync.WaitGroup
	quit chan struct{}
}

// NewBook instantiates a new Book backed by the given config.
func NewBook(cfg *BookConfig) *Book {
	return &Book{
		cfg:  *cfg,
		quit: make(chan struct{}),
	}
}

// Start starts all concurrent tasks the manager is responsible for.
func (b *Book) Start() error {
	var err error
	b.started.Do(func() {
		// TODO(guggero): implement once order book is no longer
		//  stateless.
	})
	return err
}

// Stop stops all concurrent tasks the manager is responsible for.
func (b *Book) Stop() {
	b.stopped.Do(func() {
		close(b.quit)
		b.wg.Wait()
	})
}

// PrepareOrder validates an incoming order and stores it to the database.
func (b *Book) PrepareOrder(ctx context.Context, o ServerOrder) error {
	err := b.validateOrder(ctx, o)
	if err != nil {
		return err
	}

	return b.cfg.Store.SubmitOrder(ctx, o)
}

// CancelOrder sets an order's state to canceled if it has not yet been
// processed and is still pending.
func (b *Book) CancelOrder(ctx context.Context, nonce order.Nonce) error {
	o, err := b.cfg.Store.GetOrder(ctx, nonce)
	if err != nil {
		return err
	}

	if o.Details().State != order.StateSubmitted {
		return fmt.Errorf("cannot cancel order that is not in status " +
			"'submitted'")
	}

	return b.cfg.Store.UpdateOrder(
		ctx, o.Nonce(), StateModifier(order.StateCanceled),
	)
}

// validateOrder makes sure the order is formally correct, has a correct
// signature and that the account has enough balance to actually execute the
// order.
func (b *Book) validateOrder(ctx context.Context, srvOrder ServerOrder) error {
	kit := srvOrder.ServerDetails()
	kit.ChanType = ChanTypeDefault
	srvOrder.Details().State = order.StateSubmitted

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
		balanceNeeded = o.Amt + b.cfg.SubmitFee

	case *Bid:
		if o.MinDuration() == 0 {
			return fmt.Errorf("invalid min duration")
		}
		orderFee := order.CalcFee(o.Amt, o.FixedRate, o.MinDuration())
		balanceNeeded = orderFee + b.cfg.SubmitFee
	}

	acct, err := b.cfg.Store.Account(ctx, srvOrder.Details().AcctKey)
	if err != nil {
		return fmt.Errorf("unable to locate account with key %x: %v",
			srvOrder.Details().AcctKey[:], err)
	}
	if acct.Value < balanceNeeded {
		return ErrInvalidAmt
	}
	return nil
}
