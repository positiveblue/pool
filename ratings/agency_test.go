package ratings

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/stretchr/testify/require"
)

const (
	// testServerAddr is the address that our test server will listen on
	// within this set.
	testServerAddr = "localhost:0"
)

var (
	_, nodeKey1 = btcec.PrivKeyFromBytes([]byte{0x1})
	_, nodeKey2 = btcec.PrivKeyFromBytes([]byte{0x2})
)

// testBosScoreServer...
type testBosScoreServer struct {
	ratings NodeRatingsMap
}

func newTestBosScoreServer() *testBosScoreServer {
	return &testBosScoreServer{
		ratings: make(NodeRatingsMap),
	}
}

func (t *testBosScoreServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := scoreResp{
		LastUpdated: "now",
		Scores:      make([]nodeRatingInfo, 0, 3),
	}

	for node, rating := range t.ratings {
		resp.Scores = append(resp.Scores, nodeRatingInfo{
			Alias:  string(node[:]),
			Pubkey: hex.EncodeToString(node[:]),
			Score:  uint64(rating),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func closeOrFail(t *testing.T, c io.Closer) {
	err := c.Close()
	if err != nil {
		t.Fatal(err)
	}
}

// TestBosScoreRatingsDatabase tests that the bos score DB is able to properly
// hit the specified endpoint, and refresh the set of scores periodically.
func TestBosScoreRatingsDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	var node1, node2, node3 [33]byte
	copy(node1[:], nodeKey1.SerializeCompressed())
	copy(node2[:], nodeKey2.SerializeCompressed())

	node1Score, node2Score, node3Score := 4, 2, 1

	bosScoreAPI := newTestBosScoreServer()
	bosScoreAPI.ratings[node1] = orderT.NodeTier(node1Score)
	bosScoreAPI.ratings[node2] = orderT.NodeTier(node2Score)

	// To pass the race tests where this test case is run multiple times in
	// parallel, we need to choose a distinct port for the server each time.
	listener, err := net.Listen("tcp", testServerAddr)
	require.NoError(t, err)
	defer closeOrFail(t, listener)

	// Next, we'll set up the set of struct we need, as well as set up the
	// write thru DB as well to test the caching logic.
	scrapeUpdates := make(chan struct{})
	refreshInterval := time.Second * 2
	writeThruDB := NewMemRatingsDatabase(
		nil, nil, orderT.NodeTier0, scrapeUpdates,
	)
	ratingsDB := NewMemRatingsDatabase(
		writeThruDB, nil, orderT.NodeTier0, nil,
	)
	scoreWebSource := BosScoreWebRatings{
		URL: fmt.Sprintf("http://%s/", listener.Addr()),
	}
	bosScoreDB := NewBosScoreRatingsDatabase(
		&scoreWebSource, refreshInterval, orderT.NodeTier0, ratingsDB,
	)

	// First, we'll kick off the indexing of the ratings for the first
	// time, however, we haven't actually started the endpoint yet, so a
	// scrape shouldn't actually happen.
	err = bosScoreDB.IndexRatings(ctx)
	require.NoError(t, err)

	// At this point, there should be no items within the ratings databse
	// as the initila scrape should have failed.
	require.Equal(t, len(writeThruDB.nodeTierCache), 0)

	// Now start the server that'll serve the response of the web server
	// within our tests.
	server := &http.Server{
		Handler: http.HandlerFunc(bosScoreAPI.ServeHTTP),
	}
	go func() { _ = server.Serve(listener) }()

	// Give the scraper time to do a proper scrape now that the endpoint is
	// back up.
	select {
	case <-scrapeUpdates:

	case <-time.After(refreshInterval * 2):
		t.Fatal("no scrape update made in time")
	}

	// We'll manually pause the ticker here to make the test a bit easier
	// to work with.
	bosScoreDB.refreshFunc.Stop()

	// Now that the source has been indexed, we should be able to find the
	// two nodes that we initialized the service with.
	freshNode1Score, ok := bosScoreDB.LookupNode(ctx, node1)
	require.True(t, ok)
	require.Equal(t, freshNode1Score, orderT.NodeTier1)

	freshNode2Score, ok := bosScoreDB.LookupNode(ctx, node2)
	require.True(t, ok)
	require.Equal(t, freshNode2Score, orderT.NodeTier1)

	// The write thru DB should now also have the same data as well.
	freshNode1Score, ok = writeThruDB.LookupNode(ctx, node1)
	require.True(t, ok)
	require.Equal(t, freshNode1Score, orderT.NodeTier1)
	freshNode2Score, ok = writeThruDB.LookupNode(ctx, node2)
	require.True(t, ok)
	require.Equal(t, freshNode2Score, orderT.NodeTier1)

	// Additionally, if we try to look up the 3rd node that isn't yet part
	// of the list, then we should come up with a node tier of 0 (the base
	// tier).
	freshNode3Score, ok := bosScoreDB.LookupNode(ctx, node3)
	require.True(t, ok)
	require.Equal(t, freshNode3Score, orderT.NodeTier0)

	// Now that we confirmed the initially scraping correctness, we'll
	// unpause the ticker to have it fetch some new fresh data.
	bosScoreAPI.ratings[node3] = orderT.NodeTier(node3Score)
	bosScoreDB.refreshFunc.Reset(refreshInterval)

	// If we query the system again, we should find that the 3rd node now
	// shows up at node tier 1.
	err = wait.Predicate(func() bool {
		freshNode3Score, _ := bosScoreDB.LookupNode(ctx, node3)
		writeThruNode3Score, _ := writeThruDB.LookupNode(ctx, node3)

		return (freshNode3Score == orderT.NodeTier1 &&
			writeThruNode3Score == orderT.NodeTier1)
	}, refreshInterval*2)
	require.NoError(t, err)
}
