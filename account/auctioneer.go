package account

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/lndclient"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
)

// AuctioneerVersion represents the version of the auctioneer account.
type AuctioneerVersion uint8

const (
	// VersionInitialNoVersion is the initial version any legacy account has
	// that technically wasn't versioned at all.
	VersionInitialNoVersion AuctioneerVersion = 0

	// VersionTaprootEnabled is the version that introduced account
	// versioning and the upgrade to Taproot (with MuSig2 multi-sig).
	VersionTaprootEnabled AuctioneerVersion = 1
)

// String returns the string representation of the version.
func (v AuctioneerVersion) String() string {
	switch v {
	case VersionInitialNoVersion:
		return "account_p2wsh"

	case VersionTaprootEnabled:
		return "account_p2tr"

	default:
		return fmt.Sprintf("unknown <%d>", v)
	}
}

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

	// IsPending determines whether the account is pending its confirmation
	// in the chain.
	IsPending bool

	// Version is the version of the auctioneer account.
	Version AuctioneerVersion
}

// accountV0WitnessScript computes the raw witness script of the target account.
func (a *Auctioneer) accountV0WitnessScript() ([]byte, error) {
	batchKey, err := btcec.ParsePubKey(a.BatchKey[:])
	if err != nil {
		return nil, err
	}

	return AuctioneerV0WitnessScript(
		batchKey, a.AuctioneerKey.PubKey,
	)
}

// Output returns the output of auctioneer's output as it should appear on the
// last batch transaction.
func (a *Auctioneer) Output() (*wire.TxOut, error) {
	batchKey, err := btcec.ParsePubKey(a.BatchKey[:])
	if err != nil {
		return nil, err
	}

	pkScript, err := AuctioneerAccountScript(
		a.Version, batchKey, a.AuctioneerKey.PubKey,
	)
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
	tx *wire.MsgTx, inputIndex int, version AuctioneerVersion,
	prevOutputs []*wire.TxOut) (wire.TxWitness, error) {

	// First, we'll compute the witness script, and its corresponding
	// witness program as we'll need both for the sign descriptor below.
	witnessScript, err := a.accountV0WitnessScript()
	if err != nil {
		return nil, err
	}

	// Next, as the raw auctioneer key isn't used in the output scripts,
	// we'll construct the current account tweak using the current batch
	// key:
	//    * batchTweak = sha256(batchKey || auctioneerKey)
	//    * accountKey = auctioneerKey  + batchTweak*G
	batchKey, err := btcec.ParsePubKey(a.BatchKey[:])
	if err != nil {
		return nil, err
	}
	batchTweak := input.SingleTweakBytes(batchKey, a.AuctioneerKey.PubKey)
	pkScript, err := AuctioneerAccountScript(
		version, batchKey, a.AuctioneerKey.PubKey,
	)
	if err != nil {
		return nil, err
	}

	// Now that we have all the required items, we'll query the Signer for
	// a valid signature for our account output.
	signDesc := &lndclient.SignDescriptor{
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
	}

	// Signing for a Taproot input works somewhat differently.
	if version == VersionTaprootEnabled {
		signDesc.WitnessScript = nil
		signDesc.HashType = txscript.SigHashDefault
		signDesc.SignMethod = input.TaprootKeySpendBIP0086SignMethod
	}

	ctx := context.Background()
	sigs, err := signer.SignOutputRaw(
		ctx, tx, []*lndclient.SignDescriptor{signDesc}, prevOutputs,
	)
	if err != nil {
		return nil, err
	}

	// Next we'll construct the account witness given our witness script,
	// pubkey, and signature.
	witness := AuctioneerWitness(version, witnessScript, sigs[0])

	txCopy := tx.Copy()
	txCopy.TxIn[inputIndex].Witness = witness

	// As a final step, we'll ensure the signature we just generated above
	// is valid.
	prevOutputFetcher := txscript.NewMultiPrevOutFetcher(nil)
	for idx := range prevOutputs {
		prevOutputFetcher.AddPrevOut(
			txCopy.TxIn[idx].PreviousOutPoint, prevOutputs[idx],
		)
	}
	sigHashes := txscript.NewTxSigHashes(txCopy, prevOutputFetcher)
	vm, err := txscript.NewEngine(
		pkScript, txCopy, inputIndex, txscript.StandardVerifyFlags,
		nil, sigHashes, int64(a.Balance), prevOutputFetcher,
	)
	if err != nil {
		return nil, err
	}
	if err := vm.Execute(); err != nil {
		return nil, fmt.Errorf("invalid master acct sig: %v", err)
	}

	return witness, nil
}

// InputWitnesses attempts to construct fully valid witnesses which can be used
// to spend the given UTXOs in the batch execution transaction. It is assumed
// that all given inputs are managed by the signer.
func InputWitnesses(signer lndclient.SignerClient, tx *wire.MsgTx,
	inputs map[int]*lnwallet.Utxo,
	prevOutputs []*wire.TxOut) (map[int]*input.Script, error) {

	// For each input, create a signdescriptor.
	signDescs := make([]*lndclient.SignDescriptor, 0, len(inputs))
	for idx, utxo := range inputs {
		signDesc := &lndclient.SignDescriptor{
			Output: &wire.TxOut{
				Value:    int64(utxo.Value),
				PkScript: utxo.PkScript,
			},
			InputIndex: idx,
		}

		scriptTy := txscript.GetScriptClass(utxo.PkScript)
		switch scriptTy {
		case txscript.WitnessV0PubKeyHashTy:
			signDesc.HashType = txscript.SigHashAll

		case txscript.WitnessV1TaprootTy:
			// We only support BIP0086 keyspend path.
			signDesc.HashType = txscript.SigHashDefault
			signDesc.SignMethod = input.TaprootKeySpendBIP0086SignMethod

		default:
			return nil, fmt.Errorf("unsupported script type: %v",
				scriptTy)
		}

		signDescs = append(signDescs, signDesc)
	}

	// We'll query the signer for witnesses for these inputs.
	witnesses := make(map[int]*input.Script)
	if len(signDescs) > 0 {
		ctx := context.Background()
		scripts, err := signer.ComputeInputScript(
			ctx, tx, signDescs, prevOutputs,
		)
		if err != nil {
			return nil, err
		}

		for i, script := range scripts {
			inputIndex := signDescs[i].InputIndex
			witnesses[inputIndex] = script
		}
	}

	txCopy := tx.Copy()
	for i, w := range witnesses {
		txCopy.TxIn[i].SignatureScript = w.SigScript
		txCopy.TxIn[i].Witness = w.Witness
	}

	// As a final step, we'll ensure the signatures we just generated above
	// is valid.
	prevOutputFetcher := txscript.NewMultiPrevOutFetcher(nil)
	for idx := range prevOutputs {
		prevOutputFetcher.AddPrevOut(
			txCopy.TxIn[idx].PreviousOutPoint, prevOutputs[idx],
		)
	}
	sigHashes := txscript.NewTxSigHashes(txCopy, prevOutputFetcher)
	for i, utxo := range inputs {
		vm, err := txscript.NewEngine(
			utxo.PkScript, txCopy, i, txscript.StandardVerifyFlags,
			nil, sigHashes, int64(utxo.Value), prevOutputFetcher,
		)
		if err != nil {
			return nil, err
		}
		if err := vm.Execute(); err != nil {
			return nil, fmt.Errorf("invalid input sig: %v", err)
		}
	}

	return witnesses, nil
}
