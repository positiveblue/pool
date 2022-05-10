package subastadb

import (
	"context"

	orderT "github.com/lightninglabs/pool/order"
)

// IndexRatings indexes the set of ratings to get the most up to date
// state. No other calls should be executed before this one.
func (s *SQLStore) IndexRatings(context.Context) error {
	return ErrNotImplemented
}

// LookupNode attempts to look up a rating for a node. If the node
// isn't found, the lowest rating should be returned. The second return
// value signifies if this node was found in the DB or not.
func (s *SQLStore) LookupNode(context.Context, [33]byte) (orderT.NodeTier,
	bool) {

	return 0, false
}

// ModifyNodeRating attempts to modify the rating for a node in-place.
// This rating will then supersede the existing entry in the database.
// This method can also be used to add a rating for a node that isn't
// tracked.
//
// TODO(roasbeef): have only batched versions of this and the above?
func (s *SQLStore) ModifyNodeRating(context.Context, [33]byte,
	orderT.NodeTier) error {

	return ErrNotImplemented
}
