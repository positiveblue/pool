package venue

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/pool/chaninfo"
	"github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/chanbackup"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
)

var (
	testTimeout = time.Second * 5

	_, nodeKey1          = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x1})
	_, nodeKey2          = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x2})
	_, paymentBasePoint1 = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x3})
	_, paymentBasePoint2 = btcec.PrivKeyFromBytes(btcec.S256(), []byte{0x4})
)

type invalidSignAction uint8

const (
	noAction invalidSignAction = iota
	invalidSig
	missingChanInfo
	invalidChanInfo
)

type mockExecutorStore struct {
	*subastadb.StoreMock

	exeState ExecutionState

	stateTransitions chan ExecutionState
}

func newMockExecutorStore(t *testing.T) *mockExecutorStore {
	store := subastadb.NewStoreMock(t)
	store.BatchPubkey = startBatchKey
	return &mockExecutorStore{
		StoreMock:        store,
		stateTransitions: make(chan ExecutionState, 1),
	}
}

func (m *mockExecutorStore) ExecutionState() (ExecutionState, error) {
	return m.exeState, nil
}

func (m *mockExecutorStore) UpdateExecutionState(newState ExecutionState) error {
	m.exeState = newState

	m.stateTransitions <- newState

	return nil
}

// A compile-time assertion to ensure mockExecutorStore meets the ExecutorStore
// interface.
var _ ExecutorStore = (*mockExecutorStore)(nil)

type mockAccountWatcher struct {
	watchedAccounts []*btcec.PublicKey
}

func (m *mockAccountWatcher) WatchMatchedAccounts(_ context.Context,
	accts [][33]byte) error {

	for _, rawAcctKey := range accts {
		acctKey, err := btcec.ParsePubKey(rawAcctKey[:], btcec.S256())
		if err != nil {
			return err
		}

		m.watchedAccounts = append(m.watchedAccounts, acctKey)
	}

	return nil
}

func (m *mockAccountWatcher) isWatching(acctKey *btcec.PublicKey) error {
	for _, watchedAcct := range m.watchedAccounts {
		if watchedAcct.IsEqual(acctKey) {
			return nil
		}
	}

	return fmt.Errorf("account %x is not being watched",
		acctKey.SerializeCompressed())
}

// A compile-time assertion to ensure mockAccountWatcher meets the
// AccountWatcher interface.
var _ AccountWatcher = (*mockAccountWatcher)(nil)

// executorTestHarness contains several helper functions that drive the main
// test below.
type executorTestHarness struct {
	t *testing.T

	store *mockExecutorStore

	executor *BatchExecutor

	watcher *mockAccountWatcher

	outgoingChans map[matching.AccountID]chan ExecutionMsg

	batchTx *wire.MsgTx
}

func newExecutorTestHarness(t *testing.T, msgTimeout time.Duration) *executorTestHarness {
	store := newMockExecutorStore(t)
	signer := &account.MockSigner{
		PrivKey: batchPriv,
	}

	watcher := &mockAccountWatcher{}
	return &executorTestHarness{
		t:             t,
		store:         store,
		outgoingChans: make(map[matching.AccountID]chan ExecutionMsg),
		executor: NewBatchExecutor(&ExecutorConfig{
			Store:            store,
			Signer:           signer,
			BatchStorer:      NewExeBatchStorer(store),
			AccountWatcher:   watcher,
			TraderMsgTimeout: msgTimeout,
		}),
		watcher: watcher,
	}
}

func (e *executorTestHarness) Start() {
	if err := e.executor.Start(); err != nil {
		e.t.Fatalf("unable to start executor: %v", err)
	}
}

func (e *executorTestHarness) Stop() {
	if err := e.executor.Stop(); err != nil {
		e.t.Fatalf("unable to stop executor: %v", err)
	}
}

func (e *executorTestHarness) newTestExecutionContext(
	batch *matching.OrderBatch,
	feeRate chainfee.SatPerKWeight) *batchtx.ExecutionContext {

	e.t.Helper()

	ctxb := context.Background()
	masterAcct, err := e.store.FetchAuctioneerAccount(ctxb)
	if err != nil {
		e.t.Fatal(err)
	}

	exeCtx, err := batchtx.NewExecutionContext(
		startBatchKey, batch, masterAcct, &batchtx.BatchIO{}, feeRate,
		&terms.LinearFeeSchedule{},
	)
	if err != nil {
		e.t.Fatal(err)
	}

	return exeCtx
}

func (e *executorTestHarness) RegisterTrader(acct *account.Account) {
	trader := matching.NewTraderFromAccount(acct)

	outgoingChan := make(chan ExecutionMsg, 1)
	e.outgoingChans[trader.AccountKey] = outgoingChan

	activeTrader := &ActiveTrader{
		Trader: &trader,
		CommLine: &DuplexLine{
			Send: outgoingChan,
			Recv: make(IncomingMsgLine, 1),
		},
		TokenID: randomTokenID(),
	}

	err := e.executor.RegisterTrader(activeTrader)
	if err != nil {
		e.t.Fatalf("unable to register trader: %v", err)
	}
}

func (e *executorTestHarness) SubmitBatch(batch *matching.OrderBatch,
	feeRate chainfee.SatPerKWeight) chan *ExecutionResult {

	e.t.Helper()

	exeCtx := e.newTestExecutionContext(batch, feeRate)
	respChan, err := e.executor.Submit(exeCtx)
	if err != nil {
		e.t.Fatalf("unable to submit batch: %v", err)
	}

	return respChan
}

func (e *executorTestHarness) AssertStateTransitions(states ...ExecutionState) {
	e.t.Helper()

	for _, state := range states {
		select {
		case newState := <-e.store.stateTransitions:

			if newState != state {
				e.t.Fatalf("expected transition to %v, "+
					"instead went to: %v", state, newState)
			}

		case <-time.After(testTimeout):
			e.t.Fatalf("no state transition occurred (wanted %v)", state)
		}
	}
}

func (e *executorTestHarness) AssertNoStateTransition(state ExecutionState) {
	e.t.Helper()

	select {
	case newState := <-e.store.stateTransitions:
		e.t.Fatalf("unexpected transition to %v", newState)

	case <-time.After(5 * time.Millisecond):
	}

	s, err := e.store.ExecutionState()
	if err != nil {
		e.t.Fatal(err)
	}

	if s != state {
		e.t.Fatalf("in unexpected state %v", s)
	}
}

func (e *executorTestHarness) ExpectExecutionSuccess(respChan chan *ExecutionResult) *ExecutionResult {
	e.t.Helper()

	select {
	case resp := <-respChan:
		if resp.Err != nil {
			e.t.Fatalf("execution error: %v", resp.Err)
		}
		return resp

	case <-time.After(testTimeout):
		e.t.Fatalf("no execution result received")
		return nil
	}
}

func (e *executorTestHarness) ExpectExecutionError(respChan chan *ExecutionResult) error {
	e.t.Helper()

	select {
	case resp := <-respChan:

		if resp.Err == nil {
			e.t.Fatalf("no execution error")
		}

		return resp.Err

	case <-time.After(testTimeout):
		e.t.Fatalf("no execution error received")
		return nil
	}
}

func (e *executorTestHarness) ExpectPrepareMsgForAll() {
	e.t.Helper()

	for _, traderOutChan := range e.outgoingChans {
		select {
		case msg := <-traderOutChan:
			prepareMsg, ok := msg.(*PrepareMsg)
			if !ok {
				e.t.Fatalf("expected prepare msg but "+
					"got: %T", msg)
			}

			if e.batchTx != nil {
				continue
			}

			e.batchTx = &wire.MsgTx{}
			err := e.batchTx.Deserialize(
				bytes.NewReader(prepareMsg.BatchTx),
			)
			if err != nil {
				e.t.Fatalf("unable to decode batch tx: %v", err)
			}

		case <-time.After(testTimeout):
			e.t.Fatalf("no prepare msg sent")
		}
	}
}

func (e *executorTestHarness) ExpectSignBeginMsgForAll() {
	for _, traderOutChan := range e.outgoingChans {
		select {
		case msg := <-traderOutChan:
			_, ok := msg.(*SignBeginMsg)
			if !ok {
				e.t.Fatalf("expected sign begin msg but "+
					"got: %T", msg)
			}

		case <-time.After(testTimeout):
			e.t.Fatalf("no sign begin msg sent")
		}
	}
}

func (e *executorTestHarness) ExpectFinalizeMsgForAll() {
	for _, traderOutChan := range e.outgoingChans {
		select {
		case msg := <-traderOutChan:
			msg, ok := msg.(*FinalizeMsg)
			if !ok {
				e.t.Fatalf("expected finalize msg but "+
					"got: %T", msg)
			}

		case <-time.After(testTimeout):
			e.t.Fatalf("no finalize msg sent")
		}
	}
}

func (e *executorTestHarness) ExpectNoMsgs() {
	errChan := make(chan error, len(e.outgoingChans))
	for _, traderOutChan := range e.outgoingChans {
		traderOutChan := traderOutChan

		go func() {
			select {
			case msg := <-traderOutChan:
				errChan <- fmt.Errorf(
					"got unexpected msg: %T", msg)

			case <-time.After(100 * time.Millisecond):
				errChan <- nil
			}
		}()
	}

	for range e.outgoingChans {
		select {
		case err := <-errChan:
			if err != nil {
				e.t.Fatal(err)
			}

		case <-time.After(testTimeout):
			e.t.Fatalf("no result received")
		}
	}
}

func (e *executorTestHarness) SendAcceptMsg(senders ...*ActiveTrader) {
	for _, trader := range senders {
		acceptMsg := &TraderAcceptMsg{
			Trader: trader,
		}

		if err := e.executor.HandleTraderMsg(acceptMsg); err != nil {
			e.t.Fatalf("unable to send msg: %v", err)
		}
	}
}

func (e *executorTestHarness) SendRejectMsg(sender *ActiveTrader,
	rejectType RejectType) {

	var rejectMsg TraderMsg

	switch rejectType {
	case PartialRejectFundingFailed,
		PartialRejectDuplicatePeer:
		rejectMsg = &TraderRejectMsg{
			Trader: sender,
			Type:   rejectType,
			Reason: "mismatch",
		}

	case FullRejectUnknown,
		FullRejectServerMisbehavior,
		FullRejectBatchVersionMismatch:

		rejectMsg = &TraderPartialRejectMsg{
			Trader: sender,
			Orders: map[order.Nonce]*Reject{},
		}
	}

	if err := e.executor.HandleTraderMsg(rejectMsg); err != nil {
		e.t.Fatalf("unable to send msg: %v", err)
	}
}

func (e *executorTestHarness) SendSignMsg(batchCtx *batchtx.ExecutionContext,
	sender *ActiveTrader, action invalidSignAction) {

	senderID := sender.AccountKey
	senderPriv, ok := acctIDToPriv[senderID]
	if !ok {
		e.t.Fatalf("no priv for: %x", senderID)
	}

	traderKey, err := btcec.ParsePubKey(sender.AccountKey[:], btcec.S256())
	if err != nil {
		e.t.Fatalf("unable to parse pubkey: %v", err)
	}
	batchKey, err := btcec.ParsePubKey(sender.BatchKey[:], btcec.S256())
	if err != nil {
		e.t.Fatalf("unable to parse batch key; %v", err)
	}

	batchTx := e.batchTx

	var inputIndex int
	for i, txIn := range batchTx.TxIn {
		if txIn.PreviousOutPoint == sender.AccountOutPoint {
			inputIndex = i
			break
		}
	}

	traderKeyTweak := poolscript.TraderKeyTweak(
		batchKey, sender.VenueSecret, traderKey,
	)
	witnessScript, err := poolscript.AccountWitnessScript(
		sender.AccountExpiry, traderKey, startBatchKey,
		batchKey, sender.VenueSecret,
	)
	if err != nil {
		e.t.Fatalf("unable to gen account witness script: %v", err)
	}
	pkScript, err := input.WitnessScriptHash(witnessScript)
	if err != nil {
		e.t.Fatalf("unable to gen pk script: %v", err)
	}

	signDesc := &lndclient.SignDescriptor{
		KeyDesc: keychain.KeyDescriptor{
			PubKey: traderKey,
		},
		SingleTweak:   traderKeyTweak,
		WitnessScript: witnessScript,
		Output: &wire.TxOut{
			PkScript: pkScript,
			Value:    int64(sender.AccountBalance),
		},
		HashType:   txscript.SigHashAll,
		InputIndex: inputIndex,
	}

	traderSigner := &account.MockSigner{
		PrivKey: senderPriv,
	}
	sigs, err := traderSigner.SignOutputRaw(
		context.Background(), batchTx,
		[]*lndclient.SignDescriptor{signDesc},
	)
	if err != nil {
		e.t.Fatalf("unable to generate sig: %v", err)
	}

	if action == invalidSig {
		sigs[0][12] ^= 1
	}

	traderSig, err := btcec.ParseDERSignature(sigs[0], btcec.S256())
	if err != nil {
		e.t.Fatalf("unable to parse strader sig: %v", err)
	}

	// Prepare the info for each relevant channel to this trader.
	chanOutputs, ok := batchCtx.ChanOutputsForTrader(sender.AccountKey)
	if !ok {
		e.t.Fatalf("no channel outputs found for trader %v",
			sender.AccountKey)
	}
	chanInfos := make(map[wire.OutPoint]*chaninfo.ChannelInfo, len(chanOutputs))
	for _, chanOutputs := range chanOutputs {
		localNodeKey, remoteNodeKey := nodeKey1, nodeKey2
		localPaymentBasePoint, remotePaymentBasePoint :=
			paymentBasePoint1, paymentBasePoint2

		// To provide matching info, it must be flipped for the opposite
		// order.
		if chanOutputs.Order.Type() == order.TypeAsk {
			localNodeKey, remoteNodeKey = remoteNodeKey, localNodeKey
			localPaymentBasePoint, remotePaymentBasePoint =
				remotePaymentBasePoint, localPaymentBasePoint

			// Produce invalid info if required.
			if action == invalidChanInfo {
				localNodeKey, localPaymentBasePoint =
					localPaymentBasePoint, localNodeKey
				remoteNodeKey, remotePaymentBasePoint =
					remotePaymentBasePoint, remoteNodeKey
			}
		}

		// Omit the info if required.
		if action == missingChanInfo {
			continue
		}

		chanInfos[chanOutputs.OutPoint] = &chaninfo.ChannelInfo{
			Version:                chanbackup.AnchorsCommitVersion,
			LocalNodeKey:           localNodeKey,
			RemoteNodeKey:          remoteNodeKey,
			LocalPaymentBasePoint:  localPaymentBasePoint,
			RemotePaymentBasePoint: remotePaymentBasePoint,
		}
	}

	signMsg := &TraderSignMsg{
		Trader: sender,
		Sigs: map[string]*btcec.Signature{
			hex.EncodeToString(sender.AccountKey[:]): traderSig,
		},
		ChannelInfos: chanInfos,
	}

	if err := e.executor.HandleTraderMsg(signMsg); err != nil {
		e.t.Fatalf("unable to handle msg: %v", err)
	}
}

func (e *executorTestHarness) AssertMsgTimeoutErr(err error,
	slowTrader matching.AccountID) {

	traderErr, ok := err.(*ErrMsgTimeout)
	if !ok {
		e.t.Fatalf("expected ErrMsgTimeout instead got: %T", err)
	}

	if traderErr.Trader != slowTrader {
		e.t.Fatalf("%x not found as slow trader",
			slowTrader[:])
	}
}

func (e *executorTestHarness) AssertMsgRejectErr(err error,
	rejectingTrader matching.AccountID) {

	traderErr, ok := err.(*ErrReject)
	if !ok {
		e.t.Fatalf("expected ErrReject instead got: %T", err)
	}

	if _, ok := traderErr.RejectingTraders[rejectingTrader]; !ok {
		e.t.Fatalf("%x not found as rejecting trader",
			rejectingTrader[:])
	}
}

func (e *executorTestHarness) AssertInvalidWitnessErr(err error,
	rougeTrader matching.AccountID) {

	traderErr, ok := err.(*ErrInvalidWitness)
	if !ok {
		e.t.Fatalf("expected ErrInvalidWitness instead got: %T", err)
	}

	if traderErr.Trader != rougeTrader {
		e.t.Fatalf("%x not flagged as sending invalid sigs",
			rougeTrader[:])
	}
}

func (e *executorTestHarness) AssertNonMatchingChannelInfoErr(exeErr error,
	rougeTraders [2]matching.AccountID) {

	err, ok := exeErr.(*ErrNonMatchingChannelInfo)
	if !ok {
		e.t.Fatalf("expected ErrNonMatchingChannelInfo instead got: %T",
			exeErr)
	}

	if rougeTraders[0] != err.Trader1 && rougeTraders[0] != err.Trader2 {
		e.t.Fatalf("%x not flagged as sending non matching channel info",
			rougeTraders[0])
	}
	if rougeTraders[1] != err.Trader1 && rougeTraders[1] != err.Trader2 {
		e.t.Fatalf("%x not flagged as sending non matching channel info",
			rougeTraders[1])
	}
}

func (e *executorTestHarness) AssertMissingChannelInfoErr(exeErr error,
	rougeTrader matching.AccountID) {

	err, ok := exeErr.(*ErrMissingChannelInfo)
	if !ok {
		e.t.Fatalf("expected ErrMissingChannelInfo instead got: %T",
			exeErr)
	}

	if err.Trader != rougeTrader {
		e.t.Fatalf("%x not flagged as missing channel info",
			rougeTrader[:])
	}
}

func (e *executorTestHarness) AssertMissingTradersErr(err error,
	missingTraders ...matching.AccountID) {

	traderErr, ok := err.(*ErrMissingTraders)
	if !ok {
		e.t.Fatalf("expected ErrMissingTraders instead got: %T", err)
	}

	for _, trader := range missingTraders {
		if _, ok := traderErr.TraderKeys[trader]; !ok {
			e.t.Fatalf("%x not found as missing trader",
				trader[:])
		}
	}
}

// TestBatchExecutorOfflineTradersNewBatch tests that any time we need to
// transition the main execution state machine, if a single trader is offline,
// then we'll transition to an error state to kick off that trader and identify
// which orders should be removed from the batch (to be re-done).
func TestBatchExecutorOfflineTradersNewBatch(t *testing.T) {
	offlineTraderScenario := func(offlineTrader, onlineTrader *account.Account) {
		// First, we'll create our test harness which includes all the
		// functionality we need to carry out our tests.
		testCtx := newExecutorTestHarness(t, time.Second*10)
		testCtx.Start()

		defer testCtx.Stop()

		testCtx.store.MasterAcct = oldMasterAccount
		testCtx.store.Accs = map[[33]byte]*account.Account{
			bigAcct.TraderKeyRaw:   bigAcct.Copy(),
			smallAcct.TraderKeyRaw: smallAcct.Copy(),
		}

		// In this test, we'll have two traders which will be a part of
		// the batch: bigAcct, and smallAcct. However, we'll only
		// register an active trader for the bigAcct.
		testCtx.RegisterTrader(onlineTrader)

		// With only one of the accounts registered, we'll attempt to
		// submit a new batch for execution.
		batch := orderBatch.Copy()
		respChan := testCtx.SubmitBatch(&batch, chainfee.FeePerKwFloor)

		// In the state machine itself, we should transition from the
		// NoActiveBatch state to the BatchTempError state.
		testCtx.AssertStateTransitions(BatchTempError)

		// We should receive an error on the ExecutionResult error
		// channel.
		exeErr := testCtx.ExpectExecutionError(respChan)

		// This error specifically should be the ErrMissingTraders
		// error, and it should show that the pubkey of the smallAcct
		// isn't online.
		testCtx.AssertMissingTradersErr(exeErr, offlineTrader.TraderKeyRaw)

		// TODO(roasbeef): assert nonces too?
	}

	// First, we'll have the bidder be offline in our scenario, then switch
	// things up to have the other trader be the only one registered.
	offlineTraderScenario(smallAcct, bigAcct)
	offlineTraderScenario(bigAcct, smallAcct)
}

// TestBatchExecutorNewBatchExecution tests that the batch execution state
// machine is able to properly step through a complete execution scenario. This
// includes backing out and removing any traders that fail to send a message in
// time, or send invalid signatures.
func TestBatchExecutorNewBatchExecution(t *testing.T) {
	// First, we'll create our test harness which includes all the
	// functionality we need to carry out our tests.
	msgTimeout := time.Millisecond * 200
	testCtx := newExecutorTestHarness(t, msgTimeout)
	testCtx.Start()
	defer testCtx.Stop()

	// Before we start, we'll also insert some initial master account state
	// that we'll need in order to proceed, and also the on-disk state of
	// the orders.
	masterAcct := oldMasterAccount
	testCtx.store.MasterAcct = masterAcct
	testCtx.store.Accs = map[[33]byte]*account.Account{
		bigAcct.TraderKeyRaw:   bigAcct.Copy(),
		smallAcct.TraderKeyRaw: smallAcct.Copy(),
	}
	for _, orderPair := range orderBatch.Orders {
		ask := orderPair.Details.Ask
		bid := orderPair.Details.Bid
		testCtx.store.Orders[ask.Nonce()] = ask
		testCtx.store.Orders[bid.Nonce()] = bid
	}

	// We'll now register both traders as online so we're able to proceed
	// as expected (though we may delay some messages at a point).
	testCtx.RegisterTrader(smallAcct)
	testCtx.RegisterTrader(bigAcct)

	activeSmallTrader := &ActiveTrader{
		Trader: &smallTrader,
	}
	activeBigTrader := &ActiveTrader{
		Trader: &bigTrader,
	}

	// We choose a feerate other than the fee floor to ensure everything
	// checks out with a different rate.
	batchFeeRate := 2 * chainfee.FeePerKwFloor

	// Next, we'll kick things off by submitting our canned batch.
	batch := orderBatch.Copy()
	respChan := testCtx.SubmitBatch(&batch, batchFeeRate)

	// stepPrepareToSign handles simulating execution up to the
	// BatchSigning phase. If a slow trader is specified, then we'll have
	// them hold back the accept message to trigger our error handling.
	// If a rejecting trader is specified, the execution will be halted with
	// a reject error.
	stepPrepareToSign := func(slowTrader *matching.AccountID,
		rejectingTrader *ActiveTrader, rejectType *RejectType,
		acceptingTraders ...*ActiveTrader) {

		t.Helper()

		// At this point, both of our active traders should have the
		// prepare message sent to them.
		testCtx.ExpectPrepareMsgForAll()

		// With the prepare message sent, our message timer should
		// start, and we should now transition to the PrepareSent
		// stage. The first transition is for when we first go to the
		// state, and the second will be a no-op as the state machine
		// won't progress further.
		testCtx.AssertStateTransitions(PrepareSent)
		testCtx.AssertStateTransitions(PrepareSent)

		// If we have a full or partially rejecting trader, then we'll
		// assert that we don't immediately transition to an error state
		// because this is just the first message and the executor
		// should wait until all messages arrived.
		if rejectingTrader != nil {
			testCtx.SendRejectMsg(rejectingTrader, *rejectType)

			// The executioner should not yet go into the error
			// state as there are still messages to be received.
			testCtx.AssertStateTransitions(PrepareSent)
		}

		// Next, we'll send the accept message, but only for the set of
		// fast, accepting traders. As a result, this should trip our
		// expiry timer shortly.
		for i, fastTrader := range acceptingTraders {
			testCtx.SendAcceptMsg(fastTrader)

			lastTrader := i == len(acceptingTraders)-1
			goodTradersOnly := slowTrader == nil &&
				rejectingTrader == nil

			// If this isn't the last responding trader, we expect
			// no change in the auction state.
			if !lastTrader {
				testCtx.AssertStateTransitions(PrepareSent)
				continue
			}

			// This was the last of the accepting traders to respond
			// now. Should this result in a success or failure now?
			switch {
			// If this is the last trader, and we don't have any
			// interrupting traders, then we'll transition to
			// BatchSigning and can exit here.
			case goodTradersOnly:
				testCtx.AssertStateTransitions(BatchSigning)
				return

			// If there is a trader that rejected the batch, we now
			// should transition into the temporary error state.
			case rejectingTrader != nil:
				testCtx.AssertStateTransitions(BatchTempError)

				// We should now receive an execution error
				// which singles out that one trader as the one
				// that rejected the batch.
				exeErr := testCtx.ExpectExecutionError(respChan)
				testCtx.AssertMsgRejectErr(
					exeErr, rejectingTrader.AccountKey,
				)
				testCtx.AssertStateTransitions(NoActiveBatch)

				return
			}

			// Otherwise, As we sent this accept message, we should
			// have another transition to PrepareSent as we process
			// their message.
			testCtx.AssertStateTransitions(PrepareSent)
		}

		// If we have a slow trader, then we'll assert that we
		// transitioned to an error state, and the proper error was
		// returned.
		if slowTrader != nil {
			// As only a single trader sent the message, we should
			// now transition to the error state, and expect an
			// error that instructs the caller to re-submit the
			// batch without the problematic trader.
			testCtx.AssertStateTransitions(BatchTempError)

			// We should now receive an execution error which
			// singles out that one trader as the one that didn't
			// send the message in time.
			exeErr := testCtx.ExpectExecutionError(respChan)
			testCtx.AssertMsgTimeoutErr(exeErr, *slowTrader)
			testCtx.AssertStateTransitions(NoActiveBatch)
		}
	}

	// We'll test the timeout handling of the transition from NoActiveBatch
	// -> PrepareSent -> BatchSigning. We'll have the big trader be the one
	// that fails to send the prepare message.
	stepPrepareToSign(&bigTrader.AccountKey, nil, nil, activeSmallTrader)

	// We'll now re-submit the batch once again, this time with a trader
	// that will fully reject the batch.
	batch = orderBatch.Copy()
	respChan = testCtx.SubmitBatch(&batch, batchFeeRate)
	reject := FullRejectBatchVersionMismatch
	stepPrepareToSign(nil, activeBigTrader, &reject, activeSmallTrader)

	// We try yet again but this time with a trader that partially rejects
	// the batch.
	batch = orderBatch.Copy()
	respChan = testCtx.SubmitBatch(&batch, batchFeeRate)
	reject = PartialRejectDuplicatePeer
	stepPrepareToSign(nil, activeBigTrader, &reject, activeSmallTrader)

	// We'll now re-submit the batch once again, this time, sending all
	// accept messages for each trader, which should transition us to the
	// next phase.
	batch = orderBatch.Copy()
	respChan = testCtx.SubmitBatch(&batch, batchFeeRate)
	stepPrepareToSign(nil, nil, nil, activeSmallTrader, activeBigTrader)

	stepSignToFinalize := func(action invalidSignAction,
		slowTrader *matching.AccountID, rejectingTrader *ActiveTrader,
		fastTraders ...*ActiveTrader) {

		t.Helper()

		// Now that we're entering the BatchSigning phase, we expect
		// that the auctioneer sends the SignBeginMsg to all active
		// traders.
		testCtx.ExpectSignBeginMsgForAll()

		// Similar to the prior state, we expect another finalize no-op
		// as we self-loop waiting for responses.
		testCtx.AssertStateTransitions(BatchSigning)

		// If we have a partially rejecting trader, then we'll again
		// assert that we don't immediately transition to an error state
		// because this is just the first message and the executor
		// should wait until all messages arrived.
		if rejectingTrader != nil {
			testCtx.SendRejectMsg(
				rejectingTrader, PartialRejectFundingFailed,
			)

			// The executioner should not yet go into the error
			// state as there are still messages to be received.
			testCtx.AssertStateTransitions(BatchSigning)
		}

		// We'll now send all sig messages for all the fast traders.
		batchCopy := orderBatch.Copy()
		batchCtx := testCtx.newTestExecutionContext(
			&batchCopy, batchFeeRate,
		)
		for i, fastTrader := range fastTraders {
			testCtx.SendSignMsg(
				batchCtx, fastTrader, action,
			)

			lastTrader := i == len(fastTraders)-1
			goodTradersOnly := slowTrader == nil &&
				rejectingTrader == nil

			// If this is the last trader, and we don't have a slow
			// or rejecting trader, and all sigs are valid, then we
			// should transition to the finalize phase assuming
			// those were all valid signatures.
			if goodTradersOnly && lastTrader && action == noAction {
				testCtx.AssertStateTransitions(BatchFinalize)
				return
			}

			// Otherwise, we expect a self-loop as we wait in this
			// state until we have all the signatures we need.
			if action == noAction {
				testCtx.AssertStateTransitions(BatchSigning)
			}

			firstTrader := i == 0
			secondTrader := i == 1

			switch {
			// On the other hand, if we sent an invalid sig above, then we
			// expect that we go to the error state, but emit a distinct
			// error.
			case action == invalidSig:
				// We'll reset everything after the first
				// invalid signature is found, going back to
				// the NoActiveBatch state. As a result, we
				// expect the message to be ignored, since
				// there are no longer any active batch.
				if !firstTrader {
					testCtx.AssertNoStateTransition(NoActiveBatch)
					continue
				}

				testCtx.AssertStateTransitions(BatchTempError)
				exeErr := testCtx.ExpectExecutionError(respChan)
				testCtx.AssertInvalidWitnessErr(
					exeErr, fastTraders[0].AccountKey,
				)
				testCtx.AssertStateTransitions(NoActiveBatch)

			// If we did not provide the required channel info above, then
			// we expect that we go to the error state, but emit a distinct
			// error.
			case action == missingChanInfo:
				// We'll reset everything after the first
				// missing chan info, going back to the
				// NoActiveBatch state. As a result, we expect
				// the message to be ignored, since there are
				// no longer any active batch.
				if !firstTrader {
					testCtx.AssertNoStateTransition(NoActiveBatch)
					continue
				}

				testCtx.AssertStateTransitions(BatchTempError)
				exeErr := testCtx.ExpectExecutionError(respChan)
				testCtx.AssertMissingChannelInfoErr(
					exeErr, fastTraders[0].AccountKey,
				)
				testCtx.AssertStateTransitions(NoActiveBatch)

			// If we did not provide the correct channel info above, then we
			// expect that we go to the error state, but emit a distinct
			// error.
			case action == invalidChanInfo:
				// If this is the first trader, we have no chan
				// info to match with, so we'll stay in the
				// batch siging state.
				if firstTrader {
					testCtx.AssertStateTransitions(BatchSigning)
					continue
				}

				// For other traders than the first and second
				// one we expect the batch to have already
				// failed, so we'll ignore all messages,
				// staying in NoActiveBatch.
				if !secondTrader {
					testCtx.AssertNoStateTransition(NoActiveBatch)
					continue
				}

				// For the second trader we'll notice the
				// invalid chan info and the batch will fail.
				testCtx.AssertStateTransitions(BatchTempError)
				exeErr := testCtx.ExpectExecutionError(respChan)
				testCtx.AssertNonMatchingChannelInfoErr(
					exeErr, [2]matching.AccountID{
						fastTraders[0].AccountKey,
						fastTraders[1].AccountKey,
					},
				)
				testCtx.AssertStateTransitions(NoActiveBatch)
			}
		}

		switch {
		// If we had a slow trader above, then we'll ensure that we go
		// to the expected error state, and also that the slow trader
		// was flagged as the one that didn't send a response.
		case slowTrader != nil:
			testCtx.AssertStateTransitions(BatchTempError)
			exeErr := testCtx.ExpectExecutionError(respChan)
			testCtx.AssertMsgTimeoutErr(exeErr, *slowTrader)
			testCtx.AssertStateTransitions(NoActiveBatch)

		// If we had a rejecting trader, then we'll ensure that we go
		// to the expected error state, and also that the rejecting
		// trader was flagged as the one that didn't accept the batch.
		case rejectingTrader != nil:
			testCtx.AssertStateTransitions(BatchTempError)
			exeErr := testCtx.ExpectExecutionError(respChan)
			testCtx.AssertMsgTimeoutErr(
				exeErr, rejectingTrader.AccountKey,
			)
			testCtx.AssertStateTransitions(NoActiveBatch)
		}
	}

	// Similar to the scenario above, we'll execute this step once with one
	// trader failing to send their sigs, once with a trader rejecting part
	// of the order because of a funding failure, and then another time with
	// a trader sending an invalid sig before we move forward.
	//
	// First, we'll hold back the OrderMatchSign response for one of the
	// traders, we should fail out with a message time out error.
	stepSignToFinalize(
		noAction, &bigTrader.AccountKey, nil, activeSmallTrader,
	)

	// Next, we'll make sure a rejecting trader results in a new match
	// making process.
	batch = orderBatch.Copy()
	respChan = testCtx.SubmitBatch(&batch, batchFeeRate)
	stepPrepareToSign(nil, nil, nil, activeSmallTrader, activeBigTrader)
	stepSignToFinalize(
		noAction, nil, activeBigTrader, activeSmallTrader,
	)

	// TODO(roasbeef): timer not ticking for some reason

	// Next we'll execute again (from the top), but one of the traders will
	// send an invalid witness, this should cause us to again bail out as
	// we can't proceed with an invalid input.
	batch = orderBatch.Copy()
	respChan = testCtx.SubmitBatch(&batch, batchFeeRate)
	stepPrepareToSign(nil, nil, nil, activeSmallTrader, activeBigTrader)
	stepSignToFinalize(
		invalidSig, nil, nil, activeSmallTrader, activeBigTrader,
	)

	// Next we'll execute again (from the top), but one of the traders will
	// send non matching channel info, this should cause us to again bail
	// out as we can't proceed.
	batch = orderBatch.Copy()
	respChan = testCtx.SubmitBatch(&batch, batchFeeRate)
	stepPrepareToSign(nil, nil, nil, activeSmallTrader, activeBigTrader)
	stepSignToFinalize(
		missingChanInfo, nil, nil, activeSmallTrader, activeBigTrader,
	)

	// Next we'll execute again (from the top), but one of the traders will
	// send non matching channel info, this should cause us to again bail
	// out as we can't proceed.
	batch = orderBatch.Copy()
	respChan = testCtx.SubmitBatch(&batch, batchFeeRate)
	stepPrepareToSign(nil, nil, nil, activeSmallTrader, activeBigTrader)
	stepSignToFinalize(
		invalidChanInfo, nil, nil, activeSmallTrader, activeBigTrader,
	)

	// Now we'll move forward with no slow traders and no invalid
	// signatures, we should proceed to the finalize phase and end up with
	// a final valid batch.
	batch = orderBatch.Copy()
	respChan = testCtx.SubmitBatch(&batch, batchFeeRate)
	stepPrepareToSign(nil, nil, nil, activeSmallTrader, activeBigTrader)
	stepSignToFinalize(
		noAction, nil, nil, activeSmallTrader, activeBigTrader,
	)

	// At this point, all parties should now receive a finalize message,
	// which signals the completion of this batch execution.
	testCtx.ExpectFinalizeMsgForAll()

	// Once the finalize message has been sent, we expect the state machine
	// to terminate at the BatchComplete state and a successful execution
	// result to be received.
	testCtx.AssertStateTransitions(BatchComplete)
	exeRes := testCtx.ExpectExecutionSuccess(respChan)
	testCtx.AssertStateTransitions(NoActiveBatch)

	// We'll assert that a lifetime package was created for each matched
	// order.
	if len(exeRes.LifetimePackages) != len(batch.Orders) {
		t.Fatalf("expected %v lifetime pacakges, found %v",
			len(batch.Orders), len(exeRes.LifetimePackages))
	}
	require.Equal(t, exeRes.LifetimePackages, testCtx.store.LifetimePackages)

	// We'll also make sure the account manager has been instructed to start
	// watching the recreated accounts again.
	require.NoError(t, testCtx.watcher.isWatching(acctKeySmall))
	require.NoError(t, testCtx.watcher.isWatching(acctKeyBig))

	// TODO(roasbeef): assert every input of batch transaction valid?
}

func randomTokenID() lsat.TokenID {
	var token lsat.TokenID
	_, _ = rand.Read(token[:])
	return token
}

// TestBatchExecutorEmptyBatch checks that we can execute an empty order batch
// by going straight to batch finalization.
func TestBatchExecutorEmptyBatch(t *testing.T) {
	// First, we'll create our test harness which includes all the
	// functionality we need to carry out our tests.
	msgTimeout := time.Millisecond * 200
	testCtx := newExecutorTestHarness(t, msgTimeout)
	testCtx.Start()
	defer testCtx.Stop()

	// Before we start, we'll also insert some initial master account state
	// that we'll need in order to proceed.
	masterAcct := oldMasterAccount
	testCtx.store.MasterAcct = masterAcct
	testCtx.store.Accs = map[[33]byte]*account.Account{
		bigAcct.TraderKeyRaw:   bigAcct.Copy(),
		smallAcct.TraderKeyRaw: smallAcct.Copy(),
	}

	// We'll register two traders as online. We won't actually include
	// their orders in the batch, but we use them to make sure we are not
	// sending messages unintentionally.
	testCtx.RegisterTrader(smallAcct)
	testCtx.RegisterTrader(bigAcct)

	// Next, we'll kick things off by submitting the empty batch.
	batch := matching.EmptyBatch()
	batchFeeRate := chainfee.FeePerKwFloor
	respChan := testCtx.SubmitBatch(batch, batchFeeRate)

	// We expect no messages to be sent, since no trader is part of the
	// batch.
	testCtx.ExpectNoMsgs()

	// Instead we should go straight to BatchFinalize, then BatchComplete.
	testCtx.AssertStateTransitions(BatchFinalize, BatchComplete)
	exeRes := testCtx.ExpectExecutionSuccess(respChan)

	// Final transaction should have one input, one output.
	if len(exeRes.BatchTx.TxIn) != 1 || len(exeRes.BatchTx.TxOut) != 1 {
		t.Fatalf("expected batch tx to be 1 in 1 out: %v",
			spew.Sdump(exeRes.BatchTx))
	}
}

// TestBatchExecutorIgnoreUnknownTraders runs through a normal, succesfull
// batch execution scenario with two involved traders, while a third
// non-participating trader is sending messages to the executor that should not
// be impacting our state.
func TestBatchExecutorIgnoreUnknownTraders(t *testing.T) {
	// First, we'll create our test harness which includes all the
	// functionality we need to carry out our tests.
	msgTimeout := time.Millisecond * 200
	testCtx := newExecutorTestHarness(t, msgTimeout)
	testCtx.Start()
	defer testCtx.Stop()

	// Before we start, we'll also insert some initial master account state
	// that we'll need in order to proceed, and also the on-disk state of
	// the orders.
	masterAcct := oldMasterAccount
	testCtx.store.MasterAcct = masterAcct
	testCtx.store.Accs = map[[33]byte]*account.Account{
		bigAcct.TraderKeyRaw:   bigAcct.Copy(),
		smallAcct.TraderKeyRaw: smallAcct.Copy(),
	}
	for _, orderPair := range orderBatch.Orders {
		ask := orderPair.Details.Ask
		bid := orderPair.Details.Bid
		testCtx.store.Orders[ask.Nonce()] = ask
		testCtx.store.Orders[bid.Nonce()] = bid
	}

	// We'll now register both traders as online so we're able to proceed
	// as expected.
	testCtx.RegisterTrader(smallAcct)
	testCtx.RegisterTrader(bigAcct)

	activeSmallTrader := &ActiveTrader{
		Trader: &smallTrader,
	}
	activeBigTrader := &ActiveTrader{
		Trader: &bigTrader,
	}
	acceptingTraders := []*ActiveTrader{activeSmallTrader, activeBigTrader}

	// We create a third trader we'll just use to send random messages that
	// should be ignored.
	randomTrader := &ActiveTrader{
		Trader: &medTrader,
	}

	// We start by sending a message to the executor, which should be
	// ignored since there is no active batch.
	testCtx.SendAcceptMsg(randomTrader)
	testCtx.AssertNoStateTransition(NoActiveBatch)

	// Next, we'll kick things off by submitting our canned batch.
	batch := orderBatch.Copy()
	batchFeeRate := 2 * chainfee.FeePerKwFloor
	respChan := testCtx.SubmitBatch(&batch, batchFeeRate)

	// At this point, both of our active traders should have the
	// prepare message sent to them.
	testCtx.ExpectPrepareMsgForAll()

	// With the prepare message sent, our message timer should
	// start, and we should now transition to the PrepareSent
	// stage. The first transition is for when we first go to the
	// state, and the second will be a no-op as the state machine
	// won't progress further.
	testCtx.AssertStateTransitions(PrepareSent)
	testCtx.AssertStateTransitions(PrepareSent)

	// Next, we'll send the accept messages.
	for i, fastTrader := range acceptingTraders {
		// Before sending messages from the participating trader, send
		// a message from the random trader that should be ignored.
		testCtx.SendAcceptMsg(randomTrader)
		testCtx.AssertNoStateTransition(PrepareSent)

		testCtx.SendAcceptMsg(fastTrader)
		lastTrader := i == len(acceptingTraders)-1

		// If this is the last trader, and we don't have any
		// interrupting traders, then we'll transition to
		// BatchSigning and can exit here.
		if lastTrader {
			testCtx.AssertStateTransitions(BatchSigning)
			continue
		}

		// If this isn't the last responding trader, we expect
		// no change in the auction state.
		testCtx.AssertStateTransitions(PrepareSent)
	}

	// Now that we're entering the BatchSigning phase, we expect
	// that the auctioneer sends the SignBeginMsg to all active
	// traders.
	testCtx.ExpectSignBeginMsgForAll()

	// Similar to the prior state, we expect another finalize no-op
	// as we self-loop waiting for responses.
	testCtx.AssertStateTransitions(BatchSigning)

	// We'll now send all sig messages for all the traders.
	batchCopy := orderBatch.Copy()
	batchCtx := testCtx.newTestExecutionContext(
		&batchCopy, batchFeeRate,
	)
	for i, fastTrader := range acceptingTraders {
		// Check that the random trader cannot influence our state.
		signMsg := &TraderSignMsg{
			Trader: randomTrader,
		}
		if err := testCtx.executor.HandleTraderMsg(signMsg); err != nil {
			testCtx.t.Fatalf("unable to handle msg: %v", err)
		}
		testCtx.AssertNoStateTransition(BatchSigning)

		// Send a signature from the paricipating trader.
		testCtx.SendSignMsg(
			batchCtx, fastTrader, noAction,
		)

		lastTrader := i == len(acceptingTraders)-1

		// If this is the last trader, then we should transition to the
		// finalize phase.
		if lastTrader {
			testCtx.AssertStateTransitions(BatchFinalize)
			continue
		}

		// Otherwise, we expect a self-loop as we wait in this
		// state until we have all the signatures we need.
		testCtx.AssertStateTransitions(BatchSigning)
	}

	// At this point, all parties should now receive a finalize message,
	// which signals the completion of this batch execution.
	testCtx.ExpectFinalizeMsgForAll()

	// Once the finalize message has been sent, we expect the state machine
	// to terminate at the BatchComplete state and a successful execution
	// result to be received.
	testCtx.AssertStateTransitions(BatchComplete)
	_ = testCtx.ExpectExecutionSuccess(respChan)
	testCtx.AssertStateTransitions(NoActiveBatch)

	// Now we're back to square one. The random trader is still not
	// welcome.
	testCtx.SendAcceptMsg(randomTrader)
	testCtx.AssertNoStateTransition(NoActiveBatch)
}
