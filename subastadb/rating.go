package subastadb

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/ratings"
)

const (
	// ratingPrefix is the prefix that we'll use to store all node ratings.
	// From the top level directory, this path is:
	//
	//   bitcoin/clm/subasta/<network>/rating
	ratingPrefix = "rating"

	// numNodeRatingKeyParts is the expected number of parts of a node
	// rating key found within the database when split by the key delimiter.
	numNodeRatingKeyParts = 6
)

// nodeFromRatingKey extracts the node key from its node rating database key.
func nodeFromRatingKey(key string) ([33]byte, error) {
	parts := strings.Split(key, keyDelimiter)
	if len(parts) != numNodeRatingKeyParts {
		return [33]byte{}, fmt.Errorf("malformed node rating key %v", key)
	}

	rawNodeKey, err := hex.DecodeString(parts[len(parts)-1])
	if err != nil {
		return [33]byte{}, err
	}

	var nodeKey [33]byte
	copy(nodeKey[:], rawNodeKey)
	return nodeKey, nil
}

// getNodeRatingKey returns the full path key to obtain the rating for the given
// node within the database.
func (s *EtcdStore) getNodeRatingKey(nodeKey [33]byte) string {
	// bitcoin/clm/subasta/<network>/rating/<node_key>
	return strings.Join(
		[]string{
			s.getKeyPrefix(ratingPrefix),
			hex.EncodeToString(nodeKey[:]),
		},
		keyDelimiter,
	)
}

// IndexRatings indexes the set of ratings to get the most up to date state. No
// other calls should be executed before this one.
func (s *EtcdStore) IndexRatings(ctx context.Context) error {
	if !s.initialized {
		return errNotInitialized
	}

	// There's nothing to index so we can return immediately.
	return nil
}

// LookupNode attempts to look up a rating for a node. If the node isn't found,
// the lowest rating should be returned. The second return value signifies if
// this node was found in the DB or not.
func (s *EtcdStore) LookupNode(ctx context.Context,
	nodeKey [33]byte) (orderT.NodeTier, bool) {

	if !s.initialized {
		return orderT.NodeTier0, false

	}

	// Attempt to retrieve the serialized rating from the store.
	resp, err := s.getSingleValue(ctx, s.getNodeRatingKey(nodeKey), nil)
	if err != nil {
		log.Errorf("Unable to retrieve rating for node %x: %v", nodeKey,
			err)
		return orderT.NodeTier0, false
	}

	// If a rating is not found, return the lowest rating as expected.
	if resp == nil {
		return orderT.NodeTier0, false
	}

	// Otherwise, deserialize it and return.
	var nodeTier orderT.NodeTier
	err = ReadElement(bytes.NewReader(resp.Kvs[0].Value), &nodeTier)
	if err != nil {
		log.Errorf("Unable to deserialize rating for node %x: %v",
			nodeKey, err)
		return orderT.NodeTier0, false
	}

	return nodeTier, true
}

// ModifyNodeRating attempts to modify the rating for a node in-place.  This
// rating will then supersede the existing entry in the database.  This method
// can also be used to add a rating for a node that isn't tracked.
func (s *EtcdStore) ModifyNodeRating(ctx context.Context, nodeKey [33]byte,
	tier orderT.NodeTier) error {

	if !s.initialized {
		return errNotInitialized
	}

	var buf bytes.Buffer
	if err := WriteElement(&buf, tier); err != nil {
		return err
	}

	return s.put(ctx, s.getNodeRatingKey(nodeKey), buf.String())
}

// NodeRatings returns a map of all node ratings known to the database.
func (s *EtcdStore) NodeRatings(ctx context.Context) (ratings.NodeRatingsMap, error) {
	resp, err := s.getAllValuesByPrefix(ctx, s.getKeyPrefix(ratingPrefix))
	if err != nil {
		return nil, err
	}

	nodeRatings := make(ratings.NodeRatingsMap, len(resp))
	for k, v := range resp {
		nodeKey, err := nodeFromRatingKey(k)
		if err != nil {
			return nil, err
		}

		var nodeTier orderT.NodeTier
		err = ReadElement(bytes.NewReader(v), &nodeTier)
		if err != nil {
			return nil, err
		}

		nodeRatings[nodeKey] = nodeTier
	}

	return nodeRatings, nil
}
