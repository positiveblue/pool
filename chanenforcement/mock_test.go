package chanenforcement

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/loop/lndclient"
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

func (s *mockPackageSource) LifetimePackages(context.Context) (
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

func (s *mockPackageSource) PruneLifetimePackage(_ context.Context,
	pkg *LifetimePackage) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.lifetimePackages, pkg.ChannelPoint)
	return nil
}

func (s *mockPackageSource) EnforceLifetimeViolation(ctx context.Context,
	pkg *LifetimePackage, _ uint32) error {

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
	spendChans map[wire.OutPoint]chan *chainntnfs.SpendDetail

	blockChan chan int32
	errChan   chan error
}

func newMockChainNotifier() *mockChainNotifier {
	return &mockChainNotifier{
		spendChans: make(map[wire.OutPoint]chan *chainntnfs.SpendDetail),
		blockChan:  make(chan int32),
		errChan:    make(chan error),
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

	n.mu.Lock()
	spendChan, ok := n.spendChans[op]
	n.mu.Unlock()
	if !ok {
		return fmt.Errorf("no active spend notification for %v", op)
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
