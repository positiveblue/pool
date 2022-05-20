package subasta

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/subasta/internal/test"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

var (
	testHashmailServerAddress = "localhost:8082"
	testSID                   = streamID{1, 2, 3}
	testStreamDesc            = &auctioneerrpc.CipherBoxDesc{
		StreamId: testSID[:],
	}
	testMessage           = []byte("I'm a message!")
	testPublicKeyBytes, _ = hex.DecodeString(testTraderKeyStr)
	defaultStartupWait    = 100 * time.Millisecond
)

func init() {
	logWriter := build.NewRotatingLogWriter()
	SetupLoggers(logWriter, signal.Interceptor{})
	_ = build.ParseAndSetDebugLevels("trace,PRXY=warn", logWriter)
}

func TestHashMailServerReturnStream(t *testing.T) {
	defer test.Guard(t)()

	ctxb := context.Background()
	serverErrChan := make(chan error)

	grpcServer, server := setupHashmailServer(t, serverErrChan)
	defer func() {
		server.Stop()
		grpcServer.Stop()
	}()

	// Any error while starting?
	assertNoError(t, serverErrChan)

	// Create a client and connect it to the server.
	conn, err := grpc.Dial(testHashmailServerAddress, grpc.WithInsecure())
	require.NoError(t, err)
	client := auctioneerrpc.NewHashMailClient(conn)

	defer func() {
		_ = conn.Close()
	}()

	// We'll create a new cipher box that we're going to subscribe to
	// multiple times to check disconnecting returns the read stream.
	resp, err := client.NewCipherBox(ctxb, &auctioneerrpc.CipherBoxAuth{
		Auth: &auctioneerrpc.CipherBoxAuth_AcctAuth{
			AcctAuth: &auctioneerrpc.PoolAccountAuth{
				AcctKey:   testPublicKeyBytes,
				StreamSig: testSignature,
			},
		},
		Desc: testStreamDesc,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetSuccess())

	// First we make sure there is something to read on the other end of
	// that stream by writing something to it.
	sendCtx, sendCancel := context.WithCancel(context.Background())
	defer sendCancel()

	writeStream, err := client.SendStream(sendCtx)
	require.NoError(t, err)
	err = writeStream.Send(&auctioneerrpc.CipherBox{
		Desc: testStreamDesc,
		Msg:  testMessage,
	})
	require.NoError(t, err)

	// Connect, wait for the stream to be ready, read something, then
	// disconnect immediately.
	msg, err := readMsgFromStream(t, client)
	require.NoError(t, err)
	require.Equal(t, testMessage, msg.Msg)

	// Make sure we can connect again immediately and try to read something.
	// There is no message to read before we cancel the request so we expect
	// an EOF error to be returned upon connection close/context cancel.
	_, err = readMsgFromStream(t, client)
	require.Error(t, err)
	require.Contains(t, err.Error(), "context canceled")

	// Send then receive yet another message to make sure the stream is
	// still operational.
	testMessage2 := append(testMessage, []byte("test")...) // nolint
	err = writeStream.Send(&auctioneerrpc.CipherBox{
		Desc: testStreamDesc,
		Msg:  testMessage2,
	})
	require.NoError(t, err)

	msg, err = readMsgFromStream(t, client)
	require.NoError(t, err)
	require.Equal(t, testMessage2, msg.Msg)

	// Clean up the stream now.
	_, err = client.DelCipherBox(ctxb, &auctioneerrpc.CipherBoxAuth{
		Auth: &auctioneerrpc.CipherBoxAuth_AcctAuth{
			AcctAuth: &auctioneerrpc.PoolAccountAuth{
				AcctKey:   testPublicKeyBytes,
				StreamSig: testSignature,
			},
		},
		Desc: testStreamDesc,
	})
	require.NoError(t, err)

	// The server should still be running fine, without any error.
	assertNoError(t, serverErrChan)
}

func TestHashMailServerLargeMessage(t *testing.T) {
	defer test.Guard(t)()

	ctxb := context.Background()
	serverErrChan := make(chan error)

	grpcServer, server := setupHashmailServer(t, serverErrChan)
	defer func() {
		server.Stop()
		grpcServer.Stop()
	}()

	// Any error while starting?
	assertNoError(t, serverErrChan)

	// Create a client and connect it to the server.
	conn, err := grpc.Dial(testHashmailServerAddress, grpc.WithInsecure())
	require.NoError(t, err)
	client := auctioneerrpc.NewHashMailClient(conn)

	defer func() {
		_ = conn.Close()
	}()

	// We'll create a new cipher box that we're going to subscribe to
	// multiple times to check disconnecting returns the read stream.
	resp, err := client.NewCipherBox(ctxb, &auctioneerrpc.CipherBoxAuth{
		Auth: &auctioneerrpc.CipherBoxAuth_AcctAuth{
			AcctAuth: &auctioneerrpc.PoolAccountAuth{
				AcctKey:   testPublicKeyBytes,
				StreamSig: testSignature,
			},
		},
		Desc: testStreamDesc,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetSuccess())

	// Let's create a long message and try to send it.
	var largeMessage [512 * 4096]byte
	_, err = rand.Read(largeMessage[:])
	require.NoError(t, err)

	sendCtx, sendCancel := context.WithCancel(context.Background())
	defer sendCancel()

	writeStream, err := client.SendStream(sendCtx)
	require.NoError(t, err)
	err = writeStream.Send(&auctioneerrpc.CipherBox{
		Desc: testStreamDesc,
		Msg:  largeMessage[:],
	})
	require.NoError(t, err)

	// Connect, wait for the stream to be ready, read something, then
	// disconnect immediately.
	msg, err := readMsgFromStream(t, client)
	require.NoError(t, err)
	require.Equal(t, largeMessage[:], msg.Msg)

	// The server should still be running fine, without any error.
	assertNoError(t, serverErrChan)
}

// TestHashMailServerShutdownWhileRead makes sure the server can be shut down
// even when a client is waiting on a receive stream.
func TestHashMailServerShutdownWhileRead(t *testing.T) {
	defer test.Guard(t)()

	ctxb := context.Background()
	serverErrChan := make(chan error)

	grpcServer, server := setupHashmailServer(t, serverErrChan)
	defer func() {
		// We'll stop the hashmail server in this test so, to clean up
		// we only need to close the gRPC server.
		grpcServer.Stop()
	}()

	// Any error while starting?
	assertNoError(t, serverErrChan)

	// Create a client and connect it to the server.
	conn, err := grpc.Dial(testHashmailServerAddress, grpc.WithInsecure())
	require.NoError(t, err)
	client := auctioneerrpc.NewHashMailClient(conn)

	defer func() {
		_ = conn.Close()
	}()

	// We'll create a new cipher box that we're going to subscribe to
	// multiple times to check disconnecting returns the read stream.
	resp, err := client.NewCipherBox(ctxb, &auctioneerrpc.CipherBoxAuth{
		Auth: &auctioneerrpc.CipherBoxAuth_AcctAuth{
			AcctAuth: &auctioneerrpc.PoolAccountAuth{
				AcctKey:   testPublicKeyBytes,
				StreamSig: testSignature,
			},
		},
		Desc: testStreamDesc,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetSuccess())

	// We now create a client that waits for a message on a receive stream.
	ctxc, cancel := context.WithCancel(context.Background())
	defer cancel()
	readStream, err := client.RecvStream(ctxc, testStreamDesc)
	require.NoError(t, err)

	// Let's wait for the stream to actually be created.
	time.Sleep(defaultStartupWait)

	// Shutting down the server should not block indefinitely, even if a
	// client is connected.
	shutdownComplete := make(chan struct{})
	go func() {
		server.Stop()
		close(shutdownComplete)
	}()

	// We expect the shutdown to happen reasonably quickly.
	select {
	case <-shutdownComplete:

	case <-time.After(2 * defaultStartupWait):
		t.Fatalf("Server did not shut down after %v",
			2*defaultStartupWait)
	}

	_ = readStream.CloseSend()

	// The gRPC server itself should not have been affected and in fact
	// should still be running.
	assertNoError(t, serverErrChan)
}

// TestHashMailServerShutdownWhileWrite makes sure the server can be shut down
// even when a client is waiting on a send stream.
func TestHashMailServerShutdownWhileWrite(t *testing.T) {
	defer test.Guard(t)()

	ctxb := context.Background()
	serverErrChan := make(chan error)

	grpcServer, server := setupHashmailServer(t, serverErrChan)
	defer func() {
		// We'll stop the hashmail server in this test so, to clean up
		// we only need to close the gRPC server.
		grpcServer.Stop()
	}()

	// Any error while starting?
	assertNoError(t, serverErrChan)

	// Create a client and connect it to the server.
	conn, err := grpc.Dial(testHashmailServerAddress, grpc.WithInsecure())
	require.NoError(t, err)
	client := auctioneerrpc.NewHashMailClient(conn)

	defer func() {
		_ = conn.Close()
	}()

	// We'll create a new cipher box that we're going to subscribe to
	// multiple times to check disconnecting returns the read stream.
	resp, err := client.NewCipherBox(ctxb, &auctioneerrpc.CipherBoxAuth{
		Auth: &auctioneerrpc.CipherBoxAuth_AcctAuth{
			AcctAuth: &auctioneerrpc.PoolAccountAuth{
				AcctKey:   testPublicKeyBytes,
				StreamSig: testSignature,
			},
		},
		Desc: testStreamDesc,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetSuccess())

	// Let's create a long message and try to send it.
	var largeMessage [512 * 4096]byte
	_, err = rand.Read(largeMessage[:])
	require.NoError(t, err)

	sendCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writeStream, err := client.SendStream(sendCtx)
	require.NoError(t, err)
	err = writeStream.Send(&auctioneerrpc.CipherBox{
		Desc: testStreamDesc,
		Msg:  largeMessage[:],
	})
	require.NoError(t, err)

	// We need to wait a bit to make sure the message is really sent.
	time.Sleep(defaultStartupWait)

	// Shutting down the server should not block indefinitely, even if a
	// client is connected.
	shutdownComplete := make(chan struct{})
	go func() {
		server.Stop()
		close(shutdownComplete)
	}()

	// We expect the shutdown to happen reasonably quickly.
	select {
	case <-shutdownComplete:

	case <-time.After(2 * defaultStartupWait):
		t.Fatalf("Server did not shut down after %v",
			2*defaultStartupWait)
	}

	_ = writeStream.CloseSend()

	// The gRPC server itself should not have been affected and in fact
	// should still be running.
	assertNoError(t, serverErrChan)
}

func setupHashmailServer(t *testing.T, errChan chan error) (*grpc.Server,
	*hashMailServer) {

	mockSigner := test.NewMockSigner()
	mockSigner.Signature = testSignature
	mockSigner.NodePubkey = testTraderKeyStr
	mockSigner.SignatureMsg = string(testSID[:])

	server := newHashMailServer(hashMailServerConfig{
		IsAccountActive: func(context.Context, *btcec.PublicKey) bool {
			return true
		},
		Signer: mockSigner,
	})
	grpcServer := grpc.NewServer()
	auctioneerrpc.RegisterHashMailServer(grpcServer, server)
	listener, err := net.Listen("tcp", testHashmailServerAddress)
	require.NoError(t, err)
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			errChan <- err
		}
	}()

	return grpcServer, server
}

func readMsgFromStream(t *testing.T,
	client auctioneerrpc.HashMailClient) (*auctioneerrpc.CipherBox, error) {

	ctxc, cancel := context.WithCancel(context.Background())
	readStream, err := client.RecvStream(ctxc, testStreamDesc)
	require.NoError(t, err)

	// Wait a bit again to make sure the request is actually sent before our
	// context is canceled already again.
	time.Sleep(defaultStartupWait)

	// We'll start a read on the stream in the background.
	var (
		goroutineStarted = make(chan struct{})
		resultChan       = make(chan *auctioneerrpc.CipherBox)
		errChan          = make(chan error)
	)
	go func() {
		close(goroutineStarted)
		box, err := readStream.Recv()
		if err != nil {
			errChan <- err
			return
		}
		resultChan <- box
	}()

	// Give the goroutine a chance to actually run, so block the main thread
	// until it did.
	<-goroutineStarted

	time.Sleep(2 * defaultStartupWait)

	// Now close and cancel the stream to make sure the server can clean it
	// up and release it.
	require.NoError(t, readStream.CloseSend())
	cancel()

	// Interpret the result.
	select {
	case err := <-errChan:
		return nil, err

	case box := <-resultChan:
		return box, nil
	}
}

func assertNoError(t *testing.T, errChan chan error) {
	select {
	case err := <-errChan:
		t.Fatalf("error received: %v", err)
	case <-time.After(defaultStartupWait):
	}
}
