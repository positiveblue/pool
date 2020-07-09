package chanenforcement

import (
	"context"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/llm/chaninfo"
	"github.com/lightningnetwork/lnd/chanbackup"
)

// PackageSource is responsible for retrieving any existing channel lifetime
// enforcement packages and pruning them accordingly once they're no longer
// actionable.
type PackageSource interface {
	// LifetimePackages retrieves all channel lifetime enforcement packages
	// which still need to be acted upon.
	LifetimePackages(context.Context) ([]*LifetimePackage, error)

	// PruneLifetimePackage prunes all references to a channel's lifetime
	// enforcement package once we've determined that a violation was not
	// present.
	PruneLifetimePackage(context.Context, *LifetimePackage) error

	// EnforceLifetimeViolation punishes the channel initiator due to a
	// channel lifetime violation, along with cleaning up the associated
	// lifetime enforcement package. The height parameter should represent
	// the chain height at which the punishable offense was detected.
	//
	// NOTE: Implementations of this interface are free to choose their
	// desired punishment heuristic.
	EnforceLifetimeViolation(_ context.Context, _ *LifetimePackage,
		height uint32) error
}

// LifetimePackage contains all of the information necessary for the auctioneer
// to watch for channel lifetime violations and act accordingly.
type LifetimePackage struct {
	// ChannelPoint is the outpoint of the channel output.
	ChannelPoint wire.OutPoint

	// ChannelScript is the script of the channel output.
	ChannelScript []byte

	// HeightHint is the earliest height in the chain that the channel can
	// be found at.
	HeightHint uint32

	// MaturityDelta is the height delta applied to the channel's
	// confirmation height after which we'll stop enforcing channel
	// lifetime. If a party closes the channel before this, then they'll be
	// punished accordingly.
	MaturityDelta uint32

	// Version is the version of the channel. This uniquely identifies the
	// type of channel we're working with, allowing us to derive the
	// expected output scripts of the channel's broadcast commitment
	// transaction.
	Version chanbackup.SingleBackupVersion

	// AskAccountKey is the account key of the asker's account used to fund
	// the channel.
	AskAccountKey *btcec.PublicKey

	// BidAccountKey is the account key of the bidder's account used to pay
	// for the channel.
	BidAccountKey *btcec.PublicKey

	// AskNodeKey is the node key of the channel initiator.
	AskNodeKey *btcec.PublicKey

	// BidNodeKey is the node key of the channel non-initiator.
	BidNodeKey *btcec.PublicKey

	// AskPaymentBasePoint is the channel initiator's base public key used
	// within the non-delayed pay-to-self output on the commitment
	// transaction.
	AskPaymentBasePoint *btcec.PublicKey

	// RemotePaymentBasePoint is the channel non-initiator's base public key
	// used within the non-delayed pay-to-self output on the commitment
	// transaction.
	BidPaymentBasePoint *btcec.PublicKey
}

// NewLifetimePackage constructs and verifies a channel's lifetime enforcement
// package. Verification involves ensuring that both traders (asker and bidder)
// submit their channel info honestly.
func NewLifetimePackage(channelPoint wire.OutPoint,
	channelScript []byte, heightHint, maturityDelta uint32,
	askAccountKey, bidAccountKey *btcec.PublicKey,
	askChannelInfo, bidChannelInfo *chaninfo.ChannelInfo) (
	*LifetimePackage, error) {

	err := askChannelInfo.Match(bidChannelInfo)
	if err != nil {
		return nil, err
	}

	return &LifetimePackage{
		ChannelPoint:        channelPoint,
		ChannelScript:       channelScript,
		HeightHint:          heightHint,
		MaturityDelta:       maturityDelta,
		Version:             askChannelInfo.Version,
		AskAccountKey:       askAccountKey,
		BidAccountKey:       bidAccountKey,
		AskNodeKey:          askChannelInfo.LocalNodeKey,
		BidNodeKey:          askChannelInfo.RemoteNodeKey,
		AskPaymentBasePoint: askChannelInfo.LocalPaymentBasePoint,
		BidPaymentBasePoint: askChannelInfo.RemotePaymentBasePoint,
	}, nil
}
