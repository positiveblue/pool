package account

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

const (
	// AuctioneerKeyFamily is the key family that we'll use to derive the
	// singleton auctioneer key.
	AuctioneerKeyFamily keychain.KeyFamily = 221

	// AuctioneerWitnessScriptSize: 35 bytes
	//    - OP_DATA: 1 byte
	//    - <pubkey: 33 bytes
	//    - OP_CHECKSIG: 1 byte
	AuctioneerWitnessScriptSize = 35

	// AuctioneerWitnessSize: 108
	//    - num_witness_elements: 1 byte
	//    - sig_len_varint: 1 byte
	//    - <sig>: 73 bytes
	//    - witness_script_len_varint: 1 byte
	//    - witness_script: 35 bytes
	AuctioneerWitnessSize = 76 + AuctioneerWitnessScriptSize
)

// AuctioneerV0WitnessScript is the witness script that the auctioneer will use
// for its master account output at version 0. Normally we'd just make this a
// regular p2wkh output, however in order to ensure this output also blends in
// uniformly with all the other scripts in the batch execution transaction, we
// make this a P2WSH output. In a post-tapscript future, all scripts as well as
// their cooperative spending paths will appear to be the exact same!
//
// The script is a simple P2PKH type script, but wrapped in P2WSH:
//
// <auctioneerBatchKey> OP_CHECKSIG
//
// The auctioneerBatchKey is derived as:
//  * auctioneerBatchKey = auctioneerKey + sha256(batchKey || auctioneerKey)
//
// The witness for the script is simply: <sig> <witnessScript>.
func AuctioneerV0WitnessScript(batchKey,
	auctioneerKey *btcec.PublicKey) ([]byte, error) {

	auctioneerBatchKey := input.TweakPubKey(auctioneerKey, batchKey)

	builder := txscript.NewScriptBuilder()
	builder.AddData(auctioneerBatchKey.SerializeCompressed())
	builder.AddOp(txscript.OP_CHECKSIG)

	return builder.Script()
}

// AuctioneerV1TaprootKey returns the Taproot public key of the auctioneer
// account.
func AuctioneerV1TaprootKey(batchKey,
	auctioneerKey *btcec.PublicKey) *btcec.PublicKey {

	tweakedInternalKey := input.TweakPubKey(auctioneerKey, batchKey)
	return txscript.ComputeTaprootKeyNoScript(tweakedInternalKey)
}

// AuctioneerAccountScript generates the P2WSH witness script for the
// AuctioneerV0WitnessScript. This is a simple checksig script wrapped in a layer
// of P2WSH. We're essentially making the "injected" p2wkh script explicit.
func AuctioneerAccountScript(version AuctioneerVersion, batchKey,
	auctioneerKey *btcec.PublicKey) ([]byte, error) {

	switch version {
	case VersionInitialNoVersion:
		witnessScript, err := AuctioneerV0WitnessScript(
			batchKey, auctioneerKey,
		)
		if err != nil {
			return nil, err
		}

		return input.WitnessScriptHash(witnessScript)

	case VersionTaprootEnabled:
		taprootKey := AuctioneerV1TaprootKey(batchKey, auctioneerKey)

		return payToWitnessTaprootScript(taprootKey)

	default:
		return nil, fmt.Errorf("invalid auctioneer version <%d>",
			version)
	}
}

// AuctioneerWitness constructs a valid witness for spending the auctioneer's
// master account at the given version. This witness is simply:
//
// V0: <sig> <witnessScript>
// V1: <sig>
//
// NOTE: The sig passed in should not already have a sighash flag applied,
// we'll attach on here ourselves manually (for V0).
func AuctioneerWitness(version AuctioneerVersion, witnessScript,
	sig []byte) wire.TxWitness {

	switch version {
	case VersionTaprootEnabled:
		witness := make(wire.TxWitness, 1)
		witness[0] = sig

		return witness

	default:
		witness := make(wire.TxWitness, 2)
		witness[0] = append(sig, byte(txscript.SigHashAll))
		witness[1] = witnessScript

		return witness
	}
}

// payToWitnessTaprootScript creates a new script to pay to a version 1
// (taproot) witness program. The passed hash is expected to be valid.
func payToWitnessTaprootScript(taprootKey *btcec.PublicKey) ([]byte, error) {
	builder := txscript.NewScriptBuilder()

	builder.AddOp(txscript.OP_1)
	builder.AddData(schnorr.SerializePubKey(taprootKey))

	return builder.Script()
}
