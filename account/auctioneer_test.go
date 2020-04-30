package account

import (
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/keychain"
)

// TestAuctioneerAccountWitness tests that we're able to properly produce a
// valid witness for the auctioneer's account given a set of starting params.
func TestAuctioneerAccountWitness(t *testing.T) {
	t.Parallel()

	// First we'll generate the auctioneer key and a random batch key that
	// we'll use for this purpose.
	batchKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatalf("unable to make batch key: %v", err)
	}
	auctioneerKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatalf("unable to make auctioneer key: %v", err)
	}

	// With these two keys generated, we can now make the full auctioneer
	// account.
	acct := &Auctioneer{
		Balance: 1_000_000,
		AuctioneerKey: &keychain.KeyDescriptor{
			PubKey: auctioneerKey.PubKey(),
		},
	}
	copy(acct.BatchKey[:], batchKey.PubKey().SerializeCompressed())

	acctOutput, err := acct.Output()
	if err != nil {
		t.Fatalf("unable to create acct output: %v", err)
	}

	// We'll construct a sweep transaction that just sweeps the output back
	// into an identical one.
	spendTx := wire.NewMsgTx(2)
	spendTx.AddTxIn(&wire.TxIn{})
	spendTx.AddTxOut(acctOutput)

	// Now we'll construct the witness to simulate a spend of the account.
	signer := &MockSigner{auctioneerKey}
	witness, err := acct.AccountWitness(signer, spendTx, 0)
	if err != nil {
		t.Fatalf("unable to generate witness: %v", err)
	}
	spendTx.TxIn[0].Witness = witness

	// Ensure that the witness script we generate is valid.
	vm, err := txscript.NewEngine(
		acctOutput.PkScript,
		spendTx, 0, txscript.StandardVerifyFlags, nil,
		nil, acctOutput.Value,
	)
	if err != nil {
		t.Fatalf("unable to create engine: %v", err)
	}
	if err := vm.Execute(); err != nil {
		t.Fatalf("invalid spend: %v", err)
	}
}
