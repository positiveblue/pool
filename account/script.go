package account

import (
	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/txscript"
	"github.com/lightningnetwork/lnd/input"
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
