package subastadb

import (
	"context"

	"github.com/btcsuite/btcd/btcec/v2"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
)

// BatchKey returns the current per-batch key that must be used to tweak account
// trader keys with.
func (s *SQLStore) BatchKey(ctx context.Context) (*btcec.PublicKey, error) {
	return nil, ErrNotImplemented
}

// PersistBatchResult atomically updates all modified orders/accounts,
// persists a snapshot of the batch and switches to the next batch ID.
// If any single operation fails, the whole set of changes is rolled
// back.
func (s *SQLStore) PersistBatchResult(context.Context, []orderT.Nonce,
	[][]order.Modifier, []*btcec.PublicKey, [][]account.Modifier,
	*account.Auctioneer, orderT.BatchID, *BatchSnapshot, *btcec.PublicKey,
	[]*chanenforcement.LifetimePackage) error {

	return ErrNotImplemented
}

// GetBatchSnapshot returns the self-contained snapshot of a batch with
// the given ID as it was recorded at the time.
func (s *SQLStore) GetBatchSnapshot(context.Context, orderT.BatchID) (*BatchSnapshot, error) {

	return nil, ErrNotImplemented
}

// ConfirmBatch finalizes a batch on disk, marking it as pending (unconfirmed)
// no longer.
func (s *SQLStore) ConfirmBatch(ctx context.Context, batchID orderT.BatchID) error {

	return ErrNotImplemented
}

// BatchConfirmed returns true if the target batch has been marked finalized
// (confirmed) on disk.
func (s *SQLStore) BatchConfirmed(context.Context, orderT.BatchID) (bool, error) {

	return false, ErrNotImplemented
}

// Batches retrieves all existing batches.
func (s *SQLStore) Batches(
	ctx context.Context) (map[orderT.BatchID]*BatchSnapshot, error) {

	return nil, ErrNotImplemented
}
