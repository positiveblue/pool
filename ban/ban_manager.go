package ban

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
)

const (
	// initialBanDuration is the initial ban duration in blocks of a trader.
	// Any consecutive bans after the initial will have a duration double
	// the previous. The current duration is equivalent to 1 day worth of
	// blocks on average.
	initialBanDuration uint32 = 144
)

// ManagerConfig contains all of the required dependencies for the Manager to
// carry out its duties.
type ManagerConfig struct {
	Store Store
}

// manager is responsible of managing account bans.
type manager struct {
	cfg *ManagerConfig
}

// Compile time assertion that manager implements the Manager interface.
var _ Manager = (*manager)(nil)

// NewManager instantiates a new Manager backed by the given config.
func NewManager(cfg *ManagerConfig) *manager { // nolint:golint
	m := &manager{
		cfg: cfg,
	}
	return m
}

// CalculateNewInfo returns the information for a new ban taking into
// account current ones.
func (m *manager) CalculateNewInfo(currentHeight uint32,
	currentInfo *Info) *Info {

	info := &Info{
		Height:   currentHeight,
		Duration: initialBanDuration,
	}

	if currentInfo != nil {
		info.Duration = currentInfo.Duration * 2
	}

	return info
}

// BanAccount attempts to ban the account associated with a trader starting from
// the current height of the chain. The duration of the ban will depend on how
// many times the node has been banned before and grows exponentially, otherwise
// it is 144 blocks.
func (m *manager) BanAccount(accKey *btcec.PublicKey,
	currentHeight uint32) (uint32, error) {

	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultStoreTimeout)
	defer cancel()

	currentInfo, err := m.cfg.Store.GetAccountBan(ctxt, accKey)
	if err != nil {
		return 0, err
	}

	info := m.CalculateNewInfo(currentHeight, currentInfo)
	err = m.cfg.Store.BanAccount(ctxt, accKey, info)
	if err != nil {
		return 0, err
	}

	return info.Expiration(), nil
}

// GetAccountBan returns the ban Info for the given accountKey.
// Info will be nil if the account is not currently banned.
func (m *manager) GetAccountBan(accKey *btcec.PublicKey) (*Info, error) {
	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultStoreTimeout)
	defer cancel()

	return m.cfg.Store.GetAccountBan(ctxt, accKey)
}

// BanNode attempts to ban the node associated with a trader starting from
// the current height of the chain. The duration of the ban will depend on how
// many times the node has been banned before and grows exponentially, otherwise
// it is 144 blocks.
func (m *manager) BanNode(nodeKey *btcec.PublicKey,
	currentHeight uint32) (uint32, error) {

	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultStoreTimeout)
	defer cancel()

	currentInfo, err := m.cfg.Store.GetNodeBan(ctxt, nodeKey)
	if err != nil {
		return 0, err
	}

	info := m.CalculateNewInfo(currentHeight, currentInfo)
	err = m.cfg.Store.BanNode(ctxt, nodeKey, info)
	if err != nil {
		return 0, err
	}

	return info.Expiration(), nil
}

// GetNodeBan returns the ban Info for the given nodeKey.
// Info will be nil if the node is not currently banned.
func (m *manager) GetNodeBan(nodeKey *btcec.PublicKey) (*Info, error) {
	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultStoreTimeout)
	defer cancel()

	return m.cfg.Store.GetNodeBan(ctxt, nodeKey)
}

// IsAccountBanned determines whether the trader's account is banned at the
// current height or not.
func (m *manager) IsAccountBanned(accKey *btcec.PublicKey,
	currentHeight uint32) (bool, uint32, error) {

	info, err := m.GetAccountBan(accKey)
	if err != nil {
		return false, 0, err
	}

	if info != nil && !info.ExceedsBanExpiration(currentHeight) {
		return true, info.Expiration(), nil
	}

	return false, 0, nil
}

// IsNodeBanned determines whether the trader's node is banned at the
// current height or not.
func (m *manager) IsNodeBanned(nodeKey *btcec.PublicKey,
	currentHeight uint32) (bool, uint32, error) {

	info, err := m.GetNodeBan(nodeKey)
	if err != nil {
		return false, 0, err
	}

	if info != nil && !info.ExceedsBanExpiration(currentHeight) {
		return true, info.Expiration(), nil
	}

	return false, 0, nil
}

// IsTraderBanned determines whether the trader's account or node is
// banned at the current height.
func (m *manager) IsTraderBanned(acctBytes, nodeBytes [33]byte,
	currentHeight uint32) (bool, error) {

	acctKey, err := btcec.ParsePubKey(acctBytes[:])
	if err != nil {
		return false, err
	}
	banned, _, err := m.IsAccountBanned(acctKey, currentHeight)
	if banned || err != nil {
		return banned, err
	}

	nodeKey, err := btcec.ParsePubKey(nodeBytes[:])
	if err != nil {
		return false, err
	}
	banned, _, err = m.IsNodeBanned(nodeKey, currentHeight)
	return banned, err
}

// ListBannedAccounts returns a map of all accounts that are currently banned.
// The map key is the account's trader key and the value is the ban info.
func (m *manager) ListBannedAccounts() (map[[33]byte]*Info, error) {
	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultStoreTimeout)
	defer cancel()

	return m.cfg.Store.ListBannedAccounts(ctxt)
}

// ListBannedNodes returns a map of all nodes that are currently banned.
// The map key is the node's identity pubkey and the value is the ban info.
func (m *manager) ListBannedNodes() (map[[33]byte]*Info, error) {
	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultStoreTimeout)
	defer cancel()

	return m.cfg.Store.ListBannedNodes(ctxt)
}

// RemoveAccountBan removes the ban information for a given trader's account
// key. Returns an error if no ban exists.
func (m *manager) RemoveAccountBan(acctKey *btcec.PublicKey) error {
	info, err := m.GetAccountBan(acctKey)
	if err != nil {
		return nil
	}
	if info == nil {
		return fmt.Errorf("unable to remove account ban, %v is not "+
			"banned", acctKey)
	}

	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultStoreTimeout)
	defer cancel()

	return m.cfg.Store.RemoveAccountBan(ctxt, acctKey)
}

// RemoveNodeBan removes the ban information for a given trader's node identity
// key. Returns an error if no ban exists.
func (m *manager) RemoveNodeBan(nodeKey *btcec.PublicKey) error {
	info, err := m.GetNodeBan(nodeKey)
	if err != nil {
		return nil
	}
	if info == nil {
		return fmt.Errorf("unable to remove node ban, %v is not "+
			"banned", nodeKey)
	}

	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultStoreTimeout)
	defer cancel()

	return m.cfg.Store.RemoveNodeBan(ctxt, nodeKey)
}
