package account

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightningnetwork/lnd/keychain"
)

var (
	// DefaultAccountKeyWindow is the number of account keys that are
	// derived to be checked on recovery. This is the absolute maximum
	// number of accounts that can ever be restored. But the trader doesn't
	// necessarily make as many requests on recovery, if no accounts are
	// found for a certain number of tries.
	DefaultAccountKeyWindow uint32 = 20
)

type auctioneerData struct {
	// TODO
	InitialBatchKey *btcec.PublicKey
	// TODO
	PubKey *btcec.PublicKey
	// TODO
	FirstBatchBlock uint32
}

// getAuctioneerData
func getAuctioneerData(network string) (*auctioneerData, error) {
	var auctioneerKey string
	var batchKey string
	var fstBlock uint32

	switch network {
	case "mainnet":
		auctioneerKey = "034d1fd68b5cd92f694766aa8b3f90131613d1b731" +
			"caf03be5d2d34c333163c606"
		batchKey = "02824d0cbac65e01712124c50ff2cc74ce22851d7b444c1" +
			"bf2ae66afefb8eaf27f"
		fstBlock = 648168
	case "testnet":
		auctioneerKey = ""
		batchKey = ""
		fstBlock = 0
	case "regtest":
		// NOTE: populate during manual testing
		auctioneerKey = "0327f7100595489c896542d7dca7592f4f1b1e86eb3997be6ccb56ba20bc2ea15b"
		batchKey = "02824d0cbac65e01712124c50ff2cc74ce22851d7b444c1" +
			"bf2ae66afefb8eaf27f"
		fstBlock = 100
	default:
		return nil, fmt.Errorf("%s is not a valid network", network)
	}

	akStr, err := hex.DecodeString(auctioneerKey)
	if err != nil {
		return nil, err
	}
	ak, err := btcec.ParsePubKey(akStr, btcec.S256())
	if err != nil {
		return nil, err
	}

	bkStr, err := hex.DecodeString(batchKey)
	if err != nil {
		return nil, err
	}
	bk, err := btcec.ParsePubKey(bkStr, btcec.S256())
	if err != nil {
		return nil, err
	}

	return &auctioneerData{
		PubKey:          ak,
		InitialBatchKey: bk,
		FirstBatchBlock: fstBlock,
	}, nil
}

type RecoveryConfig struct {
	Network string

	AccountTarget uint32

	FirstBlock uint32
	LastBlock  uint32

	ExpirySpans []uint32

	Transactions []lndclient.Transaction

	Signer lndclient.SignerClient
	Wallet lndclient.WalletKitClient
}

type AccountRecovery struct {
	cfg RecoveryConfig

	possibleAccounts map[*Account]struct{}

	recoveredAccounts []*Account

	auctioneerData *auctioneerData
}

func NewAccuntRecovery(cfg RecoveryConfig) (*AccountRecovery, error) {
	auctioneerData, err := getAuctioneerData(cfg.Network)
	if err != nil {
		return nil, err
	}

	if cfg.FirstBlock < auctioneerData.FirstBatchBlock {
		return nil, fmt.Errorf("first batch block (%d) is lower than "+
			"the first batch block (%d)", cfg.FirstBlock,
			auctioneerData.FirstBatchBlock,
		)
	}
	if cfg.LastBlock < auctioneerData.FirstBatchBlock {
		return nil, fmt.Errorf("last batch block (%d) is lower than "+
			"the first batch block (%d)", cfg.LastBlock,
			auctioneerData.FirstBatchBlock,
		)
	}

	return &AccountRecovery{
		cfg:              cfg,
		auctioneerData:   auctioneerData,
		possibleAccounts: make(map[*Account]struct{}, cfg.AccountTarget),
	}, nil
}

func (a *AccountRecovery) Recover(ctx context.Context) ([]*Account, error) {

	if err := a.recoverInitalAccountState(ctx); err != nil {
		return nil, err
	}

	if err := a.updateAccountStates(); err != nil {
		return nil, err
	}

	return a.recoveredAccounts, nil
}

func (a *AccountRecovery) recreatePossibleAccounts(ctx context.Context) error {
	// Prepare the keys we are going to try. Possibly not all of them will
	// be used.
	KeyDescriptors, err := GenerateRecoveryKeys(
		ctx, a.cfg.AccountTarget, a.cfg.Wallet,
	)
	if err != nil {
		return fmt.Errorf("error generating keys: %v", err)
	}

	for _, keyDes := range KeyDescriptors {
		secret, err := a.cfg.Signer.DeriveSharedKey(
			ctx, a.auctioneerData.PubKey, &keyDes.KeyLocator,
		)
		if err != nil {
			return fmt.Errorf("error deriving shared key: %v", err)
		}

		acc := &Account{
			TraderKey:     keyDes,
			AuctioneerKey: a.auctioneerData.PubKey,
			Secret:        secret,
		}
		a.possibleAccounts[acc] = struct{}{}
	}

	return nil
}

func (a *AccountRecovery) recoverInitalAccountState(ctx context.Context) error {
	log.Debugf(
		"Recovering initial states for %d accounts...", a.cfg.AccountTarget,
	)

	if err := a.recreatePossibleAccounts(ctx); err != nil {
		return err
	}

	var recoveredAccounts []*Account

	batchKey := a.auctioneerData.InitialBatchKey
	batchKey = poolscript.DecrementKey(batchKey)

searchLoop:
	for batchCounter := 0; batchCounter < 20; batchCounter++ {
		batchKey = poolscript.IncrementKey(batchKey)
		for _, expiry := range a.cfg.ExpirySpans {
			for height := a.cfg.FirstBlock; height < a.cfg.LastBlock; height++ {
			nextAccount:
				for acc := range a.possibleAccounts {
					script, err := poolscript.AccountScript(
						height+expiry,
						acc.TraderKey.PubKey,
						a.auctioneerData.PubKey,
						batchKey,
						acc.Secret,
					)
					if err != nil {
						// todo log
						goto nextAccount
					}

					for _, tx := range a.cfg.Transactions {
						idx, ok := poolscript.LocateOutputScript(tx.Tx, script)
						if ok {
							log.Debugf(
								"found accout with trader key %v",
								hex.EncodeToString(
									acc.TraderKey.PubKey.SerializeCompressed(),
								),
							)
							acc.Expiry = height + expiry
							acc.Value = btcutil.Amount(tx.Tx.TxOut[idx].Value)
							acc.BatchKey = batchKey
							acc.HeightHint = height
							acc.OutPoint = wire.OutPoint{Hash: tx.Tx.TxHash(), Index: idx}
							acc.LatestTx = tx.Tx
							acc.State = StateOpen
							recoveredAccounts = append(recoveredAccounts, acc)
							delete(a.possibleAccounts, acc)

							if len(recoveredAccounts) == int(a.cfg.AccountTarget) {
								break searchLoop
							}
						}
					}
				}
			}
		}
	}

	log.Debugf(
		"found initial tx for %d/%d accounts", len(recoveredAccounts),
		a.cfg.AccountTarget,
	)

	a.recoveredAccounts = recoveredAccounts
	return nil
}

func (a *AccountRecovery) matchAccount(acc *Account,
	tx *wire.MsgTx) (*Account, error) {

	newAcc := acc.Copy()
	firstBlock := acc.HeightHint
	lastBlock := a.cfg.LastBlock
	newAcc.BatchKey = poolscript.IncrementKey(newAcc.BatchKey)

	for height := firstBlock; height <= lastBlock; height++ {
		for _, expiry := range a.cfg.ExpirySpans {
			script, err := poolscript.AccountScript(
				height+expiry,
				newAcc.TraderKey.PubKey,
				a.auctioneerData.PubKey,
				newAcc.BatchKey,
				acc.Secret,
			)
			if err != nil {
				continue
			}

			idx, ok := poolscript.LocateOutputScript(tx, script)
			if ok {
				newAcc.Expiry = height + expiry
				newAcc.Value = btcutil.Amount(tx.TxOut[idx].Value)
				newAcc.HeightHint = height
				newAcc.OutPoint = wire.OutPoint{Hash: tx.TxHash(), Index: idx}
				newAcc.LatestTx = tx
				fmt.Println(tx.TxOut[idx].Value)
				if newAcc.Value > 0 {
					newAcc.State = StateOpen
				} else {
					newAcc.State = StateClosed
				}

				return newAcc, nil
			}
		}
	}

	return nil, fmt.Errorf("account update not found")
}

func (a *AccountRecovery) updateAccountStates() error {

nextAccount:
	for idx, acc := range a.recoveredAccounts {
		for _, tx := range a.cfg.Transactions {
			if poolscript.LocateInputScript(tx.Tx, acc.OutPoint) {
				newAcc, err := a.matchAccount(acc, tx.Tx)
				if err != nil {
					continue
				}
				if newAcc != nil {
					a.recoveredAccounts[idx] = newAcc.Copy()
					acc = newAcc
					if acc.State == StateClosed {
						goto nextAccount
					}

				}
			}
		}
	}
	return nil
}

// GenerateRecoveryKeys generates a list of key descriptors for all possible
// keys that could be used for trader accounts, up to a hard coHashded limit.
func GenerateRecoveryKeys(ctx context.Context,
	accountTarget uint32,
	wallet lndclient.WalletKitClient) ([]*keychain.KeyDescriptor, error) {

	acctKeys := make([]*keychain.KeyDescriptor, accountTarget)
	for i := uint32(0); i < accountTarget; i++ {
		key, err := wallet.DeriveKey(ctx, &keychain.KeyLocator{
			Family: poolscript.AccountKeyFamily,
			Index:  i,
		})
		if err != nil {
			return nil, err
		}

		acctKeys[i] = key
	}
	return acctKeys, nil
}
