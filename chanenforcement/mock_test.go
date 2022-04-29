package chanenforcement

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/lndclient"
	"github.com/lightningnetwork/lnd/chainntnfs"
)

type mockPackageSource struct {
	PackageSource

	mu                 sync.Mutex
	lifetimePackages   map[wire.OutPoint]LifetimePackage
	lifetimeViolations map[wire.OutPoint]struct{}
}

func newMockPackageSource() *mockPackageSource {
	return &mockPackageSource{
		lifetimePackages:   make(map[wire.OutPoint]LifetimePackage),
		lifetimeViolations: make(map[wire.OutPoint]struct{}),
	}
}

func (s *mockPackageSource) addLifetimePackages(pkgs ...*LifetimePackage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, pkg := range pkgs {
		s.lifetimePackages[pkg.ChannelPoint] = *pkg
	}
}

func (s *mockPackageSource) LifetimePackages() (
	[]*LifetimePackage, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.lifetimePackages) == 0 {
		return nil, nil
	}

	res := make([]*LifetimePackage, 0, len(s.lifetimePackages))
	for _, pkg := range s.lifetimePackages {
		pkg := pkg
		res = append(res, &pkg)
	}

	return res, nil
}

func (s *mockPackageSource) PruneLifetimePackage(pkg *LifetimePackage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.lifetimePackages, pkg.ChannelPoint)
	return nil
}

func (s *mockPackageSource) EnforceLifetimeViolation(pkg *LifetimePackage,
	_ uint32) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.lifetimePackages, pkg.ChannelPoint)
	s.lifetimeViolations[pkg.ChannelPoint] = struct{}{}
	return nil
}

func (s *mockPackageSource) violations() map[wire.OutPoint]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := make(map[wire.OutPoint]struct{}, len(s.lifetimeViolations))
	for k, v := range s.lifetimeViolations {
		m[k] = v
	}

	return m
}

type mockChainNotifier struct {
	lndclient.ChainNotifierClient

	mu         sync.Mutex
	confChans  map[chainhash.Hash]chan *chainntnfs.TxConfirmation
	spendChans map[wire.OutPoint]chan *chainntnfs.SpendDetail

	blockChan chan int32
	errChan   chan error
}

func newMockChainNotifier() *mockChainNotifier {
	return &mockChainNotifier{
		confChans:  make(map[chainhash.Hash]chan *chainntnfs.TxConfirmation),
		spendChans: make(map[wire.OutPoint]chan *chainntnfs.SpendDetail),
		blockChan:  make(chan int32),
		errChan:    make(chan error),
	}
}

func (n *mockChainNotifier) RegisterConfirmationsNtfn(ctx context.Context,
	txid *chainhash.Hash, pkScript []byte, numConfs,
	heightHint int32) (chan *chainntnfs.TxConfirmation, chan error, error) {

	confChan := make(chan *chainntnfs.TxConfirmation)

	n.mu.Lock()
	n.confChans[*txid] = confChan
	n.mu.Unlock()

	return confChan, n.errChan, nil
}

func (n *mockChainNotifier) notifyConf(txid chainhash.Hash,
	conf *chainntnfs.TxConfirmation) error {

	n.mu.Lock()
	confChan, ok := n.confChans[txid]
	n.mu.Unlock()
	if !ok {
		return fmt.Errorf("no active conf notification for %v", txid)
	}

	select {
	case <-time.After(timeout):
		return fmt.Errorf("unable to notify %v conf within timeout", txid)
	case confChan <- conf:
		return nil
	}
}

func (n *mockChainNotifier) RegisterSpendNtfn(ctx context.Context,
	outpoint *wire.OutPoint, pkScript []byte,
	heightHint int32) (chan *chainntnfs.SpendDetail, chan error, error) {

	spendChan := make(chan *chainntnfs.SpendDetail)

	n.mu.Lock()
	n.spendChans[*outpoint] = spendChan
	n.mu.Unlock()

	return spendChan, n.errChan, nil
}

func (n *mockChainNotifier) notifySpend(op wire.OutPoint,
	spend *chainntnfs.SpendDetail) error {

	// Since the spend notification is registered after confirmation, things
	// can be a bit racy, so we'll wait for the spend notification to be
	// registered first.
	timeoutChan := time.After(timeout)
	var spendChan chan *chainntnfs.SpendDetail
loop:
	for {
		select {
		case <-time.After(timeout / 10):
			var ok bool
			n.mu.Lock()
			spendChan, ok = n.spendChans[op]
			n.mu.Unlock()
			if !ok {
				continue
			}

			// Found active spend notification, proceed below.
			break loop

		case <-timeoutChan:
			return fmt.Errorf("no active spend notification for %v",
				op)
		}
	}

	select {
	case <-time.After(timeout):
		return fmt.Errorf("unable to notify %v spend within timeout", op)
	case spendChan <- spend:
		return nil
	}
}

func (n *mockChainNotifier) RegisterBlockEpochNtfn(
	ctx context.Context) (chan int32, chan error, error) {

	// Mimic the actual ChainNotifier by sending a notification upon
	// registration.
	go func() {
		n.blockChan <- 0
	}()

	return n.blockChan, n.errChan, nil
}
