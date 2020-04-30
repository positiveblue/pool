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
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

var (
	testRawTraderKey, _ = hex.DecodeString("03c3416ff848ab7bcc54b8c63a7fa251c7dcb908d41ad2c7e8ebbf549715552ec5")
	testTraderKey, _    = btcec.ParsePubKey(testRawTraderKey, btcec.S256())

	testRawAuctioneerKey, _ = hex.DecodeString("036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0af58e0c9395446ba09")
	testAuctioneerKey, _    = btcec.ParsePubKey(testRawAuctioneerKey, btcec.S256())

	testAuctioneerKeyDesc = &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: clmscript.AccountKeyFamily,
			Index:  0,
		},
		PubKey: testAuctioneerKey,
	}

	sharedSecret = [32]byte{0x73, 0x65, 0x63, 0x72, 0x65, 0x74}
)

type mockStore struct {
	Store

	mu              sync.Mutex
	reservations    map[lsat.TokenID]Reservation
	newReservations chan *Reservation
	accounts        map[[33]byte]Account
}

func newMockStore() *mockStore {
	return &mockStore{
		reservations:    make(map[lsat.TokenID]Reservation),
		newReservations: make(chan *Reservation, 1),
		accounts:        make(map[[33]byte]Account),
	}
}

func (s *mockStore) HasReservation(_ context.Context,
	tokenID lsat.TokenID) (*Reservation, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	reservation, ok := s.reservations[tokenID]
	if !ok {
		return nil, ErrNoReservation
	}
	return &reservation, nil
}

func (s *mockStore) ReserveAccount(_ context.Context, tokenID lsat.TokenID,
	reservation *Reservation) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	s.reservations[tokenID] = *reservation
	s.newReservations <- reservation
	return nil
}

func (s *mockStore) CompleteReservation(_ context.Context,
	account *Account) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], account.TraderKeyRaw[:])

	delete(s.reservations, account.TokenID)
	s.accounts[accountKey] = *account
	return nil
}

func (s *mockStore) UpdateAccount(_ context.Context, account *Account,
	modifiers ...Modifier) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], account.TraderKeyRaw[:])

	if _, ok := s.accounts[accountKey]; !ok {
		return errors.New("account not found")
	}

	for _, modifier := range modifiers {
		modifier(account)
	}

	s.accounts[accountKey] = *account
	return nil
}

func (s *mockStore) Account(_ context.Context,
	traderKey *btcec.PublicKey) (*Account, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], traderKey.SerializeCompressed())

	account, ok := s.accounts[accountKey]
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

func (s *mockStore) BatchKey(context.Context) (*btcec.PublicKey, error) {
	return testTraderKey, nil
}

type mockWallet struct {
	lndclient.WalletKitClient
	lndclient.SignerClient
}

func (w *mockWallet) DeriveKey(ctx context.Context,
	keyLocator *keychain.KeyLocator) (*keychain.KeyDescriptor, error) {

	return testAuctioneerKeyDesc, nil
}

func (w *mockWallet) DeriveSharedKey(context.Context, *btcec.PublicKey,
	*keychain.KeyLocator) ([32]byte, error) {

	return sharedSecret, nil
}

func (w *mockWallet) SignOutputRaw(_ context.Context, tx *wire.MsgTx,
	signDescs []*input.SignDescriptor) ([][]byte, error) {

	return [][]byte{[]byte("auctioneer sig")}, nil
}

type MockChainNotifier struct {
	ConfChan  chan *chainntnfs.TxConfirmation
	SpendChan chan *chainntnfs.SpendDetail
	BlockChan chan int32
	ErrChan   chan error
}

func NewMockChainNotifier() *MockChainNotifier {
	return &MockChainNotifier{
		ConfChan:  make(chan *chainntnfs.TxConfirmation),
		SpendChan: make(chan *chainntnfs.SpendDetail),
		BlockChan: make(chan int32),
		ErrChan:   make(chan error),
	}
}

func (n *MockChainNotifier) RegisterConfirmationsNtfn(ctx context.Context,
	txid *chainhash.Hash, pkScript []byte, numConfs,
	heightHint int32) (chan *chainntnfs.TxConfirmation, chan error, error) {

	return n.ConfChan, n.ErrChan, nil
}

func (n *MockChainNotifier) RegisterSpendNtfn(ctx context.Context,
	outpoint *wire.OutPoint, pkScript []byte,
	heightHint int32) (chan *chainntnfs.SpendDetail, chan error, error) {

	return n.SpendChan, n.ErrChan, nil
}

func (n *MockChainNotifier) RegisterBlockEpochNtfn(
	ctx context.Context) (chan int32, chan error, error) {

	// Mimic the actual ChainNotifier by sending a notification upon
	// registration.
	go func() {
		n.BlockChan <- 0
	}()

	return n.BlockChan, n.ErrChan, nil
}

type MockSigner struct {
	PrivKey *btcec.PrivateKey
}

func (m *MockSigner) SignOutputRaw(ctx context.Context, tx *wire.MsgTx,
	signDescriptors []*input.SignDescriptor) ([][]byte, error) {

	s := input.MockSigner{
		Privkeys: []*btcec.PrivateKey{
			m.PrivKey,
		},
	}

	sig, err := s.SignOutputRaw(tx, signDescriptors[0])
	if err != nil {
		return nil, err
	}

	return [][]byte{sig}, nil
}

func (m *MockSigner) ComputeInputScript(ctx context.Context, tx *wire.MsgTx,
	signDescriptors []*input.SignDescriptor) ([]*input.Script, error) {
	return nil, nil
}
func (m *MockSigner) SignMessage(ctx context.Context, msg []byte,
	locator keychain.KeyLocator) ([]byte, error) {
	return nil, nil
}
func (m *MockSigner) VerifyMessage(ctx context.Context, msg, sig []byte, pubkey [33]byte) (
	bool, error) {
	return false, nil
}
func (m *MockSigner) DeriveSharedKey(ctx context.Context, ephemeralPubKey *btcec.PublicKey,
	keyLocator *keychain.KeyLocator) ([32]byte, error) {
	return [32]byte{}, nil
}
