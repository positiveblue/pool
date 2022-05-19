package chanenforcement

import (
	"context"

	"github.com/lightninglabs/subasta/ban"
)

// DefaultSource implements the PackageSource interface.
// EnforceLifetimeViolation is accomplished by banning the offender.
type DefaultSource struct {
	Store      Store
	BanManager ban.Manager
}

// NewDefaultSource returns a new DefaultSource.
func NewDefaultSource(banManager ban.Manager, store Store) *DefaultSource {
	return &DefaultSource{
		Store:      store,
		BanManager: banManager,
	}
}

// LifetimePackages retrieves all channel lifetime enforcement packages
// which still need to be acted upon.
func (p *DefaultSource) LifetimePackages() ([]*LifetimePackage, error) {
	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultWaitTimeout)
	defer cancel()

	return p.Store.LifetimePackages(ctxt)
}

// PruneLifetimePackage prunes all references to a channel's lifetime
// enforcement package once we've determined that a violation was not
// present.
func (p *DefaultSource) PruneLifetimePackage(
	pkg *LifetimePackage) error {

	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultWaitTimeout)
	defer cancel()

	return p.Store.DeleteLifetimePackage(ctxt, pkg)
}

// EnforceLifetimeViolation punishes the channel initiator due to a
// channel lifetime violation, along with cleaning up the associated
// lifetime enforcement package. The height parameter should represent
// the chain height at which the punishable offense was detected.
//
// In the case of the DefaultSource, the punishment is getting
// banned from the service.
func (p *DefaultSource) EnforceLifetimeViolation(pkg *LifetimePackage,
	height uint32) error {

	// TODO (positiveblue): execute bans + delete in a unique tx.

	// Ban the account and node key.
	currAccInfo, err := p.BanManager.GetAccountBan(
		pkg.AskAccountKey, height,
	)
	if err != nil {
		return err
	}

	accInfo := p.BanManager.CalculateNewInfo(height, currAccInfo)

	currNodeInfo, err := p.BanManager.GetNodeBan(pkg.AskNodeKey, height)
	if err != nil {
		return err
	}

	nodeInfo := p.BanManager.CalculateNewInfo(height, currNodeInfo)

	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultWaitTimeout)
	defer cancel()

	// Stop monitoring the lifetime package.
	return p.Store.EnforceLifetimeViolation(
		ctxt, pkg, pkg.AskAccountKey, pkg.AskNodeKey, accInfo, nodeInfo)
}
