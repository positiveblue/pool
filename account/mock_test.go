package account

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/agora/client/clmscript"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/keychain"
)

func init() {
	copy(testTraderKey[:], testRawTraderKey)
}

var (
	testRawTraderKey, _ = hex.DecodeString("03c3416ff848ab7bcc54b8c63a7fa251c7dcb908d41ad2c7e8ebbf549715552ec5")
	testTraderKey       [33]byte

	testRawAuctioneerKey, _ = hex.DecodeString("036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0af58e0c9395446ba09")
	testAuctioneerKey, _    = btcec.ParsePubKey(testRawAuctioneerKey, btcec.S256())
	testAuctioneerKeyDesc   = &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: clmscript.AccountKeyFamily,
			Index:  0,
		},
		PubKey: testAuctioneerKey,
	}
)

type mockStore struct {
	Store

	mu           sync.Mutex
	reservations map[lsat.TokenID][33]byte
	accounts     map[[33]byte]Account
}

func newMockStore() *mockStore {
	return &mockStore{
		reservations: make(map[lsat.TokenID][33]byte),
		accounts:     make(map[[33]byte]Account),
	}
}

func (s *mockStore) HasReservation(_ context.Context,
	tokenID lsat.TokenID) ([33]byte, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	key, ok := s.reservations[tokenID]
	if !ok {
		return [33]byte{}, ErrNoReservation
	}
	return key, nil
}

func (s *mockStore) ReserveAccount(_ context.Context,
	tokenID lsat.TokenID, auctioneerKey [33]byte) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	s.reservations[tokenID] = auctioneerKey
	return nil
}

func (s *mockStore) CompleteReservation(_ context.Context,
	account *Account) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.reservations, account.TokenID)
	s.accounts[account.TraderKey] = *account
	return nil
}

func (s *mockStore) UpdateAccount(_ context.Context, account *Account,
	modifiers ...Modifier) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.accounts[account.TraderKey]; !ok {
		return errors.New("account not found")
	}

	for _, modifier := range modifiers {
		modifier(account)
	}

	s.accounts[account.TraderKey] = *account
	return nil
}

func (s *mockStore) Account(_ context.Context, traderKey [33]byte) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	account, ok := s.accounts[traderKey]
	if !ok {
		return nil, errors.New("account not found")
	}
	return &account, nil
}

func (s *mockStore) Accounts(context.Context) ([]*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	accounts := make([]*Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		account := account
		accounts = append(accounts, &account)
	}
	return accounts, nil
}

type mockWallet struct {
	lndclient.WalletKitClient
}

func (w *mockWallet) DeriveKey(ctx context.Context,
	keyLocator *keychain.KeyLocator) (*keychain.KeyDescriptor, error) {

	return testAuctioneerKeyDesc, nil
}

type mockChainNotifier struct {
	lndclient.ChainNotifierClient

	confChan  chan *chainntnfs.TxConfirmation
	spendChan chan *chainntnfs.SpendDetail
	blockChan chan int32
	errChan   chan error
}

func newMockChainNotifier() *mockChainNotifier {
	return &mockChainNotifier{
		confChan:  make(chan *chainntnfs.TxConfirmation),
		spendChan: make(chan *chainntnfs.SpendDetail),
		blockChan: make(chan int32),
		errChan:   make(chan error),
	}
}

func (n *mockChainNotifier) RegisterConfirmationsNtfn(ctx context.Context,
	txid *chainhash.Hash, pkScript []byte, numConfs,
	heightHint int32) (chan *chainntnfs.TxConfirmation, chan error, error) {

	return n.confChan, n.errChan, nil
}

func (n *mockChainNotifier) RegisterSpendNtfn(ctx context.Context,
	outpoint *wire.OutPoint, pkScript []byte,
	heightHint int32) (chan *chainntnfs.SpendDetail, chan error, error) {

	return n.spendChan, n.errChan, nil
}

func (n *mockChainNotifier) RegisterBlockEpochNtfn(
	ctx context.Context) (chan int32, chan error, error) {

	return n.blockChan, n.errChan, nil
}
