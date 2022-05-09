package subastadb

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightningnetwork/lnd/keychain"
	"gorm.io/gorm/clause"
)

// SQLKeyDescriptor is the SQL model for keychain.KeyDescriptor.
type SQLKeyDescriptor struct {
	// Family is the family of key being identified.
	Family uint32

	// Index is the precise index of the key being identified.
	Index uint32

	// PubKey is an optional public key that fully describes a target key.
	// If this is nil, the KeyLocator MUST NOT be empty.
	PubKey string
}

// newSQLKeyDescriptor constructs an SQLKeyDescriptor from the passed key
// descriptor.
func newSQLKeyDescriptor(keyDesc *keychain.KeyDescriptor) *SQLKeyDescriptor {
	sqlKeyDesc := &SQLKeyDescriptor{
		Family: uint32(keyDesc.Family),
		Index:  keyDesc.Index,
	}

	if keyDesc.PubKey != nil {
		sqlKeyDesc.PubKey = hex.EncodeToString(
			keyDesc.PubKey.SerializeCompressed(),
		)
	}

	return sqlKeyDesc
}

// KeyDescriptor converts this SQLKeyDescriptor to keychain.KeyDescriptor.
func (s *SQLKeyDescriptor) KeyDescriptor() (*keychain.KeyDescriptor, error) {
	pubkeyRaw, err := keyFromHexString(s.PubKey)
	if err != nil {
		return nil, err
	}

	pubKey, err := btcec.ParsePubKey(pubkeyRaw[:])
	if err != nil {
		return nil, err
	}

	return &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: keychain.KeyFamily(s.Family),
			Index:  s.Index,
		},
		PubKey: pubKey,
	}, nil
}

// SQLAccount is the SQL model for account.Account.
type SQLAccount struct {
	// TokenID is the token ID associated with the account.
	TokenID string

	// Value is the value of the account reflected in on-chain output that
	// backs the existence of an account.
	Value int64 // btcutil.Amount

	// Expiry is the expiration block height of an account. After this
	// point, the trader is able to withdraw the funds from their account
	// without cooperation of the auctioneer.
	Expiry uint32

	// TraderKey is the base trader's key in the 2-of-2 multi-sig
	// construction of a CLM account. This key will never be included in the
	// account script, but rather it will be tweaked with the per-batch key
	// and the account secret to prevent script reuse and provide plausible
	// deniability between account outputs to third parties.
	TraderKey string `gorm:"primaryKey"`

	// AuctioneerKey is the base auctioneer's key in the 2-of-2 multi-sig
	// construction of a CLM account. This key will never be included in the
	// account script, but rather it will be tweaked with the per-batch
	// trader key to prevent script reuse and provide plausible deniability
	// between account outputs to third parties.
	AuctioneerKey SQLKeyDescriptor `gorm:"embedded;embeddedPrefix:auctioneer_key_"`

	// BatchKey is the batch key that is used to tweak the trader key of an
	// account with, along with the secret. This will be incremented by the
	// curve's base point each time the account is modified or participates
	// in a cleared batch to prevent output script reuse for accounts
	// on-chain.
	BatchKey string

	// Secret is a static shared secret between the trader and the
	// auctioneer that is used to tweak the trader key of an account with,
	// along with the batch key. This ensures that only the trader and
	// auctioneer are able to successfully identify every past/future output
	// of an account.
	Secret string

	// State describes the current state of the account.
	State uint8

	// HeightHint is the earliest height in the chain at which we can find
	// the account output in a block.
	HeightHint uint32

	// OutPoint the outpoint of the current account output.
	OutPoint string

	// LatestTx is the latest transaction of an account.
	//
	// NOTE: This is nil within the StatePendingOpen phase as the auctioneer
	// does not know of the transaction beforehand. There are also no
	// guarantees to the transaction having its witness populated.
	LatestTx string

	// UserAgent is the string that identifies the software running on the
	// user's side that was used to initially initialize this account.
	UserAgent string
}

// TableName overrides the default table name.
func (SQLAccount) TableName() string {
	return "accounts"
}

// toAccount converts this SQLAccount to an account.Account.
func (s *SQLAccount) toAccount() (*account.Account, error) {
	tokenID, err := lsat.MakeIDFromString(s.TokenID)
	if err != nil {
		return nil, err
	}

	traderKeyRaw, err := keyFromHexString(s.TraderKey)
	if err != nil {
		return nil, err
	}

	auctioneerKey, err := s.AuctioneerKey.KeyDescriptor()
	if err != nil {
		return nil, err
	}

	batchKeyRaw, err := keyFromHexString(s.BatchKey)
	if err != nil {
		return nil, err
	}

	batchKey, err := btcec.ParsePubKey(batchKeyRaw[:])
	if err != nil {
		return nil, err
	}

	secretBytes, err := hex.DecodeString(s.Secret)
	if err != nil {
		return nil, err
	}
	var secret [32]byte
	copy(secret[:], secretBytes)

	outpointParts := strings.Split(s.OutPoint, ":")
	hash, err := chainhash.NewHashFromStr(outpointParts[0])
	if err != nil {
		return nil, err
	}
	outIdx, err := strconv.Atoi(outpointParts[1])
	if err != nil {
		return nil, err
	}

	var latestTx *wire.MsgTx
	if s.LatestTx != "" {
		latestTx, err = msgTxFromString(s.LatestTx)
		if err != nil {
			return nil, err
		}
	}

	acc := &account.Account{
		TokenID:       tokenID,
		Value:         btcutil.Amount(s.Value),
		Expiry:        s.Expiry,
		TraderKeyRaw:  traderKeyRaw,
		AuctioneerKey: auctioneerKey,
		BatchKey:      batchKey,
		Secret:        secret,
		State:         account.State(s.State),
		HeightHint:    s.HeightHint,
		OutPoint:      *wire.NewOutPoint(hash, uint32(outIdx)),
		LatestTx:      latestTx,
		UserAgent:     s.UserAgent,
	}

	// Update the trader key.
	_, err = acc.TraderKey()
	if err != nil {
		return nil, err
	}

	return acc, nil
}

// UpdateAccount attempts to insert or update an Account using the parent SQL
// transaction.
func (s *SQLTransaction) UpdateAccount(a *account.Account) error {
	sqlAccount := &SQLAccount{
		TokenID:    a.TokenID.String(),
		Value:      int64(a.Value),
		Expiry:     a.Expiry,
		TraderKey:  hex.EncodeToString(a.TraderKeyRaw[:]),
		BatchKey:   hex.EncodeToString(a.BatchKey.SerializeCompressed()),
		Secret:     hex.EncodeToString(a.Secret[:]),
		State:      uint8(a.State),
		HeightHint: a.HeightHint,
		OutPoint:   a.OutPoint.String(),
		UserAgent:  a.UserAgent,
	}

	if a.AuctioneerKey != nil {
		sqlAccount.AuctioneerKey = *newSQLKeyDescriptor(a.AuctioneerKey)
	}

	if a.LatestTx != nil {
		var err error
		sqlAccount.LatestTx, err = msgTxToString(a.LatestTx)
		if err != nil {
			return err
		}
	}

	return s.tx.Clauses(
		clause.OnConflict{UpdateAll: true},
	).Create(sqlAccount).Error
}

// GetAccount will select the account corresponding to the passed trader key
// or nil if the account doesn't exist.
func (s *SQLTransaction) GetAccount(traderKey *btcec.PublicKey) (
	*account.Account, error) {

	var accounts []SQLAccount

	result := s.tx.Where(
		"trader_key  = ?",
		hex.EncodeToString(traderKey.SerializeCompressed()),
	).Find(&accounts)

	if result.Error != nil {
		return nil, result.Error
	}

	if len(accounts) == 0 {
		return nil, nil
	}

	return accounts[0].toAccount()
}

// UpdateAccountsSQL is a helper to insert or update accounts into our SQL
// database. It does not return any errors to not block normal processing as
// we only intend to mirror into SQL and our main source of truth is etcd.
func UpdateAccountsSQL(ctx context.Context, store *SQLGORMStore,
	accounts ...*account.Account) {

	if store == nil || len(accounts) == 0 {
		return
	}

	err := store.Transaction(ctx, func(tx *SQLTransaction) error {
		for _, acct := range accounts {
			if err := tx.UpdateAccount(acct); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		log.Warnf("Unable to store accounts to SQL: %v", err)
	}
}
