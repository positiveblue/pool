package ratings

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/lightninglabs/pool/order"
)

// Agency represents a node rating agency, its only role is to rate nodes. If
// it doesn't have the rating of anode, then the lowest tier should be retuned
// for that node.
type Agency interface {
	// RateNode returns the rating for a node. If the Agency doesn't know
	// about a node, then the lowest tier should be returned.
	RateNode(nodeKey [33]byte) order.NodeTier
}

// NodeRatingsDatabase is a logical ratings database. Before usage the
// IndexRatings() MUST be called.
type NodeRatingsDatabase interface {
	// IndexRatings indexes the set of ratings to get the most up to date
	// state. No other calls should be executed before this one.
	IndexRatings() error

	// LookupNode attempts to look up a rating for a node. If the node
	// isn't found, the lowest rating should be returned. The second return
	// value signifies if this node was found in the DB or not.
	LookupNode(nodeKey [33]byte) (order.NodeTier, bool)

	// ModifyNodeRating attempts to modify the rating for a node in-place.
	// This rating will then supersede the existing entry in the database.
	// This method can also be used to add a rating for a node that isn't
	// tracked.
	//
	// TODO(roasbeef): have only batched versions of this and the above?
	ModifyNodeRating([33]byte, order.NodeTier) error
}

// NodeRatingWebSource represents a web end point that serves node rating
// information in the form of a JSON response.
type NodeRatingWebSource interface {
	// GenQueryURL should return the full URL string to hit the JSON
	// endpoint.
	GenQueryURL() string

	// ParseResponse parses a JSON response encoded in the passed reader
	// into a map of a node pubkey to its rating.
	ParseResponse(r io.Reader) (NodeRatingsMap, error)
}

// BosScoreWebRatings is an implementation of the NodeRatingWebSource for the
// current bos score web endpoint.
type BosScoreWebRatings struct {
	// URL is the URL of the current bos score end point.
	URL string
}

// GenQueryURL should return the full URL string to hit the JSON endpoint.
//
// NOTE: This is part of the NodeRatingWebSource interface.
func (b *BosScoreWebRatings) GenQueryURL() string {
	return b.URL
}

type nodeRatingInfo struct {
	Alias  string `json:"alias"`
	Pubkey string `json:"public_key"`
	Score  uint64 `json:"score"`
}

type scoreResp struct {
	LastUpdated string `json:"last_updated"`

	Scores []nodeRatingInfo `json:"scores"`
}

// ParseResponse parses a JSON response encoded in the passed reader into a map
// of a node pubkey to its rating.
//
// NOTE: This is part of the NodeRatingWebSource interface.
func (b *BosScoreWebRatings) ParseResponse(r io.Reader) (NodeRatingsMap, error) {

	// With our up to date schema for the response above defined, we'll now
	// attempt to decode the actual JSON response into the above structs.
	resp := scoreResp{}
	jsonReader := json.NewDecoder(r)
	if err := jsonReader.Decode(&resp); err != nil {
		return nil, err
	}

	// The response uses hex encoding for each node pubkey, so we'll decode
	// and merge them into our response map.
	scoreMap := make(NodeRatingsMap)
	for _, nodeScore := range resp.Scores {
		nodeKeyBytes, err := hex.DecodeString(nodeScore.Pubkey)
		if err != nil {
			return nil, err
		}

		var nodeKey [33]byte
		copy(nodeKey[:], nodeKeyBytes)

		scoreMap[nodeKey] = order.NodeTier1
	}

	return scoreMap, nil
}

// A compile-time assertion to ensure the BosScoreWebRatings struct satisfies
// the NodeRatingWebSource interface.
var _ NodeRatingWebSource = (*BosScoreWebRatings)(nil)

// MemRatingsDatabase is an implementation of the NodeRatingsDatabase interface
// that is backed by an arbitrary in-memory list. It takes another database as
// well to act as a write through cache.
type MemRatingsDatabase struct {
	sync.RWMutex

	// nodeTierCache is protected by the above mutex and stores the current
	// set of node ratings.
	nodeTierCache NodeRatingsMap

	// writeThroughDB is set, will have all modifications to the in-memory
	// database applied to it as well.
	writeThroughDB NodeRatingsDatabase
}

// NodeRatingsMap is a type alias for a map that lets us look up a node to
// check its rating.
type NodeRatingsMap map[[33]byte]order.NodeTier

// NewMemRatingsDatabase returns a new instance of a NodeRatingsDatabase backed
// purely by an initially in-memory source. The writeThroughDB is an optional
// existing database to have all modifications replicated to.
func NewMemRatingsDatabase(writeThroughDB NodeRatingsDatabase,
	seedRatings NodeRatingsMap) *MemRatingsDatabase {

	nodeTierCache := make(NodeRatingsMap)
	for node, rating := range seedRatings {
		nodeTierCache[node] = rating
	}

	return &MemRatingsDatabase{
		nodeTierCache:  nodeTierCache,
		writeThroughDB: writeThroughDB,
	}
}

// IndexRatings indexes the set of ratings to get the most up to date state. No
// other calls should be executed before this one.
//
// NOTE: This is part of the NodeRatingsDatabase interface.
func (m *MemRatingsDatabase) IndexRatings() error {

	// No need to index things since we already had a set of seed ratings.
	return nil
}

// ModifyNodeRating attempts to modify the rating for a node in-place.  This
// rating will then supersede the existing entry in the database.  This method
// can also be used to add a rating for a node that isn't tracked.
//
// NOTE: This is part of the NodeRatingsDatabase interface.
func (m *MemRatingsDatabase) ModifyNodeRating(node [33]byte,
	tier order.NodeTier) error {

	m.Lock()
	defer m.Unlock()

	// First write through to the other DB if it's available.
	if m.writeThroughDB != nil {
		err := m.writeThroughDB.ModifyNodeRating(node, tier)
		if err != nil {
			return err
		}
	}

	m.nodeTierCache[node] = tier

	return nil
}

// LookupNode attempts to look up a rating for a node. If the node isn't found,
// the lowest rating should be returned.
//
// NOTE: This is part of the NodeRatingsDatabase interface.
func (m *MemRatingsDatabase) LookupNode(nodeKey [33]byte) (order.NodeTier, bool) {
	m.RLock()
	defer m.RUnlock()

	// We don't consult the database here, as we assume all those entires
	// if they exist, have already been loaded into the DB above to start
	// with.
	rating, ok := m.nodeTierCache[nodeKey]
	if !ok {
		return order.NodeTier0, false
	}

	return rating, ok
}

// A compile-time assertion to ensure the MemRatingsDatabase struct satisfies
// the NodeRatingsDatabase interface.
var _ NodeRatingsDatabase = (*MemRatingsDatabase)(nil)

// BosScoreRatingsDatabase is an implementation of the NodeRatingsDatabase
// backed by the BosScoreRatingsDatabase struct. This database will cache the
// result of the endpoint for a period of time, so we don't need to hammer the
// endpoint each time we want to query for a set of node ratings.
type BosScoreRatingsDatabase struct {
	// refreshInterval is the period that we'll wait between hits to the
	// API endpoint to refresh our view.
	refreshInterval time.Duration

	// webSource is the web source we'll use to hit the current bos score
	// API.
	webSource NodeRatingWebSource

	// refreshFunc stores a timer that's used as a time.AfterFunc to
	// refresh the data without needing to manage our own goroutine state.
	refreshFunc *time.Timer

	// ratingsDB is the backing DB that we'll read/write our bos scores
	// to/from.
	ratingsDB NodeRatingsDatabase
}

// NewBosScoreRatingsDatabase returns a new instance of the
// BosScoreRatingsDatabase.
func NewBosScoreRatingsDatabase(webSource NodeRatingWebSource,
	refreshInterval time.Duration,
	ratingsDB NodeRatingsDatabase) *BosScoreRatingsDatabase {

	return &BosScoreRatingsDatabase{
		webSource:       webSource,
		refreshInterval: refreshInterval,
		ratingsDB:       ratingsDB,
	}
}

// updateNodeRatings attempts to update the current set of node ratings using
// the latest state of the bos score end point.
//
// NOTE: This method should only be called ONCE, as it creates a time.AfterFunc
// to refresh the scores after an interval.
func (m *BosScoreRatingsDatabase) updateNodeRatings(doneChan chan struct{},
) func() {
	// TODO(roasbeef): if empty at this point, then read from disk or
	// accept existing ratings as args

	// We use an internal closure to be able to pass the return value
	// directly into time.AfterFunc, while also being able to give the
	// caller a sync call back.
	scrape := func() {
		// Rather than use the default http.Client, we'll make a custom
		// one which will allow us to control how long we'll wait to
		// read the response from the service. This way, if the service
		// is down or overloaded, we can exit early and use our default
		// fee.
		netTransport := &http.Transport{
			Dial: (&net.Dialer{
				Timeout: 5 * time.Second,
			}).Dial,
			TLSHandshakeTimeout: 5 * time.Second,
		}
		netClient := &http.Client{
			Timeout:   time.Second * 10,
			Transport: netTransport,
		}

		// With the client created, we'll query the API source to fetch
		// the URL that we should use to query for the fee estimation.
		targetURL := m.webSource.GenQueryURL()
		resp, err := netClient.Get(targetURL)
		if err != nil {
			log.Errorf("unable to query web api for rating response: %v",
				err)
			return
		}
		defer resp.Body.Close()

		// Once we've obtained the response, we'll instruct the
		// WebAPIFeeSource to parse out the body to obtain our final
		// result.
		nodeRatings, err := m.webSource.ParseResponse(resp.Body)
		if err != nil {
			log.Errorf("unable to query web api for rating response: %v",
				err)
			return
		}

		// Rather than replace, we'll merge in this new response to
		// make sure we don't override any of the existing scores.
		//
		// TODO(roasbeef): can/should also commit to disk here as well
		//
		// TODO(roasbeef): this also means nodees may stay on the list
		// for a longer period of time if they're volatile and are
		// right on the order
		for nodeKey, newRating := range nodeRatings {
			err := m.ratingsDB.ModifyNodeRating(nodeKey, newRating)
			if err != nil {
				log.Errorf("unable to modify rating for %x",
					nodeKey[:])
			}
		}

		// If this is our first run, then we'll set up the next
		// invocation after our wait interval.
		if m.refreshFunc == nil {
			m.refreshFunc = time.AfterFunc(
				m.refreshInterval, m.updateNodeRatings(nil),
			)

			// The very first time around, we'll close the done
			// chan if it exists to give the caller a synchronous
			// hook.
			if doneChan != nil {
				close(doneChan)
			}

			return
		}

		// Otherwise, we'll just reset the timer as it's already
		// active.
		m.refreshFunc.Reset(m.refreshInterval)
	}

	return scrape
}

// IndexRatings indexes the set of ratings to get the most up to date state. No
// other calls should be executed before this one.
//
// NOTE: This is part of the NodeRatingsDatabase interface.
func (m *BosScoreRatingsDatabase) IndexRatings() error {
	scrapeStart := time.Now()

	log.Infof("Indexing Bos Score Database")

	doneChan := make(chan struct{})

	// We want to make this first instance synchronous to ensure that once
	// this method returns the indexing has been completed, so we'll pass
	// in a done channel, then wait on it below.
	m.updateNodeRatings(doneChan)()

	select {
	case <-doneChan:
		break

	// If things don't complete within 1 minute (should be a pretty quick
	// operation, we'll return an error.
	case <-time.After(time.Minute):
		return fmt.Errorf("initial DB index for bos score " +
			"didn't complete in time")

	}

	log.Infof("Bos Score Indexing Complete: scrape_time=%v",
		time.Since(scrapeStart))

	return nil
}

// LookupNode attempts to look up a rating for a node. If the node isn't found,
// the lowest rating should be returned.
//
// NOTE: This is part of the NodeRatingsDatabase interface.
func (m *BosScoreRatingsDatabase) LookupNode(nodeKey [33]byte) (order.NodeTier, bool) {
	if _, ok := m.ratingsDB.LookupNode(nodeKey); !ok {
		return order.NodeTier0, true
	}

	return order.NodeTier1, true
}

// ModifyNodeRating attempts to modify the rating for a node in-place.  This
// rating will then supersede the existing entry in the database.  This method
// can also be used to add a rating for a node that isn't tracked.
//
// NOTE: This is part of the NodeRatingsDatabase interface.
func (m *BosScoreRatingsDatabase) ModifyNodeRating(node [33]byte,
	tier order.NodeTier) error {

	return m.ratingsDB.ModifyNodeRating(node, tier)
}

// A compile-time assertion to ensure the BosScoreRatingsDatabase struct satisfies
// the NodeRatingsDatabase interface.
var _ NodeRatingsDatabase = (*BosScoreRatingsDatabase)(nil)

// NodeTierAgency is an implementation of the Agency interface that lops nodes
// into two tiers: t0 and t1. t1 nodes have an actual rating, while t0 nodes
// are the "rest".
type NodeTierAgency struct {
	ratingsDB NodeRatingsDatabase
}

// NewNodeTierAgency returns a new instance of the NodeTierAgency struct.
func NewNodeTierAgency(ratingsDB NodeRatingsDatabase) *NodeTierAgency {
	return &NodeTierAgency{
		ratingsDB: ratingsDB,
	}
}

// RateNode returns the rating for a node. If the Agency doesn't know
// about a node, then the lowest tier should be returned.
//
// NOTE: This is part of the Agency interface.
func (n *NodeTierAgency) RateNode(nodeKey [33]byte) order.NodeTier {
	rating, _ := n.ratingsDB.LookupNode(nodeKey)
	return rating
}

// A compile-time assertion to ensure the NodeTierAgency struct satisfies the
// Agency interface.
var _ Agency = (*NodeTierAgency)(nil)
