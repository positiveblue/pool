package subastadb

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightninglabs/aperture/lsat"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/ban"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/ratings"
	"github.com/lightninglabs/subasta/traderterms"
	"github.com/lightninglabs/subasta/venue/matching"
)

var (
	// InitialBatchKey serves as our initial global batch key. This key will
	// be incremented by the curve's base point every time a new batch is
	// cleared.
	initialBatchKeyBytes, _ = hex.DecodeString(
		"02824d0cbac65e01712124c50ff2cc74ce22851d7b444c1bf2ae66afefb8eaf27f",
	)
	InitialBatchKey, _ = btcec.ParsePubKey(initialBatchKeyBytes)
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

	chanenforcement.Store

	ban.Store

	// Batches retrieves all existing batches.
	Batches(context.Context) (map[orderT.BatchID]*matching.BatchSnapshot,
		error)

	// PersistBatchResult atomically updates all modified orders/accounts,
	// persists a snapshot of the batch and switches to the next batch ID.
	// If any single operation fails, the whole set of changes is rolled
	// back.
	PersistBatchResult(context.Context, []orderT.Nonce, [][]order.Modifier,
		[]*btcec.PublicKey, [][]account.Modifier, *account.Auctioneer,
		orderT.BatchID, *matching.BatchSnapshot, *btcec.PublicKey,
		[]*chanenforcement.LifetimePackage) error

	// GetBatchSnapshot returns the self-contained snapshot of a batch with
	// the given ID as it was recorded at the time.
	GetBatchSnapshot(context.Context, orderT.BatchID) (*matching.BatchSnapshot, error)

	// ConfirmBatch finalizes a batch on disk, marking it as pending (unconfirmed)
	// no longer.
	ConfirmBatch(ctx context.Context, batchID orderT.BatchID) error

	// BatchConfirmed returns true if the target batch has been marked finalized
	// (confirmed) on disk.
	BatchConfirmed(context.Context, orderT.BatchID) (bool, error)
}

// AdminStore is the main interface for the subasta admin. It extends the
// main Store.
type AdminStore interface {
	Store

	// UpdateAccountDiff updates an account's pending diff.
	UpdateAccountDiff(ctx context.Context, accountKey *btcec.PublicKey,
		modifiers []account.Modifier) error

	// DeleteAccountDiff deletes an account's pending diff.
	DeleteAccountDiff(ctx context.Context,
		accountKey *btcec.PublicKey) error

	// RemoveReservation deletes a reservation identified by the LSAT ID.
	RemoveReservation(ctx context.Context, id lsat.TokenID) error

	// NodeRatings returns a map of all node ratings known to the database.
	NodeRatings(ctx context.Context) (ratings.NodeRatingsMap, error)

	// GetAuctioneerBalance returns the balance of the auctioneer account
	// at the given point in time.
	GetAuctioneerBalance(ctx context.Context,
		date time.Time) (btcutil.Amount, error)

	// NodeRatingsDatabase is a logical ratings database. Before usage the
	// IndexRatings() MUST be called.
	ratings.NodeRatingsDatabase
}
