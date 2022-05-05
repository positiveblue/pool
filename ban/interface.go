package ban

import (
	"context"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
)

const (
	// defaultStoreTimeout used in store timeout contexts.
	defaultStoreTimeout = 5 * time.Second
)

// Manager interface for banning accounts.
//
// TODO(positiveblue): make all the operations atomic at db level.
type Manager interface {
	// CalculateNewInfo returns the information for a new ban taking into
	// account current ones.
	CalculateNewInfo(currentHeight uint32, currentInfo *Info) *Info

	// BanAccount attempts to ban the account associated with a trader
	// starting from the current height of the chain.
	BanAccount(accKey *btcec.PublicKey, currentHeight uint32) (uint32,
		error)

	// GetAccountBan returns the ban Info for the given accountKey.
	// Info will be nil if the account is not currently banned.
	GetAccountBan(accKey *btcec.PublicKey) (*Info, error)

	// BanNode attempts to ban the node associated with a trader starting
	// from the current height of the chain.
	BanNode(nodeKey *btcec.PublicKey, currentHeight uint32) (uint32, error)

	// GetNodeBan returns the ban Info for the given nodeKey.
	// Info will be nil if the node is not currently banned.
	GetNodeBan(nodeKey *btcec.PublicKey) (*Info, error)

	// IsAccountBanned determines whether the trader's account is banned at
	// the current height or not.
	IsAccountBanned(accKey *btcec.PublicKey, currentHeight uint32) (bool,
		uint32, error)

	// IsNodeBanned determines whether the trader's node is banned at the
	// current height or not.
	IsNodeBanned(nodeKey *btcec.PublicKey, currentHeight uint32) (bool,
		uint32, error)

	// IsTraderBanned determines whether the trader's account or node is
	// banned at the current height.
	IsTraderBanned(acctBytes, nodeBytes [33]byte,
		currentHeight uint32) (bool, error)

	// ListBannedAccounts returns a map of all accounts that are currently
	// banned. The map key is the account's trader key and the value is the
	// ban info.
	ListBannedAccounts() (map[[33]byte]*Info, error)

	// ListBannedNodes returns a map of all nodes that are currently banned.
	// The map key is the node's identity pubkey and the value is the ban
	// info.
	ListBannedNodes() (map[[33]byte]*Info, error)

	// RemoveAccountBan removes the ban information for a given trader's
	// account key. Returns an error if no ban exists.
	RemoveAccountBan(acctKey *btcec.PublicKey) error

	// RemoveNodeBan removes the ban information for a given trader's node
	// identity key. Returns an error if no ban exists.
	RemoveNodeBan(nodeKey *btcec.PublicKey) error
}

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
