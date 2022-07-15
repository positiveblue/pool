package subastadb

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/jackc/pgx/v4"
	"github.com/lightninglabs/subasta/ban"
	"github.com/lightninglabs/subasta/subastadb/postgres"
)

// BanAccount creates/updates the ban Info for the given accountKey.
func (s *SQLStore) BanAccount(ctx context.Context, accountKey *btcec.PublicKey,
	info *ban.Info) error {

	txBody := func(txQueries *postgres.Queries) error {
		return banAccountWithTx(ctx, txQueries, accountKey, info)
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		traderKey := accountKey.SerializeCompressed()
		return fmt.Errorf("unable to ban account(%x): %v", traderKey,
			err)
	}
	return nil
}

// GetAccountBan returns the ban Info for the given accountKey.
// Info will be nil if the account is not currently banned.
func (s *SQLStore) GetAccountBan(ctx context.Context,
	accKey *btcec.PublicKey, currentHeight uint32) (*ban.Info, error) {

	traderKey := accKey.SerializeCompressed()
	params := postgres.GetAccountBanParams{
		TraderKey:    traderKey,
		ExpiryHeight: int64(currentHeight),
	}
	row, err := s.queries.GetAccountBan(ctx, params)
	switch {
	case err == pgx.ErrNoRows:
		return nil, nil

	case err != nil:
		return nil, fmt.Errorf("unable to get account(%x) ban: %v",
			traderKey, err)
	}

	return &ban.Info{
		Height:   uint32(row.ExpiryHeight) - uint32(row.Duration),
		Duration: uint32(row.Duration),
	}, nil
}

// BanNode creates/updates the ban Info for the given nodeKey.
func (s *SQLStore) BanNode(ctx context.Context, nKey *btcec.PublicKey,
	info *ban.Info) error {

	txBody := func(txQueries *postgres.Queries) error {
		return banNodeWithTx(ctx, txQueries, nKey, info)
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		nodeKey := nKey.SerializeCompressed()
		return fmt.Errorf("unable to ban node(%x): %v", nodeKey, err)
	}
	return nil
}

// GetNodeBan returns the ban Info for the given nodeKey.
// Info will be nil if the node is not currently banned.
func (s *SQLStore) GetNodeBan(ctx context.Context,
	nKey *btcec.PublicKey, currentHeight uint32) (*ban.Info, error) {

	nodeKey := nKey.SerializeCompressed()
	params := postgres.GetNodeBanParams{
		NodeKey:      nodeKey,
		ExpiryHeight: int64(currentHeight),
	}
	row, err := s.queries.GetNodeBan(ctx, params)
	switch {
	case err == pgx.ErrNoRows:
		return nil, nil

	case err != nil:
		return nil, fmt.Errorf("unable to get node(%x) ban: %v",
			nodeKey, err)
	}

	return &ban.Info{
		Height:   uint32(row.ExpiryHeight) - uint32(row.Duration),
		Duration: uint32(row.Duration),
	}, nil
}

// ListBannedAccounts returns a map of all accounts that are currently banned.
// The map key is the account's trader key and the value is the ban info.
func (s *SQLStore) ListBannedAccounts(ctx context.Context,
	currentHeight uint32) (map[[33]byte]*ban.Info, error) {

	rows, err := s.queries.GetAllActiveAccountBans(
		ctx, int64(currentHeight),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to get all account bans: %v",
			err)
	}

	bans := make(map[[33]byte]*ban.Info, len(rows))
	for _, row := range rows {
		var traderKey [33]byte
		copy(traderKey[:], row.TraderKey)

		expiry := uint32(row.ExpiryHeight)
		duration := uint32(row.Duration)
		bans[traderKey] = &ban.Info{
			Height:   expiry - duration,
			Duration: duration,
		}
	}

	return bans, nil
}

// ListBannedNodes returns a map of all nodes that are currently banned.
// The map key is the node's identity pubkey and the value is the ban info.
func (s *SQLStore) ListBannedNodes(ctx context.Context,
	currentHeight uint32) (map[[33]byte]*ban.Info, error) {

	rows, err := s.queries.GetAllActiveNodeBans(
		ctx, int64(currentHeight),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to get all node bans: %v", err)
	}

	bans := make(map[[33]byte]*ban.Info, len(rows))
	for _, row := range rows {
		var nodeKey [33]byte
		copy(nodeKey[:], row.NodeKey)

		expiry := uint32(row.ExpiryHeight)
		duration := uint32(row.Duration)
		bans[nodeKey] = &ban.Info{
			Height:   expiry - duration,
			Duration: duration,
		}
	}

	return bans, nil
}

// RemoveAccountBan removes the ban information for a given trader's account
// key. Returns an error if no ban exists.
func (s *SQLStore) RemoveAccountBan(ctx context.Context,
	acctKey *btcec.PublicKey) error {

	// Disable old bans.
	traderKey := acctKey.SerializeCompressed()
	_, err := s.queries.DisableAccountBan(ctx, traderKey)
	if err != nil {
		return fmt.Errorf("unable to disable account(%x) bans: %v",
			traderKey, err)
	}
	return nil
}

// RemoveNodeBan removes the ban information for a given trader's node identity
// key. Returns an error if no ban exists.
func (s *SQLStore) RemoveNodeBan(ctx context.Context,
	nKey *btcec.PublicKey) error {

	// Disable old bans.
	nodeKey := nKey.SerializeCompressed()
	_, err := s.queries.DisableNodeBan(ctx, nodeKey)
	if err != nil {
		return fmt.Errorf("unable to disable node(%x) bans: %v",
			nodeKey, err)
	}
	return nil
}

// banAccountWithTx inserts a new account ban using the provided queries
// struct.
func banAccountWithTx(ctx context.Context, txQueries *postgres.Queries,
	accountKey *btcec.PublicKey, info *ban.Info) error {

	traderKey := accountKey.SerializeCompressed()

	// Make sure that all the other bans are disabled.
	_, err := txQueries.DisableAccountBan(ctx, traderKey)
	if err != nil {
		return err
	}

	expiryHeight := int64(info.Height) + int64(info.Duration)
	params := postgres.CreateAccountBanParams{
		Disabled:     false,
		TraderKey:    traderKey,
		ExpiryHeight: expiryHeight,
		Duration:     int64(info.Duration),
	}
	return txQueries.CreateAccountBan(ctx, params)
}

// banAccountWithTx inserts a new node ban using the provided queries
// struct.
func banNodeWithTx(ctx context.Context, txQueries *postgres.Queries,
	nKey *btcec.PublicKey, info *ban.Info) error {

	nodeKey := nKey.SerializeCompressed()

	// Make sure that all the other bans are disabled.
	_, err := txQueries.DisableNodeBan(ctx, nodeKey)
	if err != nil {
		return err
	}

	expiryHeight := int64(info.Height) + int64(info.Duration)
	params := postgres.CreateNodeBanParams{
		Disabled:     false,
		NodeKey:      nodeKey,
		ExpiryHeight: expiryHeight,
		Duration:     int64(info.Duration),
	}
	return txQueries.CreateNodeBan(ctx, params)
}
