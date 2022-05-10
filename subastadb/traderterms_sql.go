package subastadb

import (
	"context"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/subasta/traderterms"
)

// AllTraderTerms returns all trader terms currently in the store.
func (s *SQLStore) AllTraderTerms(ctx context.Context) ([]*traderterms.Custom,
	error) {

	return nil, ErrNotImplemented
}

// GetTraderTerms returns the trader terms for the given trader or
// ErrNoTerms if there are no terms stored for that trader.
func (s *SQLStore) GetTraderTerms(ctx context.Context,
	traderID lsat.TokenID) (*traderterms.Custom, error) {

	return nil, ErrNotImplemented
}

// PutTraderTerms stores a trader terms item, replacing the previous one
// if an item with the same ID existed.
func (s *SQLStore) PutTraderTerms(ctx context.Context,
	terms *traderterms.Custom) error {

	return ErrNotImplemented
}

// DelTraderTerms removes the trader specific terms for the given trader
// ID.
func (s *SQLStore) DelTraderTerms(ctx context.Context,
	traderID lsat.TokenID) error {

	return ErrNotImplemented
}
