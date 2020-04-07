package venue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/venue/batchtx"
	"github.com/lightninglabs/agora/venue/matching"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// ExecutionMsg...
type ExecutionMsg interface {
	// Dest...
	Dest() matching.AccountID
}

type PrepareMsg struct {
	AcctKey matching.AccountID
}

func (m *PrepareMsg) Dest() matching.AccountID {
	return m.AcctKey
}

type FinalizeMsg struct {
	AcctKey matching.AccountID
}

func (m *FinalizeMsg) Dest() matching.AccountID {
	return m.AcctKey
}

// TraderMsg...
type TraderMsg interface {
	// Batch returns the batch ID.
	Batch() []byte
}

// TraderAcceptMsg is the message a Trader sends to accept the execution of a
// batch.
type TraderAcceptMsg struct {
	BatchID []byte
	Trader  *ActiveTrader
	Orders  []order.Nonce
}

func (m *TraderAcceptMsg) Batch() []byte {
	return m.BatchID
}

// TraderRejectMsg is the message a Trader sends to reject the execution of a
// batch.
type TraderRejectMsg struct {
	BatchID []byte
	Trader  *ActiveTrader
	Reason  string
}

func (m *TraderRejectMsg) Batch() []byte {
	return m.BatchID
}

// TraderSignMsg is the message a Trader sends when they have signed all account
// inputs of a batch transaction.
type TraderSignMsg struct {
	BatchID []byte
	Trader  *ActiveTrader
	Sigs    map[string][][]byte
}

func (m *TraderSignMsg) Batch() []byte {
	return m.BatchID
}

// ExecutionResult..
type ExecutionResult struct {
	// Err...
	Err error

	// BatchID...
	BatchID order.BatchID

	// Batch...
	Batch *matching.OrderBatch

	// MasterAccountDiff...
	MasterAccountDiff *batchtx.MasterAccountState

	// BatchTx
	BatchTx *wire.MsgTx

	// StartingFeeRate
	StartingFeeRate chainfee.SatPerKWeight

	// ExecutionResult...
	ExecutionFee btcutil.Amount

	// TODO(roasbeef): other stats?
	//  * coin blocks created (lol, like BDD)
	//  * total fees paid to makers
	//  * clearing rate
	//  * transaction size
	//  * total amount of BTC cleared

	// TODO(roasbeef): final batch account state?
}

// executionReq..
type executionReq struct {
	*matching.OrderBatch

	// Result..
	//
	// TODO(roasbeef): just make part of the resp?
	//
	// NOTE: This chan MUST be buffered.
	Result chan *ExecutionResult
}

// BatchExecutor...
type BatchExecutor struct {
	started uint32 // To be used atomically.
	stopped uint32 // To be used atomically.

	// activeTraders...
	activeTraders map[matching.AccountID]*ActiveTrader

	newBatches chan *executionReq

	sync.RWMutex

	store       agoradb.Store
	batchStorer BatchStorer

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewBatchExecutor...
func NewBatchExecutor(store agoradb.Store) (*BatchExecutor, error) {
	return &BatchExecutor{
		quit:          make(chan struct{}),
		activeTraders: make(map[matching.AccountID]*ActiveTrader),
		newBatches:    make(chan *executionReq),
		store:         store,
		batchStorer: &batchStorer{
			store: store,
		},
	}, nil
}

// Start...
func (b *BatchExecutor) Start() error {
	if !atomic.CompareAndSwapUint32(&b.started, 0, 1) {
		return nil
	}

	b.wg.Add(1)
	go b.executor()

	return nil
}

// Stop...
func (b *BatchExecutor) Stop() error {
	if !atomic.CompareAndSwapUint32(&b.stopped, 0, 1) {
		return nil
	}

	close(b.quit)

	b.wg.Wait()

	return nil
}

// validateBatch...
//
// TODO(roasbeef): don't need anymore?
func (b *BatchExecutor) validateBatch(batch *matching.OrderBatch) error { // nolint:unparam
	// ensure orders/asks match up

	// ensure valid transition (take into account fee etc)

	// TODO(roasbeef): Replace with real logic.
	for _, o := range batch.Orders {
		trader, ok := b.activeTraders[o.Asker.AccountKey]
		if !ok {
			continue
		}
		trader.CommLine.Send <- &PrepareMsg{
			AcctKey: trader.AccountKey,
		}
	}

	return nil
}

func (b *BatchExecutor) executor() {
	defer b.wg.Done()

	// TODO(roasbeef): active traders local?

	// TODO(roasbeef): server steps
	//  * get in msg that new batch is accepted
	//     * validate batch
	//  * construct the batch transaction, do sanity checks against
	//  * send OrderMatchPrepare to clients
	//  * wait for OrderMatchAccept
	//    * if not all traders in batch send error wait for next
	//  * enter into signing phase
	//  * wait for all clients to sned OrderMatchSign..
	//  * verify all witnesses are valid for batch execution transaction
	//  * send order match finalize

	// TODO(roasbeef): client stgeps
	//  * sit there, look cute
	//  * recv OrderMatchPrepare
	//  * validate (initial step no validation, just want batch signing, e2e)
	//  * locate funding output and batch details in preparte message
	//  * send OrderMatchAccept to the server
	//  * if taker: register funding shim based on result (do ahead of time
	//  for each order to avoid race condition issues?)
	//  * if maker: intiate funding w/ pending chan id of order ID
	//  * wait until both sides pending chan funding
	//  * if maker: sign inputs and send OrderMatchSign
	//  * if taker: sign inputs and send OrderMatchSign
	//  * both: wait for OrderMatchFinalize

	for {
		select {
		case newBatch := <-b.newBatches:

			// TODO(guggero): Use timeout and/or cancel?
			batchCtx := context.Background()

			// The batch ID is always the currently stored ID. We
			// can try to match the same batch multiple times with
			// traders bailing out and us trying again. But we
			// always need to use the same ID. Only after clearing
			// the batch, the ID is increased and stored as the ID
			// to use for the next batch.
			batchKey, err := b.store.BatchKey(batchCtx)
			if err != nil {
				log.Errorf("Nope, couldn't get batch key!")

				newBatch.Result <- &ExecutionResult{
					Err: err,
				}

				continue
			}
			var batchID order.BatchID
			copy(batchID[:], batchKey.SerializeCompressed())

			err = b.validateBatch(newBatch.OrderBatch)
			if err != nil {
				log.Errorf("Nope, batch didn't validate!")

				newBatch.Result <- &ExecutionResult{
					Err: err,
				}

				continue
			}

			result := &ExecutionResult{
				Batch:   newBatch.OrderBatch,
				BatchID: batchID,
			}

			// TODO(roasbeef): move to correct place.
			err = b.batchStorer.Store(batchCtx, result)
			if err != nil {
				log.Errorf("Nope, batch couldn't be persisted!")

				newBatch.Result <- &ExecutionResult{
					Err: err,
				}

				continue
			}

			// TODO(roasbeef): fail if all
		case <-b.quit:
			return
		}
	}
}

// Submit...
func (b *BatchExecutor) Submit(batch *matching.OrderBatch) (chan *ExecutionResult, error) {
	b.newBatches <- &executionReq{
		OrderBatch: batch,
		Result:     make(chan *ExecutionResult, 1),
	}
	return nil, nil
}

// RegisterTrader...
func (b *BatchExecutor) RegisterTrader(t *ActiveTrader) error {
	b.Lock()
	defer b.Unlock()

	_, ok := b.activeTraders[t.AccountKey]
	if ok {
		return fmt.Errorf("trader %x already registered",
			t.AccountKey)
	}
	b.activeTraders[t.AccountKey] = t

	return nil
}

// UnregisterTrader...
func (b *BatchExecutor) UnregisterTrader(t *ActiveTrader) error {
	b.Lock()
	defer b.Unlock()

	delete(b.activeTraders, t.AccountKey)

	return nil
}

func (b *BatchExecutor) HandleTraderMsg(m TraderMsg) error {
	// TODO(roasbeef): Handle the different messages from the Trader.
	switch msg := m.(type) {
	case *TraderAcceptMsg:
		log.Debugf("The Trader %x accepted: %v", msg.Trader.AccountKey,
			msg)

	case *TraderRejectMsg:
		log.Debugf("The Trader %x rejected: %v", msg.Trader.AccountKey,
			msg)

	case *TraderSignMsg:
		log.Debugf("The Trader %x signed: %v", msg.Trader.AccountKey,
			msg)
		msg.Trader.CommLine.Send <- &FinalizeMsg{
			AcctKey: msg.Trader.AccountKey,
		}
		log.Trace("Sent finalize message to comm line.")

	}

	return nil
}
