package subastadb

import (
	"context"

	"github.com/btcsuite/btcd/btcec/v2"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/traderterms"
)

// LeaseDurationStore is responsible for storing and retrieving information
// about lease durations reliably.
type LeaseDurationStore interface {
	// StoreLeaseDuration persists to disk the given lease duration bucket.
	StoreLeaseDuration(ctx context.Context, duration uint32,
		marketState order.DurationBucketState) error

	// LeaseDurations retrieves all lease duration buckets.
	LeaseDurations(ctx context.Context) (
		map[uint32]order.DurationBucketState, error)

	// RemoveLeaseDuration removes a single lease duration bucket from the
	// database.
	RemoveLeaseDuration(ctx context.Context, duration uint32) error
}

// Store is the main interface for subasta. It is responsible for storing and
// retrieving information about accounts, orders, tradeterms...
type Store interface {
	// Init initializes the necessary versioning state if the database hasn't
	// already been created in the past.
	Init(ctx context.Context) error

	account.Store

	order.Store

	traderterms.Store

	LeaseDurationStore

	// PersistBatchResult atomically updates all modified orders/accounts,
	// persists a snapshot of the batch and switches to the next batch ID.
	// If any single operation fails, the whole set of changes is rolled
	// back.
	PersistBatchResult(context.Context, []orderT.Nonce, [][]order.Modifier,
		[]*btcec.PublicKey, [][]account.Modifier, *account.Auctioneer,
		orderT.BatchID, *BatchSnapshot, *btcec.PublicKey,
		[]*chanenforcement.LifetimePackage) error

	// GetBatchSnapshot returns the self-contained snapshot of a batch with
	// the given ID as it was recorded at the time.
	GetBatchSnapshot(context.Context, orderT.BatchID) (*BatchSnapshot, error)

	// BanAccount attempts to ban the account associated with a trader
	// starting from the current height of the chain. The duration of the
	// ban will depend on how many times the node has been banned before and
	// grows exponentially, otherwise it is 144 blocks.
	BanAccount(context.Context, *btcec.PublicKey, uint32) error

	// IsTraderBanned determines whether the trader's account or node is
	// banned at the current height.
	IsTraderBanned(context.Context, [33]byte, [33]byte, uint32) (bool,
		error)

	// ConfirmBatch finalizes a batch on disk, marking it as pending (unconfirmed)
	// no longer.
	ConfirmBatch(ctx context.Context, batchID orderT.BatchID) error

	// BatchConfirmed returns true if the target batch has been marked finalized
	// (confirmed) on disk.
	BatchConfirmed(context.Context, orderT.BatchID) (bool, error)
}
