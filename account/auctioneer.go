package account

import (
	"context"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

// Auctioneer is the master auctioneer account, this will be threaded along in
// the batch along side all the other accounts. We'll use this account to
// accrue all the execution fees we earn each batch.
type Auctioneer struct {
	// OutPoint is the outpoint of the master account. If this is a zero
	// outpoint, then no account exists yet.
	OutPoint wire.OutPoint

	// Balance is the current balance of the master account.
	//
	// TODO(roasbeef): need to account for external sends? to replenish?
	Balance btcutil.Amount

	// AuctioneerKey is the base key for the auctioneer, this is a static
	// parameter that's created when the system is initialized.
	AuctioneerKey *keychain.KeyDescriptor

	// BatchKey is the current batch key for the auctioneer's account, this
	// will be incremented by one each batch.
	BatchKey [33]byte
}

// AccountWitnessScript computes the raw witness script of the target account.
func (a *Auctioneer) AccountWitnessScript() ([]byte, error) {
	batchKey, err := btcec.ParsePubKey(
		a.BatchKey[:], btcec.S256(),
	)
	if err != nil {
		return nil, err
	}

	return AuctioneerWitnessScript(
		batchKey, a.AuctioneerKey.PubKey,
	)
}

// Output returns the output of auctioneer's output as it should appear on the
// last batch transaction.
func (a *Auctioneer) Output() (*wire.TxOut, error) {
	witnessScript, err := a.AccountWitnessScript()
	if err != nil {
		return nil, err
	}
	pkScript, err := input.WitnessScriptHash(witnessScript)
	if err != nil {
		return nil, err
	}

	return &wire.TxOut{
		PkScript: pkScript,
		Value:    int64(a.Balance),
	}, nil
}

// AccountWitness attempts to construct a fully valid witness which can be used
// to spend the auctioneer's account on the batch execution transaction.
func (a *Auctioneer) AccountWitness(signer lndclient.SignerClient,
	tx *wire.MsgTx, inputIndex int) (wire.TxWitness, error) {

	// First, we'll compute the witness script, and its corresponding
	// witness program as we'll need both for the sign descriptor below.
	witnessScript, err := a.AccountWitnessScript()
	if err != nil {
		return nil, err
	}
	pkScript, err := input.WitnessScriptHash(witnessScript)
	if err != nil {
		return nil, err
	}

	// Next, as the raw auctioneer key isn't used in the output scripts,
	// we'll construct the current account tweak using the current batch
	// key:
	//    * batchTweak = sha256(batchKey || auctioneerKey)
	//    * accountKey = auctioneerKey  + batchTweak*G
	batchKey, err := btcec.ParsePubKey(
		a.BatchKey[:], btcec.S256(),
	)
	if err != nil {
		return nil, err
	}
	batchTweak := input.SingleTweakBytes(
		batchKey, a.AuctioneerKey.PubKey,
	)

	// Now that we have all the required items, we'll query the Signer for
	// a valid signature for our account output.
	signDesc := &input.SignDescriptor{
		// The Signer API expects key locators _only_ when deriving keys
		// that are not within the wallet's default scopes.
		KeyDesc: keychain.KeyDescriptor{
			KeyLocator: a.AuctioneerKey.KeyLocator,
		},
		SingleTweak:   batchTweak,
		WitnessScript: witnessScript,
		Output: &wire.TxOut{
			Value:    int64(a.Balance),
			PkScript: pkScript,
		},
		HashType:   txscript.SigHashAll,
		InputIndex: inputIndex,
		SigHashes:  txscript.NewTxSigHashes(tx),
	}
	ctx := context.Background()
	sigs, err := signer.SignOutputRaw(
		ctx, tx, []*input.SignDescriptor{signDesc},
	)
	if err != nil {
		return nil, err
	}

	// Finally we'll construct the account witness given our witness
	// script, pubkey, and signature.
	return AuctioneerWitness(witnessScript, sigs[0]), nil
}
