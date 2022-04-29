package subastadb

import (
	"context"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/aperture/lsat"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/ratings"
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

	// ListBannedAccounts returns a map of all accounts that are currently banned.
	// The map key is the account's trader key and the value is the ban info.
	ListBannedAccounts(
		ctx context.Context) (map[[33]byte]*BanInfo, error)

	// ListBannedNodes returns a map of all nodes that are currently banned.
	// The map key is the node's identity pubkey and the value is the ban info.
	ListBannedNodes(
		ctx context.Context) (map[[33]byte]*BanInfo, error)

	// RemoveAccountBan removes the ban information for a given trader's account
	// key. Returns an error if no ban exists.
	RemoveAccountBan(ctx context.Context,
		acctKey *btcec.PublicKey) error

	// RemoveNodeBan removes the ban information for a given trader's node identity
	// key. Returns an error if no ban exists.
	RemoveNodeBan(ctx context.Context,
		nodeKey *btcec.PublicKey) error

	// SetAccountBanInfo stores or overwrites the ban info for a trader account.
	SetAccountBanInfo(ctx context.Context, accountKey *btcec.PublicKey,
		currentHeight, duration uint32) error

	// SetNodeBanInfo stores or overwrites the ban info for a node.
	SetNodeBanInfo(ctx context.Context,
		nodeKey *btcec.PublicKey, currentHeight, duration uint32) error

	// RemoveReservation deletes a reservation identified by the LSAT ID.
	RemoveReservation(ctx context.Context, id lsat.TokenID) error

	// NodeRatings returns a map of all node ratings known to the database.
	NodeRatings(ctx context.Context) (ratings.NodeRatingsMap, error)

	// Batches retrieves all existing batches.
	Batches(ctx context.Context) (map[orderT.BatchID]*BatchSnapshot, error)

	// MirrorToSQL attempts to mirror accounts, orders and batches to the configured
	// SQL database.
	//
	// TODO(positiveblue): Delete when sql migration is completed.
	MirrorToSQL(ctx context.Context) error
}
