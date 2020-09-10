package matching

import (
	"testing"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/stretchr/testify/assert"
)

var (
	node1Key = NodeID{1, 2, 3, 4}
	node2Key = NodeID{2, 3, 4, 5}
	node3Key = NodeID{3, 4, 5, 6}
	node4Key = NodeID{4, 5, 6, 7}
	node1Ask = &order.Ask{
		Ask: orderT.Ask{},
		Kit: order.Kit{
			NodeKey: node1Key,
		},
	}
	node2Bid = &order.Bid{
		Bid: orderT.Bid{},
		Kit: order.Kit{
			NodeKey: node2Key,
		},
	}
	node4Bid = &order.Bid{
		Bid: orderT.Bid{},
		Kit: order.Kit{
			NodeKey: node4Key,
		},
	}
)

func TestNodeConflictPredicate(t *testing.T) {
	// Create the predicate and add some entries to it.
	p := NewNodeConflictPredicate()
	p.ReportConflict(node1Key, node2Key, "FUNDING_FAILED")
	p.ReportConflict(node1Key, node3Key, "HAVE_CHANNEL")
	p.ReportConflict(node3Key, node2Key, "FUNDING_FAILED")
	p.ReportConflict(node3Key, node1Key, "HAVE_CHANNEL")
	p.ReportConflict(node2Key, node4Key, "HAVE_CHANNEL")

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
