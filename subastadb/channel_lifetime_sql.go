package subastadb

import (
	"context"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/subasta/ban"
	"github.com/lightninglabs/subasta/chanenforcement"
)

// StoreLifetimePackage persists to disk the given channel lifetime
// package.
func (s *SQLStore) StoreLifetimePackage(ctx context.Context,
	pkg *chanenforcement.LifetimePackage) error {

	return ErrNotImplemented
}

// LifetimePackages retrieves all channel lifetime enforcement packages
// which still need to be acted upon.
func (s *SQLStore) LifetimePackages(
	ctx context.Context) ([]*chanenforcement.LifetimePackage, error) {

	return nil, ErrNotImplemented
}

// DeleteLifetimePackage deletes all references to a channel's lifetime
// enforcement package once we've determined that a violation was not
// present.
func (s *SQLStore) DeleteLifetimePackage(
	ctx context.Context, pkg *chanenforcement.LifetimePackage) error {

	return ErrNotImplemented
}

// EnforceLifetimeViolation punishes the channel initiator due to a channel
// lifetime violation.
//
// TODO(positiveblue): delete this from the store interface after migrating
// to postgres.
func (s *SQLStore) EnforceLifetimeViolation(ctx context.Context,
	pkg *chanenforcement.LifetimePackage, accKey, nodeKey *btcec.PublicKey,
	accInfo, nodeInfo *ban.Info) error {

	return ErrNotImplemented
}
