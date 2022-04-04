package account

import (
	"github.com/btcsuite/btcd/btcec/v2"
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

// AuctioneerWitnessScript is the witness script that the auctioneer will use
// for its master account output. Normally we'd just make this a regular p2wkh
// output, however in order to ensure this output also blends in uniformly with
// all the other scripts in the batch execution transaction, we make this a
// P2WSH output. In a post-tapscript future, all scripts as well as their
// cooperative spending paths will appear to be the exact same!
//
// The script is a simple P2PKH type script, but wrapped in P2WSH:
//
// <auctioneerBatchKey> OP_CHECKSIG
//
// The auctioneerBatchKey is derived as:
//  * auctioneerBatchKey = auctioneerKey + sha256(batchKey || auctioneerKey)
//
// The witness for the script is simply: <sig> <witnessScript>
func AuctioneerWitnessScript(batchKey, auctioneerKey *btcec.PublicKey) ([]byte, error) {
	auctioneerBatchKey := input.TweakPubKey(auctioneerKey, batchKey)

	builder := txscript.NewScriptBuilder()
	builder.AddData(auctioneerBatchKey.SerializeCompressed())
	builder.AddOp(txscript.OP_CHECKSIG)

	return builder.Script()
}

// AuctioneerAccountScript generates the P2WSH witness script for the
// AuctioneerWitnessScript. This is a simple checksig script wrapped in a layer
// of P2WSH. We're essentially making the "injected" p2wkh script explicit.
func AuctioneerAccountScript(batchKey, auctioneerKey *btcec.PublicKey) ([]byte, error) {
	witnessScript, err := AuctioneerWitnessScript(batchKey, auctioneerKey)
	if err != nil {
		return nil, nil
	}

	return input.WitnessScriptHash(witnessScript)
}

// AuctioneerWitness constructs a valid witness for spending the auctioneer's
// master account. This witness is simply: <sig> <witnessScript>
//
// NOTE: The sig passed in should not already have a sighash flag applied,
// we'll attach on here ourselves manually.
func AuctioneerWitness(witnessScript, sig []byte) wire.TxWitness {
	witness := make(wire.TxWitness, 2)
	witness[0] = append(sig, byte(txscript.SigHashAll))
	witness[1] = witnessScript

	return witness
}
