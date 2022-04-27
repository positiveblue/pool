package chanenforcement

import "context"

// DefaultSource implements the PackageSource interface.
// EnforceLifetimeViolation is accomplished by banning the offender.
type DefaultSource struct {
	Store Store
}

// NewDefaultSource returns a new DefaultSource.
func NewDefaultSource(store Store) *DefaultSource {
	return &DefaultSource{Store: store}
}

// LifetimePackages retrieves all channel lifetime enforcement packages
// which still need to be acted upon.
func (p *DefaultSource) LifetimePackages() ([]*LifetimePackage, error) {
	return p.Store.LifetimePackages(context.Background())
}

// PruneLifetimePackage prunes all references to a channel's lifetime
// enforcement package once we've determined that a violation was not
// present.
func (p *DefaultSource) PruneLifetimePackage(
	pkg *LifetimePackage) error {

	return p.Store.DeleteLifetimePackage(context.Background(), pkg)
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

	return p.Store.EnforceLifetimeViolation(
		context.Background(), pkg, height,
	)
}
