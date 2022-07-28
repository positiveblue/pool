package account

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

var (
	testRawTraderKey, _ = hex.DecodeString("03c3416ff848ab7bcc54b8c63a7fa251c7dcb908d41ad2c7e8ebbf549715552ec5")
	testTraderKey, _    = btcec.ParsePubKey(testRawTraderKey)

	testRawAuctioneerKey, _ = hex.DecodeString("036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0af58e0c9395446ba09")
	testAuctioneerKey, _    = btcec.ParsePubKey(testRawAuctioneerKey)

	testAuctioneerKeyDesc = &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: poolscript.AccountKeyFamily,
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
	accountDiffs    map[[33]byte]Account
}

func newMockStore() *mockStore {
	return &mockStore{
		reservations:    make(map[lsat.TokenID]Reservation),
		newReservations: make(chan *Reservation, 1),
		accounts:        make(map[[33]byte]Account),
		accountDiffs:    make(map[[33]byte]Account),
	}
}

func (s *mockStore) FetchAuctioneerAccount(_ context.Context) (*Auctioneer, error) {
	return &Auctioneer{
		OutPoint:      wire.OutPoint{Index: 10},
		Balance:       btcutil.SatoshiPerBitcoin,
		AuctioneerKey: testAuctioneerKeyDesc,
		BatchKey:      [33]byte{10, 20, 30},
	}, nil
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

	if _, ok := s.accounts[accountKey]; ok {
		return ErrAccountExists
	}

	delete(s.reservations, account.TokenID)
	s.accounts[accountKey] = *account
	return nil
}

func (s *mockStore) UpdateAccount(_ context.Context, account *Account,
	modifiers ...Modifier) (*Account, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], account.TraderKeyRaw[:])

	if _, ok := s.accounts[accountKey]; !ok {
		return nil, errors.New("account not found")
	}

	for _, modifier := range modifiers {
		modifier(account)
	}

	s.accounts[accountKey] = *account
	return account, nil
}

func (s *mockStore) StoreAccountDiff(_ context.Context,
	traderKey *btcec.PublicKey, modifiers []Modifier) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], traderKey.SerializeCompressed())

	account, ok := s.accounts[accountKey]
	if !ok {
		return errors.New("account not found")
	}

	for _, modifier := range modifiers {
		modifier(&account)
	}

	s.accountDiffs[accountKey] = account
	return nil
}

func (s *mockStore) CommitAccountDiff(_ context.Context,
	traderKey *btcec.PublicKey) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], traderKey.SerializeCompressed())

	accountDiff, ok := s.accountDiffs[accountKey]
	if !ok {
		return ErrNoDiff
	}

	if _, ok := s.accounts[accountKey]; !ok {
		return errors.New("account not found")
	}

	s.accounts[accountKey] = accountDiff
	delete(s.accountDiffs, accountKey)

	return nil
}

func (s *mockStore) Account(_ context.Context, traderKey *btcec.PublicKey,
	includeDiff bool) (*Account, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	var accountKey [33]byte
	copy(accountKey[:], traderKey.SerializeCompressed())

	if includeDiff {
		if accountDiff, ok := s.accountDiffs[accountKey]; ok {
			return &accountDiff, nil
		}
	}

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

	signer *MockSigner
}

func newMockWallet(privKey *btcec.PrivateKey) *mockWallet {
	return &mockWallet{
		signer: &MockSigner{
			[]*btcec.PrivateKey{privKey},
		},
	}
}

func (w *mockWallet) DeriveKey(ctx context.Context,
	keyLocator *keychain.KeyLocator) (*keychain.KeyDescriptor, error) {

	return &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: poolscript.AccountKeyFamily,
			Index:  0,
		},
		PubKey: w.signer.PrivKeys[0].PubKey(),
	}, nil
}

func (w *mockWallet) DeriveSharedKey(context.Context, *btcec.PublicKey,
	*keychain.KeyLocator) ([32]byte, error) {

	return sharedSecret, nil
}

func (w *mockWallet) SignOutputRaw(ctx context.Context, tx *wire.MsgTx,
	signDescs []*lndclient.SignDescriptor,
	prevOutputs []*wire.TxOut) ([][]byte, error) {

	return w.signer.SignOutputRaw(ctx, tx, signDescs, nil)
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
	PrivKeys []*btcec.PrivateKey
}

func (m *MockSigner) SignOutputRaw(ctx context.Context, tx *wire.MsgTx,
	signDescriptors []*lndclient.SignDescriptor,
	prevOutputs []*wire.TxOut) ([][]byte, error) {

	s := input.MockSigner{
		Privkeys: m.PrivKeys,
	}

	// The mock signer relies on the public key being set in the sign
	// descriptor, so we'll do so now.
	signDescriptors[0].KeyDesc.PubKey = m.PrivKeys[0].PubKey()

	lndSignDescriptor := &input.SignDescriptor{
		KeyDesc:       signDescriptors[0].KeyDesc,
		SingleTweak:   signDescriptors[0].SingleTweak,
		DoubleTweak:   signDescriptors[0].DoubleTweak,
		WitnessScript: signDescriptors[0].WitnessScript,
		Output:        signDescriptors[0].Output,
		HashType:      signDescriptors[0].HashType,
		SigHashes:     input.NewTxSigHashesV0Only(tx),
		InputIndex:    signDescriptors[0].InputIndex,
		SignMethod:    signDescriptors[0].SignMethod,
	}

	if lndSignDescriptor.SignMethod == input.TaprootKeySpendBIP0086SignMethod {
		prevOutputFetcher := txscript.NewMultiPrevOutFetcher(nil)
		for idx := range tx.TxIn {
			prevOutputFetcher.AddPrevOut(
				tx.TxIn[idx].PreviousOutPoint, prevOutputs[idx],
			)
		}
		sigHashes := txscript.NewTxSigHashes(tx, prevOutputFetcher)

		privKey := m.PrivKeys[0]
		if len(lndSignDescriptor.SingleTweak) > 0 {
			privKey = input.TweakPrivKey(
				privKey, lndSignDescriptor.SingleTweak,
			)
		}

		witnessScript, err := txscript.TaprootWitnessSignature(
			tx, sigHashes, lndSignDescriptor.InputIndex,
			lndSignDescriptor.Output.Value,
			lndSignDescriptor.Output.PkScript,
			txscript.SigHashDefault, privKey,
		)
		if err != nil {
			return nil, err
		}

		return witnessScript, nil
	}

	sig, err := s.SignOutputRaw(tx, lndSignDescriptor)
	if err != nil {
		return nil, err
	}

	return [][]byte{sig.Serialize()}, nil
}

func (m *MockSigner) ComputeInputScript(ctx context.Context, tx *wire.MsgTx,
	signDescriptors []*lndclient.SignDescriptor) ([]*input.Script, error) {

	s := input.MockSigner{
		Privkeys:  m.PrivKeys,
		NetParams: &chaincfg.RegressionNetParams,
	}

	scripts := make([]*input.Script, len(signDescriptors))
	for i, desc := range signDescriptors {
		lndSignDescriptor := &input.SignDescriptor{
			Output:     desc.Output,
			HashType:   desc.HashType,
			SigHashes:  input.NewTxSigHashesV0Only(tx),
			InputIndex: desc.InputIndex,
		}

		script, err := s.ComputeInputScript(tx, lndSignDescriptor)
		if err != nil {
			return nil, err
		}
		scripts[i] = script
	}

	return scripts, nil
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

// MuSig2CreateSession creates a new MuSig2 signing session using the local
// key identified by the key locator. The complete list of all public keys of
// all signing parties must be provided, including the public key of the local
// signing key. If nonces of other parties are already known, they can be
// submitted as well to reduce the number of method calls necessary later on.
func (m *MockSigner) MuSig2CreateSession(context.Context, *keychain.KeyLocator,
	[][32]byte, ...lndclient.MuSig2SessionOpts) (*input.MuSig2SessionInfo,
	error) {

	return nil, nil
}

// MuSig2RegisterNonces registers one or more public nonces of other signing
// participants for a session identified by its ID. This method returns true
// once we have all nonces for all other signing participants.
func (m *MockSigner) MuSig2RegisterNonces(context.Context, [32]byte,
	[][66]byte) (bool, error) {

	return false, nil
}

// MuSig2Sign creates a partial signature using the local signing key
// that was specified when the session was created. This can only be
// called when all public nonces of all participants are known and have
// been registered with the session. If this node isn't responsible for
// combining all the partial signatures, then the cleanup parameter
// should be set, indicating that the session can be removed from memory
// once the signature was produced.
func (m *MockSigner) MuSig2Sign(context.Context, [32]byte, [32]byte,
	bool) ([]byte, error) {

	return nil, nil
}

// MuSig2CombineSig combines the given partial signature(s) with the
// local one, if it already exists. Once a partial signature of all
// participants is registered, the final signature will be combined and
// returned.
func (m *MockSigner) MuSig2CombineSig(context.Context, [32]byte,
	[][]byte) (bool, []byte, error) {

	return false, nil, nil
}

// MuSig2Cleanup removes a session from memory to free up resources.
func (m *MockSigner) MuSig2Cleanup(context.Context, [32]byte) error {
	return nil
}
