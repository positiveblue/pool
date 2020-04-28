package agora

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	accountT "github.com/lightninglabs/agora/client/account"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/venue"
	"github.com/lightninglabs/agora/venue/matching"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightninglabs/loop/test"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lntest/wait"
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
		KeyLocator: account.LongTermKeyLocator,
		PubKey:     testAuctioneerKey,
	}

	testTraderKeyStr = "036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21" +
		"a0af58e0c9395446ba09"
	testRawTraderKey, _ = hex.DecodeString(testTraderKeyStr)
	testTokenID         = lsat.TokenID{1, 2, 3}
	testAccount         = account.Account{
		TokenID:       testTokenID,
		Value:         1337,
		Expiry:        100,
		AuctioneerKey: testAuctioneerKeyDesc,
		State:         account.StateOpen,
		HeightHint:    100,
		OutPoint:      wire.OutPoint{Index: 1},
	}
	testTraderNonce       = [32]byte{9, 8, 7, 6}
	testSignature         = []byte{33, 77, 33}
	mockLnd               = test.NewMockLnd()
	defaultTimeout        = 100 * time.Millisecond
	errGenericStreamError = errors.New("an expected error")
)

type mockStream struct {
	grpc.ServerStream
	ctx      context.Context
	toClient chan *clmrpc.ServerAuctionMessage
	toServer chan *clmrpc.ClientAuctionMessage
	recErr   chan error
}

func (s *mockStream) Context() context.Context {
	return s.ctx
}

func (s *mockStream) Send(msg *clmrpc.ServerAuctionMessage) error {
	s.toClient <- msg
	return nil
}

func (s *mockStream) Recv() (*clmrpc.ClientAuctionMessage, error) {
	// Send an error (e.g. abort signal) or a message? Either way, we need
	// to block until there's something to send.
	select {
	case err := <-s.recErr:
		return nil, err

	case msg := <-s.toServer:
		return msg, nil
	}
}

var _ clmrpc.ChannelAuctioneer_SubscribeBatchAuctionServer = (*mockStream)(nil)

// TestRPCServerBatchAuction tests the normal happy flow of a client connecting,
// opening a stream, registering a notification for an account, reading messages
// and then disconnecting.
func TestRPCServerBatchAuction(t *testing.T) {
	var (
		authCtx = auth.AddToContext(
			context.Background(), auth.KeyTokenID, testTokenID,
		)
		mockStore  = agoradb.NewStoreMock(t)
		rpcServer  = newServer(mockStore)
		mockStream = &mockStream{
			ctx:      authCtx,
			toClient: make(chan *clmrpc.ServerAuctionMessage),
			toServer: make(chan *clmrpc.ClientAuctionMessage),
			recErr:   make(chan error, 1),
		}
		streamErr = make(chan error)
		streamWg  sync.WaitGroup
	)
	mockLnd.Signature = testSignature
	mockLnd.NodePubkey = testTraderKeyStr

	// Start the stream. This will block until either the client disconnects
	// or an error happens, so we'll run it in a goroutine.
	streamWg.Add(1)
	go func() {
		defer streamWg.Done()

		err := rpcServer.SubscribeBatchAuction(mockStream)
		if err != nil {
			t.Logf("Error in subscribe batch auction: %v", err)
			streamErr <- err
		}
	}()

	// Let's add an account to our store and register for updates on that
	// account. This is the first message we'll get from new clients after
	// connecting.
	_ = mockStore.CompleteReservation(context.Background(), &testAccount)

	// The 3-way handshake begins. The trader sends its commitment.
	var (
		traderKey [33]byte
		challenge [32]byte
	)
	copy(traderKey[:], testRawTraderKey)
	testCommitHash := accountT.CommitAccount(traderKey, testTraderNonce)
	mockStream.toServer <- &clmrpc.ClientAuctionMessage{
		Msg: &clmrpc.ClientAuctionMessage_Commit{
			Commit: &clmrpc.AccountCommitment{
				CommitHash: testCommitHash[:],
			},
		},
	}

	// The server sends the challenge next.
	select {
	case msg := <-mockStream.toClient:
		challengeMsg, ok := msg.Msg.(*clmrpc.ServerAuctionMessage_Challenge)
		if !ok {
			t.Fatalf("unexpected first message from server: %v", msg)
		}
		copy(challenge[:], challengeMsg.Challenge.Challenge)

	case <-time.After(defaultTimeout):
		t.Fatalf("server didn't send expected challenge in time")
	}

	// Step 3 of 3 is the trader sending their signature over the auth hash.
	authHash := accountT.AuthHash(testCommitHash, challenge)
	mockLnd.SignatureMsg = string(authHash[:])
	mockStream.toServer <- &clmrpc.ClientAuctionMessage{
		Msg: &clmrpc.ClientAuctionMessage_Subscribe{
			Subscribe: &clmrpc.AccountSubscription{
				UserSubKey:  testRawTraderKey,
				CommitNonce: testTraderNonce[:],
				AuthSig:     testSignature,
			},
		},
	}

	// Make sure the trader stream was registered.
	err := wait.NoError(func() error {
		if len(rpcServer.connectedStreams) != 1 {
			return fmt.Errorf("unexpected number of trader "+
				"streams, got %d expected %d",
				len(rpcServer.connectedStreams), 1)
		}
		if _, ok := rpcServer.connectedStreams[testTokenID]; !ok {
			return fmt.Errorf("trader stream for token %v not "+
				"found", testTokenID)
		}
		return nil
	}, defaultTimeout)
	if err != nil {
		t.Fatalf("trader stream was not registered before timeout: %v",
			err)
	}
	comms := rpcServer.connectedStreams[testTokenID].comms

	// Simulate a message from the batch executor to the trader and see that
	// it is converted correctly to the gRPC message.
	var acctID matching.AccountID
	copy(acctID[:], testAccount.TraderKeyRaw[:])
	comms.toTrader <- &venue.PrepareMsg{
		AcctKey:      acctID,
		ExecutionFee: order.NewLinearFeeSchedule(1, 100),
	}
	select {
	case rpcMsg := <-mockStream.toClient:
		switch typedMsg := rpcMsg.Msg.(type) {
		case *clmrpc.ServerAuctionMessage_Prepare:
			// This is what we expected.

		default:
			t.Fatalf("received unexpected message: %v", typedMsg)
		}

	case <-time.After(defaultTimeout):
		t.Fatalf("trader client stream didn't receive a message")
	}

	// Disconnect the trader client and make sure everything is cleaned up
	// nicely and the streams are closed.
	mockStream.recErr <- io.EOF
	streamWg.Wait()
	if len(rpcServer.connectedStreams) != 0 {
		t.Fatalf("stream was not cleaned up after disconnect")
	}
	_, ok := <-comms.quitConn
	if ok {
		t.Fatalf("expected abort channel to be closed")
	}

	// The stream should close without an error on the server side as the
	// client did an ordinary close.
	select {
	case err := <-streamErr:
		t.Fatalf("unexpected error in server stream: %v", err)

	default:
	}
}

// TestRPCServerBatchAuctionStreamError tests the case when an error happens
// during stream operations.
func TestRPCServerBatchAuctionStreamError(t *testing.T) {
	var (
		authCtx = auth.AddToContext(
			context.Background(), auth.KeyTokenID, testTokenID,
		)
		mockStore  = agoradb.NewStoreMock(t)
		rpcServer  = newServer(mockStore)
		mockStream = &mockStream{
			ctx:      authCtx,
			toClient: make(chan *clmrpc.ServerAuctionMessage),
			toServer: make(chan *clmrpc.ClientAuctionMessage),
			recErr:   make(chan error),
		}
		streamErr = make(chan error, 1)
		streamWg  sync.WaitGroup
	)

	// Start the stream. This will block until either the client disconnects
	// or an error happens, so we'll run it in a goroutine.
	streamWg.Add(1)
	go func() {
		defer streamWg.Done()

		err := rpcServer.SubscribeBatchAuction(mockStream)
		if err != nil {
			streamErr <- err
		}
	}()

	// Disconnect the trader client and make sure everything is cleaned up
	// nicely and the streams are closed.
	mockStream.recErr <- errGenericStreamError
	streamWg.Wait()
	if len(rpcServer.connectedStreams) != 0 {
		t.Fatalf("stream was not cleaned up after disconnect")
	}

	// The stream should close with the specific error.
	select {
	case err := <-streamErr:
		if !strings.Contains(err.Error(), errGenericStreamError.Error()) {
			t.Fatalf("unexpected error in server stream: %v", err)
		}

	default:
		t.Fatalf("expected stream to be terminated with error")
	}
}

// TestRPCServerBatchAuctionStreamInitialTimeout tests the case when a trader
// connects to the stream but doesn't send a subscription within the timeout.
func TestRPCServerBatchAuctionStreamInitialTimeout(t *testing.T) {
	var (
		authCtx = auth.AddToContext(
			context.Background(), auth.KeyTokenID, testTokenID,
		)
		mockStore  = agoradb.NewStoreMock(t)
		rpcServer  = newServer(mockStore)
		mockStream = &mockStream{
			ctx:      authCtx,
			toClient: make(chan *clmrpc.ServerAuctionMessage),
			toServer: make(chan *clmrpc.ClientAuctionMessage),
			recErr:   make(chan error),
		}
		streamErr = make(chan error, 1)
		streamWg  sync.WaitGroup
	)

	// Start the stream. This will block until either the client disconnects
	// or an error happens, so we'll run it in a goroutine.
	streamWg.Add(1)
	go func() {
		defer streamWg.Done()

		err := rpcServer.SubscribeBatchAuction(mockStream)
		if err != nil {
			streamErr <- err
		}
	}()

	// Now just wait and do nothing, we should be disconnected after a
	// while.
	time.Sleep(defaultTimeout * 2)

	// The stream should close without an error on the server side as the
	// client did an ordinary close.
	select {
	case err := <-streamErr:
		if !strings.Contains(err.Error(), "no subscription received") {
			t.Fatalf("unexpected error in server stream: %v", err)
		}

	default:
		t.Fatalf("unexpected stream to be terminated with error")
	}
}

func newServer(store agoradb.Store) *rpcServer {
	lndServices := &lndclient.GrpcLndServices{
		LndServices: lndclient.LndServices{
			Client:        mockLnd.Client,
			WalletKit:     mockLnd.WalletKit,
			ChainNotifier: mockLnd.ChainNotifier,
			Signer:        mockLnd.Signer,
			Invoices:      mockLnd.Invoices,
			Router:        mockLnd.Router,
			ChainParams:   mockLnd.ChainParams,
		},
	}

	batchExecutor := venue.NewBatchExecutor(
		&executorStore{
			Store: store,
		},
		lndServices.Signer, time.Second*15, venue.NewExeBatchStorer(store),
	)

	return newRPCServer(
		store, lndServices, nil, nil, nil, batchExecutor,
		order.NewLinearFeeSchedule(1, 100),
		bufconn.Listen(100), nil, defaultTimeout,
	)
}

func init() {
	copy(testAccount.TraderKeyRaw[:], testRawTraderKey)
}
