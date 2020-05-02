package chanenforcement

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/chanbackup"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lntest/wait"
)

const (
	timeout = time.Second

	testMaturityHeight = 1337
)

var (
	nextIndex uint32 // Should be used atomically.

	testAskAccountKey       = fromHex("02187d1a0e30f4e5016fc1137363ee9e7ed5dde1e6c50f367422336df7a108b716")
	testBidAccountKey       = fromHex("036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0af58e0c9395446ba09")
	testAskNodeKey          = fromHex("035ee12c6262543b81232bbacb3b4685b9201c3ce3330949b1c1336d649b1ba0c4")
	testBidNodeKey          = fromHex("03d3d331262cf696472829ec527973bd383b94549671b398cb0a964bdf23cf27c2")
	testAskPaymentBasePoint = fromHex("03fa26bab2220c62ea5dea00c157c7bd76714fa48d75d1787df7b266a25d30a563")
	testBidPaymentBasePoint = fromHex("020c80b8d6fa00197fd467943162459aae79e062b53208a05f0fe6cbdea6ca1bd0")
)

func fromHex(s string) *btcec.PublicKey {
	rawKey, _ := hex.DecodeString(s)
	key, _ := btcec.ParsePubKey(rawKey, btcec.S256())
	return key
}

type testHarness struct {
	t               *testing.T
	notifier        *mockChainNotifier
	packageSource   *mockPackageSource
	channelEnforcer *ChannelEnforcer
}

func newTestHarness(t *testing.T) *testHarness {
	notifier := newMockChainNotifier()
	packageSource := newMockPackageSource()

	return &testHarness{
		t:             t,
		notifier:      notifier,
		packageSource: packageSource,
		channelEnforcer: New(&Config{
			ChainNotifier: notifier,
			PackageSource: packageSource,
		}),
	}
}

func (h *testHarness) start() {
	h.t.Helper()

	if err := h.channelEnforcer.Start(); err != nil {
		h.t.Fatalf("unable to start channel enforcer: %v", err)
	}
}

func (h *testHarness) stop() {
	h.t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.channelEnforcer.Stop()
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		h.t.Fatalf("channel enforcer did not shutdown cleanly within " +
			"timeout")
	}
}

func (h *testHarness) newLifetimePackage(
	version chanbackup.SingleBackupVersion) *LifetimePackage {

	index := atomic.AddUint32(&nextIndex, 1)
	pkg := &LifetimePackage{
		ChannelPoint: wire.OutPoint{
			Hash:  chainhash.Hash{},
			Index: index,
		},
		ChannelScript:       nil,
		HeightHint:          100,
		MaturityHeight:      testMaturityHeight,
		Version:             version,
		AskAccountKey:       testAskAccountKey,
		BidAccountKey:       testBidAccountKey,
		AskNodeKey:          testAskNodeKey,
		BidNodeKey:          testBidNodeKey,
		AskPaymentBasePoint: testAskPaymentBasePoint,
		BidPaymentBasePoint: testBidPaymentBasePoint,
	}

	h.packageSource.addLifetimePackages(pkg)

	return pkg
}

func (h *testHarness) enforceChannelLifetimes(pkgs ...*LifetimePackage) {
	h.t.Helper()

	err := h.channelEnforcer.EnforceChannelLifetimes(pkgs...)
	if err != nil {
		h.t.Fatalf("unable to enforce channel lifetimes: %v", err)
	}
}

func (h *testHarness) createSpendTx(pkg *LifetimePackage, channelInitiator,
	cooperative, missingToRemote bool) *wire.MsgTx {

	tx := wire.NewMsgTx(2)

	txIn := &wire.TxIn{}
	if cooperative {
		txIn.Sequence = wire.MaxTxInSequenceNum
	}
	tx.AddTxIn(txIn)

	if missingToRemote {
		return tx
	}

	var (
		witnessScript []byte
		err           error
	)
	switch {
	case pkg.Version == chanbackup.AnchorsCommitVersion && channelInitiator:
		witnessScript, err = input.CommitScriptToRemoteConfirmed(
			pkg.BidPaymentBasePoint,
		)

	case pkg.Version == chanbackup.AnchorsCommitVersion && !channelInitiator:
		witnessScript, err = input.CommitScriptToRemoteConfirmed(
			pkg.AskPaymentBasePoint,
		)

	case pkg.Version == chanbackup.TweaklessCommitVersion && channelInitiator:
		witnessScript, err = input.CommitScriptUnencumbered(
			pkg.BidPaymentBasePoint,
		)

	case pkg.Version == chanbackup.TweaklessCommitVersion && !channelInitiator:
		witnessScript, err = input.CommitScriptUnencumbered(
			pkg.AskPaymentBasePoint,
		)

	default:
		err = fmt.Errorf("unhandled case: version=%v, "+
			"channel_initiator=%v", pkg.Version, channelInitiator)
	}
	if err != nil {
		h.t.Fatalf("unable to create witness script: %v", err)
	}

	pkScript, err := input.WitnessScriptHash(witnessScript)
	if err != nil {
		h.t.Fatalf("unable to create witness script hash: %v", err)
	}

	tx.AddTxOut(wire.NewTxOut(btcutil.SatoshiPerBitcoin, pkScript))
	return tx
}

func (h *testHarness) notifyPrematureSpend(pkg *LifetimePackage,
	channelInitiator, cooperative, missingToRemote bool) {

	h.t.Helper()

	tx := h.createSpendTx(pkg, channelInitiator, cooperative, missingToRemote)
	txHash := tx.TxHash()

	err := h.notifier.notifySpend(pkg.ChannelPoint, &chainntnfs.SpendDetail{
		SpentOutPoint:  &pkg.ChannelPoint,
		SpendingHeight: testMaturityHeight - 1,
		SpendingTx:     tx,
		SpenderTxHash:  &txHash,
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *testHarness) notifyMatureSpend(pkg *LifetimePackage) {
	h.t.Helper()

	tx := wire.NewMsgTx(2)
	txHash := tx.TxHash()

	err := h.notifier.notifySpend(pkg.ChannelPoint, &chainntnfs.SpendDetail{
		SpentOutPoint:  &pkg.ChannelPoint,
		SpendingHeight: testMaturityHeight,
		SpendingTx:     tx,
		SpenderTxHash:  &txHash,
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *testHarness) assertNoViolation(pkg *LifetimePackage) {
	h.t.Helper()

	ctx := context.Background()
	err := wait.NoError(func() error {
		pkgs, err := h.packageSource.LifetimePackages(ctx)
		if err != nil {
			return err
		}
		for _, activePkg := range pkgs {
			if pkg.ChannelPoint == activePkg.ChannelPoint {
				return fmt.Errorf("lifetime package of %v not "+
					"pruned", pkg.ChannelPoint)
			}
		}

		if _, ok := h.packageSource.violations()[pkg.ChannelPoint]; ok {
			return fmt.Errorf("unexpected lifetime violation for %v",
				pkg.ChannelPoint)
		}

		return nil
	}, timeout)
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *testHarness) assertViolation(pkg *LifetimePackage) {
	h.t.Helper()

	ctx := context.Background()
	err := wait.NoError(func() error {
		pkgs, err := h.packageSource.LifetimePackages(ctx)
		if err != nil {
			return err
		}
		for _, activePkg := range pkgs {
			if pkg.ChannelPoint == activePkg.ChannelPoint {
				return fmt.Errorf("lifetime package of %v not "+
					"pruned", pkg.ChannelPoint)
			}
		}

		violations := h.packageSource.violations()
		if _, ok := violations[pkg.ChannelPoint]; !ok {
			return fmt.Errorf("lifetime violation for %v not found",
				pkg.ChannelPoint)
		}

		return nil
	}, timeout)
	if err != nil {
		h.t.Fatal(err)
	}
}
