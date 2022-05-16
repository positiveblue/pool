package subastadb

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/jackc/pgx/v4"
	"github.com/lightninglabs/subasta/ban"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/subastadb/postgres"
	pg "github.com/lightninglabs/subasta/subastadb/postgres"
	"github.com/lightningnetwork/lnd/chanbackup"
)

// StoreLifetimePackage persists to disk the given channel lifetime
// package.
func (s *SQLStore) StoreLifetimePackage(ctx context.Context,
	pkg *chanenforcement.LifetimePackage) error {

	txBody := func(txQueries *postgres.Queries) error {
		_, err := txQueries.GetLifetimePackage(
			ctx, pkg.ChannelPoint.String(),
		)
		switch {
		case err == nil:
			return ErrLifetimePackageAlreadyExists

		case err != pgx.ErrNoRows:
			return err
		}

		params := upsertLifetimePackageParams(pkg)
		return txQueries.UpsertLifetimePackage(ctx, params)
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to store lifetime package: %w", err)
	}

	return nil
}

// LifetimePackages retrieves all channel lifetime enforcement packages
// which still need to be acted upon.
func (s *SQLStore) LifetimePackages(
	ctx context.Context) ([]*chanenforcement.LifetimePackage, error) {

	errMsg := "unable to get lifetime packages: %v"

	var rows []postgres.LifetimePackage
	txBody := func(txQueries *postgres.Queries) error {
		var err error
		params := postgres.GetLifetimePackagesParams{}
		rows, err = txQueries.GetLifetimePackages(ctx, params)
		if err != nil {
			return err
		}

		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return nil, fmt.Errorf(errMsg,
			err)
	}

	pkgs := make([]*chanenforcement.LifetimePackage, 0, len(rows))
	for _, row := range rows {
		pkg, err := unmarshalLifetimePackage(row)
		if err != nil {
			return nil, fmt.Errorf(errMsg, err)
		}
		pkgs = append(pkgs, pkg)
	}

	return pkgs, nil
}

// DeleteLifetimePackage deletes all references to a channel's lifetime
// enforcement package once we've determined that a violation was not
// present.
func (s *SQLStore) DeleteLifetimePackage(
	ctx context.Context, pkg *chanenforcement.LifetimePackage) error {

	_, err := s.queries.DeleteLifetimePackage(
		ctx, pkg.ChannelPoint.String(),
	)
	if err != nil {
		return fmt.Errorf("unable to delete lifetime package: %v", err)
	}

	return err
}

// EnforceLifetimeViolation punishes the channel initiator due to a channel
// lifetime violation.
//
// TODO(positiveblue): delete this from the store interface after migrating
// to postgres.
func (s *SQLStore) EnforceLifetimeViolation(ctx context.Context,
	pkg *chanenforcement.LifetimePackage, accKey, nodeKey *btcec.PublicKey,
	accInfo, nodeInfo *ban.Info) error {

	txBody := func(txQueries *postgres.Queries) error {
		err := banAccountWithTx(ctx, txQueries, accKey, accInfo)
		if err != nil {
			return nil
		}

		err = banNodeWithTx(ctx, txQueries, nodeKey, nodeInfo)
		if err != nil {
			return err
		}

		channelPoint := pkg.ChannelPoint.String()
		_, err = txQueries.DeleteLifetimePackage(ctx, channelPoint)
		if err != nil {
			return err
		}

		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to enforce lifetime violation for "+
			"package(%s): %v", pkg.ChannelPoint.String(), err)
	}

	return nil
}

// upsertLifetimePackageParams returns the parameters for the db to
// create/update a lifetimepackage.
func upsertLifetimePackageParams(
	pkg *chanenforcement.LifetimePackage) pg.UpsertLifetimePackageParams {

	hash, idx := marshalOutPoint(pkg.ChannelPoint)

	askPaymentBasePoint := pkg.AskPaymentBasePoint.SerializeCompressed()
	bidPaymentBasePoint := pkg.BidPaymentBasePoint.SerializeCompressed()

	return pg.UpsertLifetimePackageParams{
		ChannelPointString:  pkg.ChannelPoint.String(),
		ChannelPointHash:    hash,
		ChannelPointIndex:   idx,
		ChannelScript:       pkg.ChannelScript,
		HeightHint:          int64(pkg.HeightHint),
		MaturityHeight:      int64(pkg.MaturityHeight),
		Version:             int16(pkg.Version),
		AskAccountKey:       pkg.AskAccountKey.SerializeCompressed(),
		BidAccountKey:       pkg.BidAccountKey.SerializeCompressed(),
		AskNodeKey:          pkg.AskNodeKey.SerializeCompressed(),
		BidNodeKey:          pkg.BidNodeKey.SerializeCompressed(),
		AskPaymentBasePoint: askPaymentBasePoint,
		BidPaymentBasePoint: bidPaymentBasePoint,
	}
}

// unmarshalLifetimePackage deserializes a *chanenforcement.LifetimePackage
// from its serialized version used in the db.
func unmarshalLifetimePackage(
	row pg.LifetimePackage) (*chanenforcement.LifetimePackage, error) {

	errMsg := "unable to unmarshall lifetime package: %v"

	channelPoint, err := unmarshalOutPoint(
		row.ChannelPointHash, row.ChannelPointIndex,
	)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	version := chanbackup.SingleBackupVersion(row.Version)

	askAccountKey, err := btcec.ParsePubKey(row.AskAccountKey)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}
	bidAccountKey, err := btcec.ParsePubKey(row.BidAccountKey)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	askNodeKey, err := btcec.ParsePubKey(row.AskNodeKey)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}
	bidNodeKey, err := btcec.ParsePubKey(row.BidNodeKey)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	askBasePoint, err := btcec.ParsePubKey(row.AskPaymentBasePoint)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}
	bidBasePoint, err := btcec.ParsePubKey(row.BidPaymentBasePoint)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	return &chanenforcement.LifetimePackage{
		ChannelPoint:        *channelPoint,
		ChannelScript:       row.ChannelScript,
		HeightHint:          uint32(row.HeightHint),
		MaturityHeight:      uint32(row.MaturityHeight),
		Version:             version,
		AskAccountKey:       askAccountKey,
		BidAccountKey:       bidAccountKey,
		AskNodeKey:          askNodeKey,
		BidNodeKey:          bidNodeKey,
		AskPaymentBasePoint: askBasePoint,
		BidPaymentBasePoint: bidBasePoint,
	}, nil
}
