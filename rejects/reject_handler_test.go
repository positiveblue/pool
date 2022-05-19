package rejects_test

import (
	"testing"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/rejects"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/stretchr/testify/require"
)

var (
	nonce1 = orderT.Nonce{0x01}
	nonce2 = orderT.Nonce{0x02}
	nonce3 = orderT.Nonce{0x03}
	nonce4 = orderT.Nonce{0x04}

	acct1 = matching.AccountID{0x01}
	acct2 = matching.AccountID{0x02}
	acct3 = matching.AccountID{0x03}

	node1 = [33]byte{0x11}
	node2 = [33]byte{0x12}
	node3 = [33]byte{0x13}
)

type conflict struct {
	node1, node2 [33]byte
}

// TestHandleReject tests various scenarios of reject handling, ensuring the
// proper orders and conflicts are reported.
func TestHandleReject(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		matches         []rejects.Match
		rejectingTrader map[matching.AccountID]*venue.OrderRejectMap
		expRemoved      []orderT.Nonce
		expConflicts    []conflict
	}{{
		name: "single full reject",
		rejectingTrader: map[matching.AccountID]*venue.OrderRejectMap{
			acct1: {
				FullReject: &venue.Reject{
					Type:   venue.FullRejectServerMisbehavior,
					Reason: "hmm",
				},
				OwnOrders: []orderT.Nonce{
					nonce1,
					nonce2,
				},
			},
		},
		// All the trader's orders should be removed.
		expRemoved: []orderT.Nonce{
			nonce1,
			nonce2,
		},
	}, {
		name: "partial reject",
		matches: []rejects.Match{
			{
				Ask: rejects.Order{
					AcctKey: acct1,
					NodeKey: node1,
					Nonce:   nonce1,
				},
				Bid: rejects.Order{
					AcctKey: acct2,
					NodeKey: node2,
					Nonce:   nonce2,
				},
			},
		},
		rejectingTrader: map[matching.AccountID]*venue.OrderRejectMap{
			acct1: {
				PartialRejects: map[orderT.Nonce]*venue.Reject{
					nonce2: {
						Type:   venue.PartialRejectFundingFailed,
						Reason: "hmm",
					},
				},
				OwnOrders: []orderT.Nonce{
					nonce1,
				},
			},
		},
		// The two nodes should have a conflict.
		expConflicts: []conflict{
			{
				node1, node2,
			},
		},
	}, {
		// In this test case there is a bid that is matched
		// with two asks. One of the askers rejects.
		name: "partial matches, partial reject from one",
		matches: []rejects.Match{
			{
				Ask: rejects.Order{
					AcctKey: acct1,
					NodeKey: node1,
					Nonce:   nonce1,
				},
				Bid: rejects.Order{
					AcctKey: acct2,
					NodeKey: node2,
					Nonce:   nonce2,
				},
			},
			{
				Ask: rejects.Order{
					AcctKey: acct3,
					NodeKey: node3,
					Nonce:   nonce3,
				},
				Bid: rejects.Order{
					AcctKey: acct2,
					NodeKey: node2,
					Nonce:   nonce2,
				},
			},
		},
		rejectingTrader: map[matching.AccountID]*venue.OrderRejectMap{
			acct3: {
				PartialRejects: map[orderT.Nonce]*venue.Reject{
					nonce2: {
						Type:   venue.PartialRejectFundingFailed,
						Reason: "hmm",
					},
				},
				OwnOrders: []orderT.Nonce{
					nonce3,
				},
			},
		},
		// We expect node 3 and 2 to have a conflict.
		expConflicts: []conflict{
			{
				node3, node2,
			},
		},
	}, {
		// In this test case there is a node that rejects an order it
		// was not matched with.
		name: "partial reject invalid nonce",
		matches: []rejects.Match{
			{
				Ask: rejects.Order{
					AcctKey: acct1,
					NodeKey: node1,
					Nonce:   nonce1,
				},
				Bid: rejects.Order{
					AcctKey: acct2,
					NodeKey: node2,
					Nonce:   nonce2,
				},
			},
		},
		rejectingTrader: map[matching.AccountID]*venue.OrderRejectMap{
			acct1: {
				PartialRejects: map[orderT.Nonce]*venue.Reject{
					nonce3: {
						Type:   venue.PartialRejectFundingFailed,
						Reason: "hmm",
					},
				},
				OwnOrders: []orderT.Nonce{
					nonce1, nonce4,
				},
			},
		},
		// We expect the node's orders to be removed, since it sent a
		// reject for a nonce it was not matched with.
		expRemoved: []orderT.Nonce{
			nonce1,
			nonce4,
		},
	}}

	for _, test := range testCases {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var (
				ineligibleOrders []orderT.Nonce
				conflicts        []conflict
			)

			// Set up a handler that will fill the above slices
			// with the reported information.
			handler := &rejects.RejectHandler{
				ReportConflict: func(reporter, subject [33]byte,
					reason string) {

					conflicts = append(
						conflicts,
						conflict{reporter, subject},
					)
				},
				RemoveIneligibleOrders: func(
					orders []orderT.Nonce) {

					ineligibleOrders = append(
						ineligibleOrders, orders...,
					)
				},
			}

			// Start the reject handling.
			handler.HandleReject(test.matches, test.rejectingTrader)

			// Make sure the reported orders and conflicts are what
			// we expect.
			require.Equal(t, test.expRemoved, ineligibleOrders)
			require.Equal(t, test.expConflicts, conflicts)
		})
	}
}
