package subastadb

import (
	"context"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/subasta/ban"
)

// BanAccount creates/updates the ban Info for the given accountKey.
func (s *SQLStore) BanAccount(ctx context.Context, accountKey *btcec.PublicKey,
	info *ban.Info) error {

	return ErrNotImplemented
}

// GetAccountBan returns the ban Info for the given accountKey.
// Info will be nil if the account is not currently banned.
func (s *SQLStore) GetAccountBan(ctx context.Context,
	accKey *btcec.PublicKey, currentHeight uint32) (*ban.Info, error) {

	return nil, ErrNotImplemented
}

// BanNode creates/updates the ban Info for the given nodeKey.
func (s *SQLStore) BanNode(ctx context.Context, nodeKey *btcec.PublicKey,
	info *ban.Info) error {

	return ErrNotImplemented
}

// GetNodeBan returns the ban Info for the given nodeKey.
// Info will be nil if the node is not currently banned.
func (s *SQLStore) GetNodeBan(ctx context.Context,
	nodeKey *btcec.PublicKey, currentHeight uint32) (*ban.Info, error) {

	return nil, ErrNotImplemented
}

// ListBannedAccounts returns a map of all accounts that are currently banned.
// The map key is the account's trader key and the value is the ban info.
func (s *SQLStore) ListBannedAccounts(ctx context.Context,
	currentHeight uint32) (map[[33]byte]*ban.Info, error) {

	return nil, ErrNotImplemented
}

// ListBannedNodes returns a map of all nodes that are currently banned.
// The map key is the node's identity pubkey and the value is the ban info.
func (s *SQLStore) ListBannedNodes(ctx context.Context,
	currentHeight uint32) (map[[33]byte]*ban.Info, error) {

	return nil, ErrNotImplemented
}

// RemoveAccountBan removes the ban information for a given trader's account
// key. Returns an error if no ban exists.
func (s *SQLStore) RemoveAccountBan(ctx context.Context,
	acctKey *btcec.PublicKey) error {

	return ErrNotImplemented
}

// RemoveNodeBan removes the ban information for a given trader's node identity
// key. Returns an error if no ban exists.
func (s *SQLStore) RemoveNodeBan(ctx context.Context,
	nodeKey *btcec.PublicKey) error {

	return ErrNotImplemented
}
