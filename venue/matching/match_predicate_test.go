package matching

import (
	"testing"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	node1Key    = NodeID{1, 2, 3, 4}
	node2Key    = NodeID{2, 3, 4, 5}
	node3Key    = NodeID{3, 4, 5, 6}
	node4Key    = NodeID{4, 5, 6, 7}
	acct1Key    = [33]byte{1, 2, 3, 4}
	acct2Key    = [33]byte{2, 3, 4, 5}
	multiSigKey = [33]byte{3, 4, 5, 6}
	acct1       = &account.Account{
		TraderKeyRaw: acct1Key,
		State:        account.StateOpen,
		Expiry:       2016,
	}
	acct2 = &account.Account{
		TraderKeyRaw: acct2Key,
		State:        account.StatePendingBatch,
		Expiry:       2016,
	}
	node1Ask = &order.Ask{
		Ask: orderT.Ask{
			Kit: orderT.Kit{
				AcctKey: acct1Key,
			},
		},
		Kit: order.Kit{
			NodeKey: node1Key,
		},
	}
	node2Bid = &order.Bid{
		Bid: orderT.Bid{
			Kit: orderT.Kit{
				AcctKey: acct2Key,
			},
		},
		Kit: order.Kit{
			NodeKey:     node2Key,
			MultiSigKey: multiSigKey,
		},
		IsSidecar: true,
	}
	node4Bid = &order.Bid{
		Bid: orderT.Bid{
			Kit: orderT.Kit{
				AcctKey: acct2Key,
			},
			MinNodeTier: 9,
		},
		Kit: order.Kit{
			NodeKey: node4Key,
		},
	}
)

func TestNodeConflictPredicate(t *testing.T) {
	t.Parallel()

	// Create the predicate and add some entries to it.
	p := NewNodeConflictPredicate()
	p.ReportConflict(node1Key, node2Key, "FUNDING_FAILED")
	p.ReportConflict(node1Key, node3Key, "HAVE_CHANNEL")
	p.ReportConflict(node3Key, node2Key, "FUNDING_FAILED")
	p.ReportConflict(node3Key, node1Key, "HAVE_CHANNEL")
	p.ReportConflict(node2Key, node4Key, "HAVE_CHANNEL")
	p.ReportConflict(node2Key, multiSigKey, "HAVE_CHANNEL")

	// Make sure the internal conflict map is filled correctly and can be
	// queried as expected.
	assert.Equal(t, 2, len(p.reports[node1Key]))
	assert.Equal(t, 1, len(p.reports[node1Key][node2Key]))
	assert.Equal(t, 2, len(p.reports[node3Key]))
	assert.Equal(t, 1, len(p.reports[node3Key][node2Key]))
	assert.True(t, p.HasConflict(node1Key, node2Key))
	assert.True(t, p.HasConflict(node1Key, node3Key))
	assert.True(t, p.HasConflict(node3Key, node2Key))
	assert.True(t, p.HasConflict(node3Key, node1Key))
	assert.True(t, p.HasConflict(node2Key, node1Key))
	assert.False(t, p.HasConflict(node1Key, node4Key))
	assert.False(t, p.HasConflict(node3Key, node4Key))
	assert.False(t, p.HasConflict(node4Key, node1Key))
	assert.False(t, p.HasConflict(node4Key, node3Key))

	// Also make sure the match predicate works as expected.
	assert.False(t, p.IsMatchable(node1Ask, node2Bid))
	assert.True(t, p.IsMatchable(node1Ask, node4Bid))
}

type agency struct {
	resp orderT.NodeTier
}

func (a *agency) RateNode(node [33]byte) orderT.NodeTier {
	return a.resp
}

// TestMinNodeRatingPredicate tests that two orders will only batch if the ask
// tier is GE the specified bid node tier.
func TestMinNodeRatingPredicate(t *testing.T) {
	t.Parallel()

	nodeAgency := &agency{}
	predicate := NewMinNodeRatingPredicate(nodeAgency)

	// As is, the bid wants a tier of 9, while the ask isn't in the agency,
	// which should cause it to fail.
	if predicate.IsMatchable(node1Ask, node4Bid) {
		t.Fatalf("node tier should be incompatible")
	}

	// Now we'll bump up the rating of the node to tier 11 which should
	// allow these two orders to be matched.
	nodeAgency.resp = 11
	if !predicate.IsMatchable(node1Ask, node4Bid) {
		t.Fatalf("node tier should be compatible")
	}
}

// TestChannelTypePredicate test that two orders will only match if their
// desired channel types are compatible.
func TestChannelTypePredicate(t *testing.T) {
	t.Parallel()

	ask := *node1Ask
	bid := *node2Bid

	// Both orders can specify their desired channel type. They'll match as
	// long as their channel types are compatible.
	ask.Version = orderT.VersionChannelType
	ask.ChannelType = orderT.ChannelTypePeerDependent
	bid.Version = orderT.VersionChannelType
	bid.ChannelType = orderT.ChannelTypePeerDependent
	require.True(t, MatchChannelType(&ask, &bid))

	ask.ChannelType = orderT.ChannelTypeScriptEnforced
	bid.ChannelType = orderT.ChannelTypeScriptEnforced
	require.True(t, MatchChannelType(&ask, &bid))

	bid.ChannelType = orderT.ChannelTypePeerDependent
	require.False(t, MatchChannelType(&ask, &bid))

	// Only the ask can specify its desired channel type. They'll match as
	// long as the ask isn't specifying a channel type.
	ask.Version = orderT.VersionChannelType
	ask.ChannelType = orderT.ChannelTypePeerDependent
	bid.Version = orderT.VersionChannelType - 1
	require.True(t, MatchChannelType(&ask, &bid))

	ask.ChannelType = orderT.ChannelTypeScriptEnforced
	require.False(t, MatchChannelType(&ask, &bid))

	// Only the bid can specify its desired channel type. They'll match as
	// long as the bid isn't specifying a channel type.
	ask.Version = orderT.VersionChannelType - 1
	bid.Version = orderT.VersionChannelType
	bid.ChannelType = orderT.ChannelTypePeerDependent
	require.True(t, MatchChannelType(&ask, &bid))

	bid.ChannelType = orderT.ChannelTypeScriptEnforced
	require.False(t, MatchChannelType(&ask, &bid))
}
