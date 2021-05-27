package subasta

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/lndclient"
	accountT "github.com/lightninglabs/pool/account"
	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/internal/test"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

var (
	testRawAuctioneerKey, _ = hex.DecodeString("02187d1a0e30f4e5016fc1137" +
		"363ee9e7ed5dde1e6c50f367422336df7a108b716")
	testAuctioneerKey, _ = btcec.ParsePubKey(
		testRawAuctioneerKey, btcec.S256(),
	)
	testAuctioneerKeyDesc = &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: account.AuctioneerKeyFamily,
		},
		PubKey: testAuctioneerKey,
	}

	testTraderKeyStr = "036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0a" +
		"f58e0c9395446ba09"
	testRawTraderKey, _ = hex.DecodeString(testTraderKeyStr)
	testTraderKey, _    = btcec.ParsePubKey(testRawTraderKey, btcec.S256())

	testRawTraderKey2, _ = hex.DecodeString("037265ea5016fb5d5e05538e360e" +
		"1d17f557aa9a3aca7431bf78666931d5c8afd7")
	testTraderKey2, _ = btcec.ParsePubKey(testRawTraderKey2, btcec.S256())

	initialBatchKeyBytes, _ = hex.DecodeString("02824d0cbac65e01712124c50" +
		"ff2cc74ce22851d7b444c1bf2ae66afefb8eaf27f")
	initialBatchKey, _ = btcec.ParsePubKey(
		initialBatchKeyBytes, btcec.S256(),
	)

	testTokenID = lsat.TokenID{1, 2, 3}
	testAccount = account.Account{
		TokenID:       testTokenID,
		Value:         1337,
		Expiry:        100,
		AuctioneerKey: testAuctioneerKeyDesc,
		State:         account.StateOpen,
		HeightHint:    100,
		OutPoint:      wire.OutPoint{Index: 1},
		TraderKeyRaw:  toRawKey(testTraderKey),
		BatchKey:      initialBatchKey,
		LatestTx:      wire.NewMsgTx(2),
	}
	testTraderNonce = [32]byte{9, 8, 7, 6}
	testSignature   = []byte{33, 77, 33}
	testReservation = account.Reservation{
		AuctioneerKey:   testAuctioneerKeyDesc,
		InitialBatchKey: initialBatchKey,
		TraderKeyRaw:    toRawKey(testTraderKey2),
		HeightHint:      100,
	}
	defaultTimeout        = 100 * time.Millisecond
	errGenericStreamError = errors.New("an expected error")
)

type mockStream struct {
	grpc.ServerStream
	ctx      context.Context
	toClient chan *auctioneerrpc.ServerAuctionMessage
	toServer chan *auctioneerrpc.ClientAuctionMessage
	recErr   chan error
}

func (s *mockStream) Context() context.Context {
	return s.ctx
}

func (s *mockStream) Send(msg *auctioneerrpc.ServerAuctionMessage) error {
	s.toClient <- msg
	return nil
}

func (s *mockStream) Recv() (*auctioneerrpc.ClientAuctionMessage, error) {
	// Send an error (e.g. abort signal) or a message? Either way, we need
	// to block until there's something to send.
	select {
	case err := <-s.recErr:
		return nil, err

	case msg := <-s.toServer:
		return msg, nil
	}
}

var _ auctioneerrpc.ChannelAuctioneer_SubscribeBatchAuctionServer = (*mockStream)(nil)

// TestRPCServerBatchAuction tests the normal happy flow of a client connecting,
// opening a stream, registering a notification for an account, reading messages
// and then disconnecting.
func TestRPCServerBatchAuction(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	// Start the stream. This will block until either the client disconnects
	// or an error happens, so we'll run it in a goroutine.
	h.start()

	// Let's add an account to our store and register for updates on that
	// account. This is the first message we'll get from new clients after
	// connecting.
	_ = h.mockStore.CompleteReservation(context.Background(), &testAccount)

	// Make sure we can normally complete the subscription handshake for our
	// normal, completed account.
	resp, err := h.testHandshake(testTraderKey)
	require.NoError(t, err)
	require.IsType(
		t, &auctioneerrpc.ServerAuctionMessage_Success{}, resp.Msg,
	)

	// Make sure the trader stream was registered.
	h.assertStreamRegistered(testTokenID)
	comms := h.rpcServer.connectedStreams[testTokenID].comms

	// Simulate a message from the batch executor to the trader and see that
	// it is converted correctly to the gRPC message.
	var acctID matching.AccountID
	copy(acctID[:], testAccount.TraderKeyRaw[:])
	comms.toTrader <- &venue.PrepareMsg{
		ExecutionFee: terms.NewLinearFeeSchedule(1, 100),
	}

	rpcMsg := h.receiveWithTimeout()
	switch {
	case rpcMsg.GetPrepare() != nil:
		// This is what we expected.

	default:
		t.Fatalf("received unexpected message: %v", rpcMsg)
	}

	// Disconnect the trader client and make sure everything is cleaned up
	// nicely and the streams are closed.
	h.assertDisconnect(comms)

	// The stream should close without an error on the server side as the
	// client did an ordinary close.
	h.assertNoError()
}

// TestRPCServerBatchAuctionRecovery tests the recovery flow of a client
// connecting, opening a stream, registering subscription for an account then
// asking for recovery.
func TestRPCServerBatchAuctionRecovery(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	// Start the stream. This will block until either the client disconnects
	// or an error happens, so we'll run it in a goroutine.
	h.start()

	// We'll add a reservation for an account that we later try to recover.
	ctxb := context.Background()
	_ = h.mockStore.ReserveAccount(ctxb, testTokenID, &testReservation)

	// We should get an error for the account where we only have created a
	// reservation.
	resp, err := h.testHandshake(testTraderKey2)
	require.NoError(t, err)
	if resp.GetError() == nil {
		t.Fatalf("server didn't send expected error: %v", resp)
	}
	errCode := resp.GetError().ErrorCode
	if errCode != auctioneerrpc.SubscribeError_INCOMPLETE_ACCOUNT_RESERVATION {
		t.Fatalf("server didn't send expected error code, got %v "+
			"wanted %v", errCode,
			auctioneerrpc.SubscribeError_INCOMPLETE_ACCOUNT_RESERVATION)
	}

	// Let's add an account to our store and register for updates on that
	// account. This is the first message we'll get from new clients after
	// connecting.
	_ = h.mockStore.CompleteReservation(ctxb, &testAccount)

	// Make sure we can normally complete the subscription handshake for our
	// normal, completed account.
	resp, err = h.testHandshake(testTraderKey)
	require.NoError(t, err)
	if resp.GetSuccess() == nil {
		t.Fatalf("server didn't send expected success message")
	}

	// Make sure the trader stream was registered.
	h.assertStreamRegistered(testTokenID)
	comms := h.rpcServer.connectedStreams[testTokenID].comms

	// Simulate a recovery message from the trader now.
	h.mockStream.toServer <- &auctioneerrpc.ClientAuctionMessage{
		Msg: &auctioneerrpc.ClientAuctionMessage_Recover{
			Recover: &auctioneerrpc.AccountRecovery{
				TraderKey: testRawTraderKey,
			},
		},
	}

	rpcMsg := h.receiveWithTimeout()
	switch {
	case rpcMsg.GetAccount() != nil:
		// This is what we expected.

	default:
		t.Fatalf("received unexpected message: %v", rpcMsg)
	}

	// Disconnect the trader client and make sure everything is cleaned up
	// nicely and the streams are closed.
	h.assertDisconnect(comms)

	// The stream should close without an error on the server side as the
	// client did an ordinary close.
	h.assertNoError()
}

// TestRPCServerBatchAuctionStreamError tests the case when an error happens
// during stream operations.
func TestRPCServerBatchAuctionStreamError(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	// Start the stream. This will block until either the client disconnects
	// or an error happens, so we'll run it in a goroutine.
	h.start()

	// Disconnect the trader client and make sure everything is cleaned up
	// nicely and the streams are closed.
	h.mockStream.recErr <- errGenericStreamError
	h.streamWg.Wait()
	if len(h.rpcServer.connectedStreams) != 0 {
		t.Fatalf("stream was not cleaned up after disconnect")
	}

	// The stream should close with the specific error.
	h.assertError(errGenericStreamError.Error())
}

// TestRPCServerBatchAuctionStreamInitialTimeout tests the case when a trader
// connects to the stream but doesn't send a subscription within the timeout.
func TestRPCServerBatchAuctionStreamInitialTimeout(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	// Start the stream. This will block until either the client disconnects
	// or an error happens, so we'll run it in a goroutine.
	h.start()

	// Now just wait and do nothing, we should be disconnected after a
	// while.
	time.Sleep(defaultTimeout * 2)

	// The stream should close without an error on the server side as the
	// client did an ordinary close.
	h.assertError("no subscription received")
}

// TestRPCServerReconnectAfterFailure makes sure that a trader is always
// properly disconnected once the event handler for it exits.
func TestRPCServerReconnectAfterFailure(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	// Start the stream. This will block until either the client disconnects
	// or an error happens, so we'll run it in a goroutine.
	h.start()

	// Let's add an account to our store and register for updates on that
	// account. This is the first message we'll get from new clients after
	// connecting.
	_ = h.mockStore.CompleteReservation(context.Background(), &testAccount)

	// We want the call to `addStreamSubscription` in the RPC server to fail
	// so we can reproduce the problem. This can be done easily by manually
	// adding the trader to the active trader map. Any new subscription will
	// error out and cause the handshake to fail. Whatever happens, the
	// trader should be properly unregistered anyway.
	_ = h.rpcServer.activeTraders.RegisterTrader(&venue.ActiveTrader{
		TokenID: testTokenID,
		Trader: &matching.Trader{
			AccountKey: testAccount.TraderKeyRaw,
		},
	})

	// Make sure we can normally complete the subscription handshake for our
	// normal, completed account.
	_, err := h.testHandshake(testTraderKey)
	require.Error(t, err)

	// TODO(guggero): This is wrong and needs to be fixed!
	// This serves as the demonstration of the bug: Even though the client
	// never fully completed the handshake, the stream is still registered
	// in the connectedStreams map.
	h.assertStreamRegistered(testTokenID)
}

func newServer(store subastadb.Store,
	mockSigner lndclient.SignerClient) *rpcServer {

	activeTraders := &activeTradersMap{
		activeTraders: make(map[matching.AccountID]*venue.ActiveTrader),
	}
	batchExecutor := venue.NewBatchExecutor(&venue.ExecutorConfig{
		Store: &executorStore{
			Store: store,
		},
		Signer:           mockSigner,
		BatchStorer:      venue.NewExeBatchStorer(store),
		TraderMsgTimeout: time.Second * 15,
		ActiveTraders:    activeTraders.GetTraders,
	})

	return newRPCServer(
		store, mockSigner, nil, nil, nil, batchExecutor, nil,
		&terms.AuctioneerTerms{
			OrderExecBaseFee: 1,
			OrderExecFeeRate: 100,
		}, nil, nil, bufconn.Listen(100), bufconn.Listen(100), nil, nil,
		defaultTimeout, activeTraders,
	)
}

type testHarness struct {
	t          *testing.T
	rpcServer  *rpcServer
	authCtx    context.Context
	mockSigner *test.MockSigner
	mockStore  subastadb.Store
	mockStream *mockStream
	streamErr  chan error
	streamWg   sync.WaitGroup
}

func newTestHarness(t *testing.T) *testHarness {
	ctxb := context.Background()
	authCtx := lsat.AddToContext(ctxb, lsat.KeyTokenID, testTokenID)
	mockStore := subastadb.NewStoreMock(t)
	mockSigner := test.NewMockSigner()
	mockSigner.Signature = testSignature
	mockSigner.NodePubkey = testTraderKeyStr

	return &testHarness{
		t:          t,
		rpcServer:  newServer(mockStore, mockSigner),
		authCtx:    authCtx,
		mockSigner: mockSigner,
		mockStore:  mockStore,
		mockStream: &mockStream{
			ctx:      authCtx,
			toClient: make(chan *auctioneerrpc.ServerAuctionMessage),
			toServer: make(chan *auctioneerrpc.ClientAuctionMessage),
			recErr:   make(chan error, 1),
		},
		streamErr: make(chan error, 1),
	}
}

func (h *testHarness) start() {
	h.streamWg.Add(1)
	go func() {
		defer h.streamWg.Done()

		err := h.rpcServer.SubscribeBatchAuction(h.mockStream)
		if err != nil {
			h.t.Logf("Error in subscribe batch auction: %v", err)
			h.streamErr <- err
		}
	}()
}

func (h *testHarness) assertNoError() {
	h.t.Helper()

	select {
	case err := <-h.streamErr:
		h.t.Fatalf("unexpected error in server stream: %v", err)

	default:
	}
}

func (h *testHarness) assertError(expected string) {
	h.t.Helper()

	select {
	case err := <-h.streamErr:
		require.Error(h.t, err)
		require.Contains(h.t, err.Error(), expected)

	default:
		h.t.Fatalf("expected stream to be terminated with error")
	}
}

func (h *testHarness) assertStreamRegistered(tokenID lsat.TokenID) {
	h.t.Helper()

	err := wait.NoError(func() error {
		if len(h.rpcServer.connectedStreams) != 1 {
			return fmt.Errorf("unexpected number of trader "+
				"streams, got %d expected %d",
				len(h.rpcServer.connectedStreams), 1)
		}
		if _, ok := h.rpcServer.connectedStreams[tokenID]; !ok {
			return fmt.Errorf("trader stream for token %v not "+
				"found", tokenID)
		}
		return nil
	}, defaultTimeout)
	require.NoError(h.t, err)
}

func (h *testHarness) assertDisconnect(comms *commChannels) {
	h.t.Helper()

	h.mockStream.recErr <- io.EOF
	h.streamWg.Wait()
	require.Len(h.t, h.rpcServer.connectedStreams, 0)
	_, ok := <-comms.quitConn
	require.False(h.t, ok)
}

func (h *testHarness) receiveWithTimeout() *auctioneerrpc.ServerAuctionMessage {
	select {
	case rpcMsg := <-h.mockStream.toClient:
		return rpcMsg

	case <-time.After(defaultTimeout):
		h.t.Fatalf("trader client stream didn't receive a message")

		return nil
	}
}

// testHandshake completes the default 3-way-handshake and returns the final
// message sent by the auctioneer.
func (h *testHarness) testHandshake(
	traderPubKey *btcec.PublicKey) (*auctioneerrpc.ServerAuctionMessage,
	error) {

	traderKey := toRawKey(traderPubKey)

	// The 3-way handshake begins. The trader sends its commitment.
	var challenge [32]byte
	testCommitHash := accountT.CommitAccount(traderKey, testTraderNonce)
	h.mockStream.toServer <- &auctioneerrpc.ClientAuctionMessage{
		Msg: &auctioneerrpc.ClientAuctionMessage_Commit{
			Commit: &auctioneerrpc.AccountCommitment{
				CommitHash: testCommitHash[:],
			},
		},
	}

	// The server sends the challenge next.
	select {
	case msg := <-h.mockStream.toClient:
		challengeMsg, ok := msg.Msg.(*auctioneerrpc.ServerAuctionMessage_Challenge)
		if !ok {
			h.t.Fatalf("unexpected first message from server: %v",
				msg)
		}
		copy(challenge[:], challengeMsg.Challenge.Challenge)

	case <-time.After(defaultTimeout):
		h.t.Fatalf("server didn't send expected challenge in time")
	}

	// Step 3 of 3 is the trader sending their signature over the auth hash.
	authHash := accountT.AuthHash(testCommitHash, challenge)
	h.mockSigner.SignatureMsg = string(authHash[:])
	h.mockStream.toServer <- &auctioneerrpc.ClientAuctionMessage{
		Msg: &auctioneerrpc.ClientAuctionMessage_Subscribe{
			Subscribe: &auctioneerrpc.AccountSubscription{
				TraderKey:   traderKey[:],
				CommitNonce: testTraderNonce[:],
				AuthSig:     testSignature,
			},
		},
	}

	// The server should find the account and acknowledge the successful
	// subscription or return an error.
	select {
	case msg := <-h.mockStream.toClient:
		return msg, nil

	case err := <-h.streamErr:
		return nil, err
	}
}

func toRawKey(pubkey *btcec.PublicKey) [33]byte {
	var result [33]byte
	copy(result[:], pubkey.SerializeCompressed())
	return result
}
