package itest

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// testHashMailServer tests that the hash mail server implements a non-blocking
// write, yet blocking read functionality to implement the desired network pipe
// functionality.
func testHashMailServer(t *harnessTest) {
	ctx := context.Background()

	// Before we start, we'll need to make a fresh account, as we require a
	// valid account (for one of the authentication routes) to create a
	// stream.
	//
	// TODO(roasbeef): create expired account as well?
	aliceNode := t.lndHarness.NewNode(t.t, "alice", lndDefaultArgs)

	// Fund the wallet to be able to open an account of the default size.
	t.lndHarness.SendCoins(t.t, 5_000_000, aliceNode)

	aliceTrader := setupTraderHarness(
		t.t, t.lndHarness.BackendCfg, aliceNode, t.auctioneer,
	)
	defer shutdownAndAssert(t, aliceNode, aliceTrader)

	aliceAcct := openAccountAndAssert(
		t, aliceTrader, &poolrpc.InitAccountRequest{
			AccountValue: defaultAccountValue,
			AccountExpiry: &poolrpc.InitAccountRequest_RelativeHeight{
				RelativeHeight: 1_000,
			},
		},
	)

	mailClient, err := t.NewHashMailClient()
	if err != nil {
		t.Fatalf("unable to make client: %v", err)
	}

	var streamID [64]byte
	if _, err := rand.Read(streamID[:]); err != nil {
		t.Fatalf("unable to read stream ID: %v", err)
	}

	// Now that we have an account, we'll use it to test out stream account
	// creation.
	t.t.Run("acct stream creation (fail)", func(tt *testing.T) {
		// First, we'll verify that account creation fails if an
		// account doesn't really exist.
		privKey, err := btcec.NewPrivateKey(btcec.S256())
		if err != nil {
			tt.Fatalf("unable to make priv key: %v", err)
		}
		pubKey := privKey.PubKey()

		streamInit := &auctioneerrpc.CipherBoxAuth{
			Desc: &auctioneerrpc.CipherBoxDesc{
				StreamId: streamID[:],
			},
			Auth: &auctioneerrpc.CipherBoxAuth_AcctAuth{
				AcctAuth: &auctioneerrpc.PoolAccountAuth{
					AcctKey:   pubKey.SerializeCompressed(),
					StreamSig: []byte{},
				},
			},
		}
		_, err = mailClient.NewCipherBox(ctx, streamInit)
		if err == nil {
			tt.Fatalf("account creation passed")
		}
	})

	// Next, we'll ensure that we're able to create a stream using a valid
	// account auth.
	var streamAuth *auctioneerrpc.CipherBoxAuth
	t.t.Run("acct stream creation (pass)", func(tt *testing.T) {
		// In order to create a valid authentication mode, we'll first
		// sign the stream ID we created above.
		streamSig, err := aliceNode.SignerClient.SignMessage(
			ctx, &signrpc.SignMessageReq{
				Msg: streamID[:],
				KeyLoc: &signrpc.KeyLocator{
					KeyFamily: int32(poolscript.AccountKeyFamily),
					KeyIndex:  0,
				},
			},
		)
		require.Nil(tt, err)

		// We'll now attempt to initialize a new stream, this should
		// pass validation as expected.
		acctKey := aliceAcct.TraderKey
		streamAuth = &auctioneerrpc.CipherBoxAuth{
			Desc: &auctioneerrpc.CipherBoxDesc{
				StreamId: streamID[:],
			},
			Auth: &auctioneerrpc.CipherBoxAuth_AcctAuth{
				AcctAuth: &auctioneerrpc.PoolAccountAuth{
					AcctKey:   acctKey,
					StreamSig: streamSig.Signature,
				},
			},
		}
		streamDesc, err := mailClient.NewCipherBox(ctx, streamAuth)
		require.Nil(tt, err, "stream creation failed: %v", err)

		// The response should be the success variant.
		require.NotNil(tt, streamDesc.GetSuccess(), "success field nil")

		// If we try to create the stream again, then we should get an
		// error that it already exists.
		_, err = mailClient.NewCipherBox(ctx, streamAuth)
		require.NotNil(tt, err, "stream should have failed")

		// Make sure we get the precise error we expect.
		statusCode, ok := status.FromError(err)
		if !ok {
			tt.Fatalf("expected status error")
		}
		require.Equal(tt, statusCode.Code(), codes.AlreadyExists)
	})

	// Now that we have the stream we'll attempt our first read and write
	// attempt.
	t.t.Run("acct stream read/write", func(tt *testing.T) {
		// First, we'll obtain our read stream, if we try to obtain one
		// twice, then we should get an error the second time.
		streamDesc := &auctioneerrpc.CipherBoxDesc{
			StreamId: streamID[:],
		}
		ctxC, cancel := context.WithCancel(ctx)
		readStream, err := mailClient.RecvStream(ctxC, streamDesc)
		require.Nil(tt, err, "unable to obtain read stream: %v", err)

		// Give the server a chance to read out the message.
		time.Sleep(time.Millisecond * 100)

		// If we obtain a second stream and attempt to read a message,
		// then we should receive the error.
		readStream2, err := mailClient.RecvStream(ctx, streamDesc)
		require.Nil(tt, err, "unable to obtain read stream: %v", err)
		_, err = readStream2.Recv()
		require.NotNil(tt, err, "read should should have failed")

		// Next we'll obtain our write stream so we can test the
		// read/write functionality.
		writeStream, err := mailClient.SendStream(ctx)
		require.Nil(tt, err, "unable to obtain write stream: %v", err)

		// Now that we have our stream, we'll first test the non-blocking
		// write property. We should be able to write to the stream, close
		// it, then read out the other end.
		msg := []byte("hello")
		err = writeStream.Send(&auctioneerrpc.CipherBox{
			Desc: streamDesc,
			Msg:  msg,
		})
		require.Nil(tt, err, "unable to write to stream: %v", err)

		// Give the server a chance to read out the message.
		time.Sleep(time.Millisecond * 100)

		// If we try to obtain another write stream at this point, then
		// we should error out as one is already occupied.
		writeStream2, err := mailClient.SendStream(ctx)
		require.Nil(tt, err, "unable to obtain write stream: %v", err)
		err = writeStream2.Send(&auctioneerrpc.CipherBox{
			Desc: streamDesc,
			Msg:  []byte("won't be written"),
		})
		require.Nil(tt, err, "empty write should proceed")
		_, err = writeStream2.CloseAndRecv()
		require.NotNil(tt, err, "write should've failed")
		_ = writeStream2.CloseSend()

		// We'll now close out the write stream to simulate a user
		// hanging up, then attempt to read what was written from the
		// other end.
		_, _ = writeStream.CloseAndRecv()

		// The message written above should match what we read here now
		// below.
		readMsg, err := readStream.Recv()
		require.Nil(tt, err, "unable to read msg: %v", err)
		require.Equal(tt, msg, readMsg.Msg)

		// Hang up the stream on the client-side.
		cancel()
	})

	// Now we test stream deletion to ensure that after stream deletion the
	// stream is garbage collected and unusable.
	t.t.Run("stream del", func(tt *testing.T) {
		// We should be able to tear down a stream using the proper
		// authentication mechanism.
		_, err := mailClient.DelCipherBox(ctx, streamAuth)
		require.NoError(tt, err)

		// At this point, if we try to read from the stream again, we
		// should get an error.
		streamDesc := &auctioneerrpc.CipherBoxDesc{
			StreamId: streamID[:],
		}
		readStream, err := mailClient.RecvStream(ctx, streamDesc)
		require.NoError(tt, err)

		_, err = readStream.Recv()
		require.Error(tt, err)
	})
}
