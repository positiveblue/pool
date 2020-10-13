package subastadb

import (
	"context"
	"testing"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/stretchr/testify/require"
)

// TestNodeRatings ensures that we can properly retrieve and modify a node's
// rating within the database.
func TestNodeRatings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	nodeKey1 := [33]byte{1}
	nodeKey2 := [33]byte{2}

	tier, found := store.LookupNode(ctx, nodeKey1)
	require.False(t, found)
	require.Equal(t, tier, orderT.NodeTier0)

	err := store.ModifyNodeRating(ctx, nodeKey1, orderT.NodeTier1)
	require.NoError(t, err)

	tier, found = store.LookupNode(ctx, nodeKey1)
	require.True(t, found)
	require.Equal(t, tier, orderT.NodeTier1)

	err = store.ModifyNodeRating(ctx, nodeKey2, orderT.NodeTier0)
	require.NoError(t, err)

	tier, found = store.LookupNode(ctx, nodeKey2)
	require.True(t, found)
	require.Equal(t, tier, orderT.NodeTier0)

	nodeRatings, err := store.NodeRatings(ctx)
	require.NoError(t, err)
	require.Len(t, nodeRatings, 2)
	require.Contains(t, nodeRatings, nodeKey1)
	require.Equal(t, nodeRatings[nodeKey1], orderT.NodeTier1)
	require.Contains(t, nodeRatings, nodeKey2)
	require.Equal(t, nodeRatings[nodeKey2], orderT.NodeTier0)
}
