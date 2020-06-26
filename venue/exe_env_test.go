package venue

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightninglabs/subasta/venue/matching"
)

// TestEnvironmentMessageMultiplex tests that messages from the venue are
// properly multi-plexed if multiple accounts/traders are connected over the
// same communication line identified by the same LSAT.
func TestEnvironmentMessageMultiplex(t *testing.T) {
	// Buffer the outgoing channel enough so we don't need to use goroutines
	// but we only expect one multi-plexed message to be sent over it.
	outgoingChan := make(chan ExecutionMsg, 2)
	commLine := &DuplexLine{
		Send: outgoingChan,
		Recv: make(IncomingMsgLine, 1),
	}

	// We reuse most of what we already have for the batch storer test. We
	// simulate the two traders of the matched orders to be connected to the
	// same communication line (and both have the same LSAT). Only one
	// message should be sent over the line.
	traders := make(map[matching.AccountID]*ActiveTrader)
	traders[bigTrader.AccountKey] = &ActiveTrader{
		Trader:   &bigTrader,
		TokenID:  lsat.TokenID{1, 2},
		CommLine: commLine,
	}
	traders[smallTrader.AccountKey] = &ActiveTrader{
		Trader:   &smallTrader,
		TokenID:  lsat.TokenID{1, 2},
		CommLine: commLine,
	}

	env := &environment{
		batch:   orderBatch,
		batchID: [33]byte{12, 34, 56},
		exeCtx: &batchtx.ExecutionContext{
			ExeTx: batchTx,
		},
		traders: traders,
	}

	validateMessagesReceived := func(validateContent func(ExecutionMsg)) {
		// Make sure we receive it on our end.
		select {
		case msg := <-outgoingChan:
			if msg.Batch() != env.batchID {
				t.Fatalf("unexpected batch ID, got %x wanted %x",
					msg.Batch(), env.batchID[:])
			}

			validateContent(msg)

		default:
			t.Fatalf("no message received")
		}

		// No further message should be received.
		select {
		case msg := <-outgoingChan:
			t.Fatalf("unexpected second message: %v", msg)

		default:
		}
	}

	// Ask the environment to send the preparation message out and make sure
	// we receive exactly one with both accounts included.
	err := env.sendPrepareMsg()
	if err != nil {
		t.Fatalf("could not send prepare message: %v", err)
	}
	validateMessagesReceived(func(msg ExecutionMsg) {
		switch m := msg.(type) {
		case *PrepareMsg:
			if len(m.MatchedOrders) != 3 {
				t.Fatalf("unexpected number of matched "+
					"orders, got %d wanted 3",
					len(m.MatchedOrders))
			}
			if len(m.AccountOutPoints) != 2 {
				t.Fatalf("unexpected number of account "+
					"outputs, got %d wanted 2",
					len(m.AccountOutPoints))
			}

		default:
			t.Fatalf("unknown message received: %#v", m)
		}
	})

	// Ask the environment to send the sign message out and make sure we
	// receive exactly one.
	err = env.sendSignBeginMsg()
	if err != nil {
		t.Fatalf("could not send sign message: %v", err)
	}
	validateMessagesReceived(func(msg ExecutionMsg) {
		switch m := msg.(type) {
		case *SignBeginMsg:
			// Nothing more to check.

		default:
			t.Fatalf("unknown message received: %#v", m)
		}
	})

	// Ask the environment to send the finalize message out and make sure we
	// receive exactly one.
	err = env.sendFinalizeMsg(chainhash.Hash{})
	if err != nil {
		t.Fatalf("could not send finalize message: %v", err)
	}
	validateMessagesReceived(func(msg ExecutionMsg) {
		switch m := msg.(type) {
		case *FinalizeMsg:
			// Nothing more to check.

		default:
			t.Fatalf("unknown message received: %#v", m)
		}
	})
}
