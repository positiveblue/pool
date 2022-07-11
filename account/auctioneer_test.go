package account

import (
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/stretchr/testify/require"
)

// TestAuctioneerAccountWitness tests that we're able to properly produce a
// valid witness for the auctioneer's account given a set of starting params.
func TestAuctioneerAccountWitness(t *testing.T) {
	t.Parallel()

	// First we'll generate the auctioneer key and a random batch key that
	// we'll use for this purpose.
	batchKey, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("unable to make batch key: %v", err)
	}
	auctioneerKey, err := btcec.NewPrivateKey()
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
	signer := &MockSigner{
		[]*btcec.PrivateKey{auctioneerKey},
	}
	witness, err := acct.AccountWitness(
		signer, spendTx, 0, []*wire.TxOut{acctOutput},
	)
	if err != nil {
		t.Fatalf("unable to generate witness: %v", err)
	}
	spendTx.TxIn[0].Witness = witness

	// Ensure that the witness script we generate is valid.
	vm, err := txscript.NewEngine(
		acctOutput.PkScript, spendTx, 0, txscript.StandardVerifyFlags,
		nil, nil, acctOutput.Value,
		txscript.NewCannedPrevOutputFetcher(
			acctOutput.PkScript, acctOutput.Value,
		),
	)
	if err != nil {
		t.Fatalf("unable to create engine: %v", err)
	}
	if err := vm.Execute(); err != nil {
		t.Fatalf("invalid spend: %v", err)
	}
}

// TestAuctioneerInputWitness tests that we are able to properly produce valid
// witnesses for arbitrary extra inputs to the batch transaction, by querying
// the backing wallet to compute input scripts.
func TestAuctioneerInputWitness(t *testing.T) {
	t.Parallel()

	chainParams := &chaincfg.RegressionNetParams
	const value = 100_000

	spendTx := wire.NewMsgTx(2)
	spendTx.AddTxOut(&wire.TxOut{})

	signer := &MockSigner{}

	// Create inputs from simple P2WKHs derived from private keys.
	const numInputs = 3
	inputs := make(map[int]*lnwallet.Utxo)

	for i := 0; i < numInputs; i++ {
		privKey, err := btcec.NewPrivateKey()
		require.NoError(t, err)

		signer.PrivKeys = append(signer.PrivKeys, privKey)

		pubKey := privKey.PubKey()
		pubKeyHash := btcutil.Hash160(pubKey.SerializeCompressed())

		p2wkhAddr, err := btcutil.NewAddressWitnessPubKeyHash(
			pubKeyHash, chainParams,
		)
		require.NoError(t, err)

		p2wkhScript, err := txscript.PayToAddrScript(p2wkhAddr)
		require.NoError(t, err)

		inputs[i] = &lnwallet.Utxo{
			Value:    value,
			PkScript: p2wkhScript,
		}
		spendTx.AddTxIn(&wire.TxIn{})
	}

	// Now we'll construct the witnesses to simulate a spend of inputs.
	witnesses, err := InputWitnesses(signer, spendTx, inputs)
	require.NoError(t, err)

	for i, w := range witnesses {
		spendTx.TxIn[i].SignatureScript = w.SigScript
		spendTx.TxIn[i].Witness = w.Witness
	}

	// Ensure that the witness script we generate is valid.
	for i, in := range inputs {
		vm, err := txscript.NewEngine(
			in.PkScript, spendTx, i, txscript.StandardVerifyFlags,
			nil, nil, value,
			txscript.NewCannedPrevOutputFetcher(
				in.PkScript, value,
			),
		)
		require.NoError(t, err)

		err = vm.Execute()
		require.NoError(t, err)
	}
}
