package subastadb

import (
	"context"
	"fmt"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/ratings"
	"github.com/lightninglabs/subasta/subastadb/postgres"
)

// IndexRatings indexes the set of ratings to get the most up to date
// state. No other calls should be executed before this one.
func (s *SQLStore) IndexRatings(context.Context) error {
	// There's nothing to index so we can return immediately.
	return nil
}

// LookupNode attempts to look up a rating for a node. If the node
// isn't found, the lowest rating should be returned. The second return
// value signifies if this node was found in the DB or not.
func (s *SQLStore) LookupNode(ctx context.Context,
	nodeKey [33]byte) (orderT.NodeTier, bool) {

	rating, err := s.queries.GetNodeRating(ctx, nodeKey[:])
	if err != nil {
		log.Errorf("unable to retrieve rating for node %x: %v", nodeKey,
			err)
		return orderT.NodeTier0, false
	}

	return orderT.NodeTier(rating.NodeTier), true
}

// ModifyNodeRating attempts to modify the rating for a node in-place.
// This rating will then supersede the existing entry in the database.
// This method can also be used to add a rating for a node that isn't
// tracked.
//
// TODO(roasbeef): have only batched versions of this and the above?
func (s *SQLStore) ModifyNodeRating(ctx context.Context, nodeKey [33]byte,
	tier orderT.NodeTier) error {

	params := postgres.UpsertNodeRatingParams{
		NodeKey:  nodeKey[:],
		NodeTier: int64(tier),
	}
	return s.queries.UpsertNodeRating(ctx, params)
}

// NodeRatings returns a map of all node ratings known to the database.
func (s *SQLStore) NodeRatings(ctx context.Context) (ratings.NodeRatingsMap,
	error) {

	var rows []postgres.NodeRating
	txBody := func(txQueries *postgres.Queries) error {
		var err error

		params := postgres.GetNodeRatingsParams{}
		rows, err = txQueries.GetNodeRatings(ctx, params)
		if err != nil {
			return err
		}
		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return nil, fmt.Errorf("unable to get node ratings: %v", err)
	}

	ratings := make(ratings.NodeRatingsMap, len(rows))
	for _, row := range rows {
		var nodeKey [33]byte
		copy(nodeKey[:], row.NodeKey)

		ratings[nodeKey] = orderT.NodeTier(row.NodeTier)
	}

	return ratings, nil
}
