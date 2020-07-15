package subastadb

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightningnetwork/lnd/chanbackup"
)

// TestLifetimePackages ensures that we are able to perform the different
// operations available for channel lifetime packages.
func TestLifetimePackages(t *testing.T) {
	ctx := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// Populate all of the fields to ensure proper serialization. Only
	// AskAccountKey, BidAccountKey, AskNodeKey, and BidNodeKey need to be
	// unique.
	key := fromHex("03fa26bab2220c62ea5dea00c157c7bd76714fa48d75d1787df7b266a25d30a563")
	askAccountKey := fromHex("0378498a866a62251e6dd32920802ac985352cfef4be788cf0cd17495d85303512")
	bidAccountKey := fromHex("03008c40e0f356d39bece057e690036e4063ed2c58aa8d43179d144c64fd9b294a")
	askNodeKey := fromHex("0311adb692be1a26396a1580d83ec5de188f33b515fa9f433e916cf1155ab80ae2")
	bidNodeKey := fromHex("03120bb78e5a0eea88996be013a37733e6faba66bc495cf0b3aa3f69a28de3e7db")
	pkg := &chanenforcement.LifetimePackage{
		ChannelPoint: wire.OutPoint{
			Hash:  chainhash.Hash{0x01, 0x02, 0x03},
			Index: 1,
		},
		ChannelScript:       []byte{0x01, 0x03, 0x03, 0x07},
		HeightHint:          100,
		MaturityDelta:       1000,
		Version:             chanbackup.AnchorsCommitVersion,
		AskAccountKey:       askAccountKey,
		BidAccountKey:       bidAccountKey,
		AskNodeKey:          askNodeKey,
		BidNodeKey:          bidNodeKey,
		AskPaymentBasePoint: key,
		BidPaymentBasePoint: key,
	}

	// We'll use a helper closure to correctly assert the state of our store
	// with respect to the channel lifetime package.
	assertPackageInStore := func(exists bool) {
		t.Helper()

		pkgs, err := store.LifetimePackages(ctx)
		if err != nil {
			t.Fatalf("unable to retrieve channel lifetime "+
				"packages: %v", err)
		}
		switch {
		case exists && len(pkgs) != 1:
			t.Fatal("expected single lifetime package to exist")
		case !exists && len(pkgs) != 0:
			t.Fatal("found unexpected lifetime package")
		}

		if exists {
			assertJSONDeepEqual(t, pkgs[0], pkg)
		}
	}

	// We'll start by storing a sample channel lifetime package.
	if err := store.StoreLifetimePackage(ctx, pkg); err != nil {
		t.Fatalf("unable to store channel lifetime package: %v", err)
	}

	// Storing the same package again should result in
	// ErrLifetimePackageAlreadyExists.
	err := store.StoreLifetimePackage(ctx, pkg)
	if err != ErrLifetimePackageAlreadyExists {
		t.Fatalf("expected ErrLifetimePackageAlreadyExists, got \"%v\"",
			err)
	}

	// We should be able to retrieve it from store.
	assertPackageInStore(true)

	// We should also be able to prune it, assuming that a premature spend
	// has not occurred.
	if err := store.PruneLifetimePackage(ctx, pkg); err != nil {
		t.Fatalf("unable to prune channel lifetime package: %v", err)
	}
	assertPackageInStore(false)

	// We'll then store the package again.
	if err := store.StoreLifetimePackage(ctx, pkg); err != nil {
		t.Fatalf("unable to store channel lifetime package: %v", err)
	}
	assertPackageInStore(true)

	// This time, we'll assume a premature spend has occurred, so we'll need
	// to ban the trader responsible. We'll assume the seller is
	// responsible.
	const currentHeight = 100
	const expiration = currentHeight + initialBanDuration
	err = store.EnforceLifetimeViolation(ctx, pkg, currentHeight)
	if err != nil {
		t.Fatalf("unable to enforce channel lifetime violation: %v", err)
	}

	// The seller's account and node should be banned for the initial
	// duration (144 blocks). The lifetime package should also no longer
	// exist at this point, since we've already acted upon the violation.
	assertPackageInStore(false)
	assertAccountBanStatus(
		t, store, pkg.AskAccountKey, currentHeight, true, expiration,
	)
	assertNodeBanStatus(
		t, store, pkg.AskNodeKey, currentHeight, true, expiration,
	)
}
