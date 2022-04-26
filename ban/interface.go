package ban

import (
	"context"

	"github.com/btcsuite/btcd/btcec/v2"
)

// Store is responsible for storing and retrieving ban information reliably.
type Store interface {
	// BanAccount creates/updates the ban Info for the given accountKey.
	BanAccount(ctx context.Context, accountKey *btcec.PublicKey,
		info *Info) error

	// GetAccountBan returns the ban Info for the given accountKey.
	// Info will be nil if the account is not currently banned.
	GetAccountBan(ctx context.Context, accKey *btcec.PublicKey) (*Info,
		error)

	// BanNode creates/updates the ban Info for the given nodeKey.
	BanNode(ctx context.Context, nodeKey *btcec.PublicKey,
		info *Info) error

	// GetNodeBan returns the ban Info for the given nodeKey.
	// Info will be nil if the node is not currently banned.
	GetNodeBan(ctx context.Context, nodeKey *btcec.PublicKey) (*Info,
		error)

	// ListBannedAccounts returns a map of all accounts that are currently banned.
	// The map key is the account's trader key and the value is the ban info.
	ListBannedAccounts(ctx context.Context) (map[[33]byte]*Info, error)

	// ListBannedNodes returns a map of all nodes that are currently banned.
	// The map key is the node's identity pubkey and the value is the ban info.
	ListBannedNodes(ctx context.Context) (map[[33]byte]*Info, error)

	// RemoveAccountBan removes the ban information for a given trader's account
	// key. Returns an error if no ban exists.
	RemoveAccountBan(ctx context.Context, acctKey *btcec.PublicKey) error

	// RemoveNodeBan removes the ban information for a given trader's node identity
	// key. Returns an error if no ban exists.
	RemoveNodeBan(ctx context.Context, nodeKey *btcec.PublicKey) error
}
