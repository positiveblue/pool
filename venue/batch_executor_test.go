package venue

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/agora/client/clmscript"
	"github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/venue/matching"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

var (
	testTimeout = time.Second * 5
)

type mockBatchStorer struct {
}

func (m *mockBatchStorer) Store(_ context.Context, _ *ExecutionResult) error {
	return nil
}

type mockExecutorStore struct {
	*agoradb.StoreMock

	exeState ExecutionState

	stateTransitions chan ExecutionState
}

func newMockExecutorStore(t *testing.T) *mockExecutorStore {
	return &mockExecutorStore{
		StoreMock:        agoradb.NewStoreMock(t),
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

// executorTestHarness contains several helper functions that drive the main
// test below.
type executorTestHarness struct {
	t *testing.T

	store *mockExecutorStore

	executor *BatchExecutor

	outgoingChans map[matching.AccountID]chan ExecutionMsg

	batchTx *wire.MsgTx
}

func newExecutorTestHarness(t *testing.T, msgTimeout time.Duration) *executorTestHarness {
	store := newMockExecutorStore(t)
	signer := &account.MockSigner{
		PrivKey: batchPriv,
	}

	return &executorTestHarness{
		t:             t,
		store:         store,
		outgoingChans: make(map[matching.AccountID]chan ExecutionMsg),
		executor: NewBatchExecutor(
			store, signer, msgTimeout, &mockBatchStorer{},
		),
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
	}

	err := e.executor.RegisterTrader(activeTrader)
	if err != nil {
		e.t.Fatalf("unable to register trader: %v", err)
	}
}

func (e *executorTestHarness) SubmitBatch(batch *matching.OrderBatch) chan *ExecutionResult {
	respChan, err := e.executor.Submit(
		batch, &order.LinearFeeSchedule{}, chainfee.FeePerKwFloor,
	)
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

func (e *executorTestHarness) ExpectExecutionError(respChan chan *ExecutionResult) error {
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

func (e *executorTestHarness) SendSignMsg(sender *ActiveTrader, invalidSig bool) {
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

	traderKeyTweak := clmscript.TraderKeyTweak(
		batchKey, sender.VenueSecret, traderKey,
	)
	witnessScript, err := clmscript.AccountWitnessScript(
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

	signDesc := &input.SignDescriptor{
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
		SigHashes:  txscript.NewTxSigHashes(batchTx),
	}

	traderSigner := &account.MockSigner{
		PrivKey: senderPriv,
	}
	sigs, err := traderSigner.SignOutputRaw(
		context.Background(), batchTx,
		[]*input.SignDescriptor{signDesc},
	)
	if err != nil {
		e.t.Fatalf("unable to generate sig: %v", err)
	}

	if invalidSig {
		sigs[0][12] ^= 1
	}

	traderSig, err := btcec.ParseDERSignature(sigs[0], btcec.S256())
	if err != nil {
		e.t.Fatalf("unable to parse strader sig: %v", err)
	}

	signMsg := &TraderSignMsg{
		Trader: sender,
		Sigs: map[string]*btcec.Signature{
			string(sender.AccountKey[:]): traderSig,
		},
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

		testCtx.store.Accs = map[[33]byte]*account.Account{
			bigAcct.TraderKeyRaw:   bigAcct,
			smallAcct.TraderKeyRaw: smallAcct,
		}

		// In this test, we'll have two traders which will be a part of
		// the batch: bigAcct, and smallAcct. However, we'll only
		// register an active trader for the bigAcct.
		testCtx.RegisterTrader(onlineTrader)

		// With only one of the accounts registered, we'll attempt to
		// submit a new batch for execution.
		respChan := testCtx.SubmitBatch(orderBatch)

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
	testBatch := *orderBatch

	// First, we'll create our test harness which includes all the
	// functionality we need to carry out our tests.
	msgTimeout := time.Millisecond * 200
	testCtx := newExecutorTestHarness(t, msgTimeout)
	testCtx.Start()

	defer testCtx.Stop()

	// Before we start, we'll also insert some initial master account state
	// that we'll need in order to proceed, and also the on-disk state of
	// the orders.
	testCtx.store.MasterAcct = oldMasterAccount
	testCtx.store.Accs = map[[33]byte]*account.Account{
		bigAcct.TraderKeyRaw:   bigAcct,
		smallAcct.TraderKeyRaw: smallAcct,
	}

	// We'll now register both traders as online so we're able to proceed
	// as expected (though we may delay some messages at a point.
	testCtx.RegisterTrader(smallAcct)
	testCtx.RegisterTrader(bigAcct)

	activeSmallTrader := &ActiveTrader{
		Trader: &smallTrader,
	}
	activeBigTrader := &ActiveTrader{
		Trader: &bigTrader,
	}

	// Next, we'll kick things off by submitting our canned batch.
	respChan := testCtx.SubmitBatch(&testBatch)

	// stepPrepareToSign handles simulating execution up to the
	// BatchSigning phase. If a slow trader is specified, then we'll have
	// them hold back the accept message to trigger our error handling.
	stepPrepareToSign := func(slowTrader *matching.AccountID,
		fastTraders ...*ActiveTrader) {

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

		// Next, we'll send the accept message, but only for the set of
		// fast traders. As a result, this should trip our expiry timer
		// shortly.
		//
		// TODO(roasbeef): also the reject msg codepath
		for i, fastTrader := range fastTraders {
			testCtx.SendAcceptMsg(fastTrader)

			// If this is the last trader, and we don't have a slow
			// trader, then we'll transition to BatchSigning and
			// can exit here.
			if slowTrader == nil && i == len(fastTraders)-1 {
				testCtx.AssertStateTransitions(BatchSigning)
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

			return
		}
	}

	// We'll test the timeout handling of the transition from NoActiveBatch
	// -> PrepareSent -> BatchSigning. We'll have the big trader be the one
	// that fails to send the prepare message.
	stepPrepareToSign(&bigTrader.AccountKey, activeSmallTrader)

	// We'll now re-submit the batch once again, this time, sending all
	// accept messages for each trader, which should transition us to the
	// next phase.
	respChan = testCtx.SubmitBatch(&testBatch)
	stepPrepareToSign(nil, activeSmallTrader, activeBigTrader)

	stepSignToFinalize := func(invalidSig bool, slowTrader *matching.AccountID,
		fastTraders ...*ActiveTrader) {

		// Now that we're entering the BatchSigning phase, we expect
		// that the auctioneer sends the SignBeginMsg to all active
		// traders.
		testCtx.ExpectSignBeginMsgForAll()

		// Similar to the prior state, we expect another finalize no-op
		// as we self-loop waiting for responses.
		testCtx.AssertStateTransitions(BatchSigning)

		// We'll now send all sig messages for all the fast traders.
		for i, fastTrader := range fastTraders {
			testCtx.SendSignMsg(fastTrader, invalidSig)

			// If this is the last trader, and we don't have a slow
			// trader, and all sigs are valid, then we should
			// transition to the finalize phase assuming those were
			// all valid signatures.
			if slowTrader == nil && i == len(fastTraders)-1 &&
				!invalidSig {

				testCtx.AssertStateTransitions(BatchFinalize)
				return
			}

			// Otherwise, we expect a self-loop as we wait in this
			// state until we have all the signatures we need.
			if !invalidSig {
				testCtx.AssertStateTransitions(BatchSigning)
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

		// On the other hand, if we sent an invalid sig above, then we
		// expect that we go to the error state, but emit a distinct
		// error.
		case invalidSig:
			testCtx.AssertStateTransitions(BatchTempError)
			exeErr := testCtx.ExpectExecutionError(respChan)
			testCtx.AssertInvalidWitnessErr(
				exeErr, fastTraders[0].AccountKey,
			)
		}
	}

	// Similar to the scenario above, we'll execute this step once with one
	// trader failing to send their sigs, and then another time with a
	// trader sending an invalid sig before we move forward.
	//
	// First, we'll hold back the OrderMatchSign response for one of the
	// traders, we should fail out with a message time out error.
	stepSignToFinalize(false, &bigTrader.AccountKey, activeSmallTrader)

	// TODO(roasbeef): timer not ticking for some reason

	// Next we'll execute again (from the top), but one of the traders will
	// send an invalid witness, this should cause us to again bail out as
	// we can't proceed with an invalid input.
	respChan = testCtx.SubmitBatch(&testBatch)
	stepPrepareToSign(nil, activeSmallTrader, activeBigTrader)
	stepSignToFinalize(true, nil, activeSmallTrader, activeBigTrader)

	// Both signatures will be invalid, but we'll reset everything after
	// the first invalid signature is found, leaving the second to still be
	// processed. As a result, we expect to transition again to
	// NoActiveBatch as we ignore all messages in the default state.
	testCtx.AssertStateTransitions(NoActiveBatch)

	// Now we'll move forward with no slow traders and no invalid
	// signatures, we should proceed to the finalize phase and end up with
	// a final valid batch.
	respChan = testCtx.SubmitBatch(&testBatch)
	stepPrepareToSign(nil, activeSmallTrader, activeBigTrader)
	stepSignToFinalize(false, nil, activeSmallTrader, activeBigTrader)

	// At this point, all parties should now receive a finalize message,
	// which signals the completion of this batch execution.
	testCtx.ExpectFinalizeMsgForAll()

	// Once the finalize message has been sent, we expect the state machine
	// to terminate at the BatchComplete state.
	testCtx.AssertStateTransitions(BatchComplete)

	// TODO(roasbeef): assert every input of batch transaction valid?
}
