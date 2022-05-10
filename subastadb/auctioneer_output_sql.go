package subastadb

import (
	"context"

	"github.com/lightninglabs/subasta/account"
)

// FetchAuctioneerAccount retrieves the current information pertaining
// to the current auctioneer output state.
func (s *SQLStore) FetchAuctioneerAccount(
	context.Context) (*account.Auctioneer, error) {

	return nil, ErrNotImplemented
}

// UpdateAuctioneerAccount updates the current auctioneer output
// in-place and also updates the per batch key according to the state in
// the auctioneer's account.
func (s *SQLStore) UpdateAuctioneerAccount(context.Context,
	*account.Auctioneer) error {

	return ErrNotImplemented
}
