package subastadb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/lightninglabs/subasta/chanenforcement"
	conc "go.etcd.io/etcd/client/v3/concurrency"
)

const (
	// lifetimePackageDir is the directory name under which we'll store all
	// channel lifetime enforcement packages. The key used for each package
	// consists of the channel's outpoint. This needs be prefixed with
	// topLevelDir to obtain the full path.
	//
	// TODO(wilmer): Group packages by their maturity height?
	lifetimePackageDir = "lifetime-packages"
)

var (
	// ErrLifetimePackageAlreadyExists is an error returned when we attempt
	// to store a channel lifetime package, but one already exists with the
	// same key.
	ErrLifetimePackageAlreadyExists = errors.New("channel lifetime " +
		"package already exists")
)

// lifetimeKeyPathPrefix returns the key path prefix under which we store _all_
// channels lifetime enforcement packages.
//
// The key path prefix is represented as follows:
//	bitcoin/clm/subasta/lifetime-packages
func (s *EtcdStore) lifetimeKeyPathPrefix() string {
	parts := []string{lifetimePackageDir}
	return s.getKeyPrefix(strings.Join(parts, keyDelimiter))
}

// lifetimeKeyPath returns the full path under which we store a channels's
// lifetime enforcement package.
//
// The key path is represented as follows:
//	bitcoin/clm/subasta/lifetime-packages/{channel_point}
func (s *EtcdStore) lifetimeKeyPath(pkg *chanenforcement.LifetimePackage) string {
	parts := []string{s.lifetimeKeyPathPrefix(), pkg.ChannelPoint.String()}
	return strings.Join(parts, keyDelimiter)
}

// StoreLifetimePackage persists to disk the given channel lifetime package.
func (s *EtcdStore) StoreLifetimePackage(ctx context.Context,
	pkg *chanenforcement.LifetimePackage) error {

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		return s.storeLifetimePackage(stm, pkg)
	})
	return err
}

// storeLifetimePackage stores the given channel lifetime package using the
// provided STM instance.
func (s *EtcdStore) storeLifetimePackage(stm conc.STM,
	pkg *chanenforcement.LifetimePackage) error {

	// Make sure a lifetime package with the same key doesn't already exist.
	k := s.lifetimeKeyPath(pkg)
	if len(stm.Get(k)) > 0 {
		return ErrLifetimePackageAlreadyExists
	}

	var buf bytes.Buffer
	if err := serializeLifetimePackage(&buf, pkg); err != nil {
		return err
	}

	stm.Put(k, buf.String())
	return nil
}

// LifetimePackages retrieves all channel lifetime enforcement packages which
// still need to be acted upon.
func (s *EtcdStore) LifetimePackages(ctx context.Context) (
	[]*chanenforcement.LifetimePackage, error) {

	resp, err := s.getAllValuesByPrefix(ctx, s.lifetimeKeyPathPrefix())
	if err != nil {
		return nil, err
	}

	r := bytes.NewReader(nil)
	pkgs := make([]*chanenforcement.LifetimePackage, 0, len(resp))
	for _, v := range resp {
		r.Reset(v)

		pkg, err := deserializeLifetimePackage(r)
		if err != nil {
			return nil, err
		}
		pkgs = append(pkgs, pkg)
	}

	return pkgs, nil
}

// DeleteLifetimePackage deletes all references to a channel's lifetime
// enforcement package once we've determined that a violation was not
// present.
func (s *EtcdStore) DeleteLifetimePackage(ctx context.Context,
	pkg *chanenforcement.LifetimePackage) error {

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		stm.Del(s.lifetimeKeyPath(pkg))
		return nil
	})
	return err
}

// PruneLifetimePackage prunes all references to a channel's lifetime
// enforcement package once we've determined that a violation was not present.
func (s *EtcdStore) PruneLifetimePackage(ctx context.Context,
	pkg *chanenforcement.LifetimePackage) error {

	return s.DeleteLifetimePackage(ctx, pkg)
}

// EnforceLifetimeViolation punishes the channel initiator due to a channel
// lifetime violation, along with cleaning up the associated lifetime
// enforcement package. The height parameter should represent the chain height
// at which the punishable offense was detected.
func (s *EtcdStore) EnforceLifetimeViolation(ctx context.Context,
	pkg *chanenforcement.LifetimePackage, height uint32) error {

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		stm.Del(s.lifetimeKeyPath(pkg))
		return s.banTrader(
			stm, pkg.AskAccountKey, pkg.AskNodeKey, height,
		)
	})
	return err
}

func serializeLifetimePackage(w *bytes.Buffer,
	pkg *chanenforcement.LifetimePackage) error {

	return WriteElements(
		w, pkg.ChannelPoint, pkg.ChannelScript, pkg.HeightHint,
		pkg.MaturityHeight, pkg.Version, pkg.AskAccountKey,
		pkg.BidAccountKey, pkg.AskNodeKey, pkg.BidNodeKey,
		pkg.AskPaymentBasePoint, pkg.BidPaymentBasePoint,
	)
}

func deserializeLifetimePackage(r io.Reader) (*chanenforcement.LifetimePackage,
	error) {

	var pkg chanenforcement.LifetimePackage
	err := ReadElements(
		r, &pkg.ChannelPoint, &pkg.ChannelScript, &pkg.HeightHint,
		&pkg.MaturityHeight, &pkg.Version, &pkg.AskAccountKey,
		&pkg.BidAccountKey, &pkg.AskNodeKey, &pkg.BidNodeKey,
		&pkg.AskPaymentBasePoint, &pkg.BidPaymentBasePoint,
	)
	if err != nil {
		return nil, err
	}

	return &pkg, nil
}
