package ban

import (
	"context"
	"sync"

	"github.com/btcsuite/btcd/btcec/v2"
)

var _ Store = (*mockStore)(nil)

type mockStore struct {
	mu             sync.Mutex
	bannedAccounts map[[33]byte]*Info
	bannedNodes    map[[33]byte]*Info
}

func NewStoreMock() *mockStore { // nolint:golint
	return &mockStore{
		bannedAccounts: make(map[[33]byte]*Info),
		bannedNodes:    make(map[[33]byte]*Info),
	}
}

func (s *mockStore) BanAccount(ctx context.Context, accKey *btcec.PublicKey,
	banInfo *Info) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], accKey.SerializeCompressed())

	s.bannedAccounts[accountKey] = banInfo

	return nil
}

func (s *mockStore) GetAccountBan(_ context.Context,
	accKey *btcec.PublicKey) (*Info, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], accKey.SerializeCompressed())

	return s.bannedAccounts[accountKey], nil
}

func (s *mockStore) BanNode(_ context.Context, nodKey *btcec.PublicKey,
	banInfo *Info) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var nodeKey [33]byte
	copy(nodeKey[:], nodKey.SerializeCompressed())

	s.bannedNodes[nodeKey] = banInfo

	return nil
}

func (s *mockStore) GetNodeBan(_ context.Context,
	nodKey *btcec.PublicKey) (*Info, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	var nodeKey [33]byte
	copy(nodeKey[:], nodKey.SerializeCompressed())

	return s.bannedNodes[nodeKey], nil
}

func (s *mockStore) ListBannedAccounts(
	_ context.Context) (map[[33]byte]*Info, error) {

	list := make(map[[33]byte]*Info, len(s.bannedAccounts))
	for key, banInfo := range s.bannedAccounts {
		list[key] = banInfo
	}
	return list, nil
}

func (s *mockStore) ListBannedNodes(
	_ context.Context) (map[[33]byte]*Info, error) {

	list := make(map[[33]byte]*Info, len(s.bannedNodes))
	for key, banInfo := range s.bannedNodes {
		list[key] = banInfo
	}
	return list, nil
}

func (s *mockStore) RemoveAccountBan(_ context.Context,
	accKey *btcec.PublicKey) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], accKey.SerializeCompressed())

	delete(s.bannedAccounts, accountKey)
	return nil
}

func (s *mockStore) RemoveNodeBan(_ context.Context,
	nodKey *btcec.PublicKey) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var nodeKey [33]byte
	copy(nodeKey[:], nodKey.SerializeCompressed())

	delete(s.bannedNodes, nodeKey)
	return nil
}
