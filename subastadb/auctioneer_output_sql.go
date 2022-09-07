package subastadb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/jackc/pgx/v4"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/subastadb/postgres"
	"github.com/lightningnetwork/lnd/keychain"
)

// FetchAuctioneerAccount retrieves the current information pertaining
// to the current auctioneer output state.
func (s *SQLStore) FetchAuctioneerAccount(
	ctx context.Context) (*account.Auctioneer, error) {

	row, err := s.queries.GetAuctioneerAccount(ctx)
	switch {
	case err == pgx.ErrNoRows:
		return nil, account.ErrNoAuctioneerAccount

	case err != nil:
		return nil, fmt.Errorf("unable to get auctioneer account: %v",
			err)
	}

	auctioneer, err := unmarshalAuctioneerAccount(row)
	if err != nil {
		return nil, fmt.Errorf("unable to get auctioneer account: %v",
			err)
	}
	return auctioneer, nil
}

// UpdateAuctioneerAccount updates the current auctioneer output
// in-place and also updates the per batch key according to the state in
// the auctioneer's account.
func (s *SQLStore) UpdateAuctioneerAccount(ctx context.Context,
	auctioneer *account.Auctioneer) error {

	txBody := func(txQueries *postgres.Queries) error {
		return upsertAuctioneerAccountWithTx(
			ctx, txQueries, auctioneer,
		)
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		pubKey := auctioneer.AuctioneerKey.PubKey.SerializeCompressed()
		return fmt.Errorf("unable to update auctioneer "+
			"account(%x): %v", pubKey, err)
	}
	return nil
}

// CreateAuctioneerSnapshot stores a snapshot of the auctioneer data.
//
// NOTE: the auctioneer.BatchKey needs to be link to the batches table,
// which means that we are able to store snapshots only after the batch
// has been created and stored.
func (s *SQLStore) CreateAuctioneerSnapshot(ctx context.Context,
	auctioneer *account.Auctioneer) error {

	txBody := func(txQueries *postgres.Queries) error {
		return createAuctioneerSnapshotWithTx(
			ctx, txQueries, auctioneer,
		)
	}

	return s.ExecTx(ctx, txBody)
}

// createAuctioneerSnapshotWithTx creates a new auctioneer snapshot using
// the provided queries struct.
func createAuctioneerSnapshotWithTx(ctx context.Context,
	txQueries *postgres.Queries, auctioneer *account.Auctioneer) error {

	params := postgres.CreateAuctioneerSnapshotParams{
		BatchKey:      auctioneer.BatchKey[:],
		Balance:       int64(auctioneer.Balance),
		OutPointHash:  auctioneer.OutPoint.Hash[:],
		OutPointIndex: int64(auctioneer.OutPoint.Index),
		Version:       int16(auctioneer.Version),
	}
	return txQueries.CreateAuctioneerSnapshot(ctx, params)
}

// GetAuctioneerBalance returns the balance of the auctioneer account at the
// given point in time.
func (s *SQLStore) GetAuctioneerBalance(ctx context.Context,
	date time.Time) (btcutil.Amount, error) {

	params := sql.NullTime{
		Time:  date,
		Valid: true,
	}

	diff, err := s.queries.GetAuctioneerSnapshotByDate(ctx, params)
	if err != nil {
		return 0, err
	}

	return btcutil.Amount(diff.Balance), nil
}

// upsertAuctioneerAccountWithTx inserts/updates the auctioneer account using
// the provided queries struct.
func upsertAuctioneerAccountWithTx(ctx context.Context,
	txQueries *postgres.Queries, auctioneer *account.Auctioneer) error {

	family, index, pubKey := marshalKeyDescriptor(
		auctioneer.AuctioneerKey,
	)

	outPointHash, outPointIndex := marshalOutPoint(auctioneer.OutPoint)

	params := postgres.UpsertAuctioneerAccountParams{
		Balance:             int64(auctioneer.Balance),
		BatchKey:            auctioneer.BatchKey[:],
		IsPending:           auctioneer.IsPending,
		AuctioneerKeyFamily: family,
		AuctioneerKeyIndex:  index,
		AuctioneerPublicKey: pubKey,
		OutPointHash:        outPointHash,
		OutPointIndex:       outPointIndex,
		Version:             int16(auctioneer.Version),
	}

	return txQueries.UpsertAuctioneerAccount(ctx, params)
}

// marshalKeyDescriptor maps a *keychain.KeyDescriptor to its serialized
// version used in the db.
func marshalKeyDescriptor(kd *keychain.KeyDescriptor) (int64, int64, []byte) {
	return int64(kd.Family), int64(kd.Index),
		kd.PubKey.SerializeCompressed()
}

// unmarshalKeyDescriptor deserializes a *keychain.KeyDescriptor from its
// serialized version used in the db.
func unmarshalKeyDescriptor(family, index int64,
	key []byte) (*keychain.KeyDescriptor, error) {

	pubKey, err := btcec.ParsePubKey(key)
	if err != nil {
		return nil, fmt.Errorf("unable to  key descriptor: "+
			"%v", err)
	}

	return &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: keychain.KeyFamily(family),
			Index:  uint32(index),
		},
		PubKey: pubKey,
	}, nil
}

// unmarshalAuctioneerAccount deserializes an *account.Auctioneer from its
// serialized version used in the db.
func unmarshalAuctioneerAccount(
	row postgres.AuctioneerAccount) (*account.Auctioneer, error) {

	errMsg := "unable to unmarshall auctioneer account: %v"

	outpoint, err := unmarshalOutPoint(row.OutPointHash, row.OutPointIndex)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	auctioneerKey, err := unmarshalKeyDescriptor(
		row.AuctioneerKeyFamily,
		row.AuctioneerKeyIndex,
		row.AuctioneerPublicKey,
	)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	var batchKey [33]byte
	copy(batchKey[:], row.BatchKey)

	return &account.Auctioneer{
		OutPoint:      *outpoint,
		Balance:       btcutil.Amount(row.Balance),
		AuctioneerKey: auctioneerKey,
		BatchKey:      batchKey,
		IsPending:     row.IsPending,
		Version:       account.AuctioneerVersion(row.Version),
	}, nil
}
