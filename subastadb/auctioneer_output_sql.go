package subastadb

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightningnetwork/lnd/keychain"
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

// marshalKeyDescriptor maps a *keychain.KeyDescriptor to its serialized
// version used in the db.
func marshalKeyDescriptor(kd *keychain.KeyDescriptor) (int64, int64, []byte) {
	return int64(kd.Family), int64(kd.Index),
		kd.PubKey.SerializeCompressed()
}

// unmarshalKeyDescriptor deserializes a *keychain.KeyDescriptor from its
// serialized version used in the db.
func unmarshalKeyDescriptor(family, index int64,
	key []byte) (*keychain.KeyDescriptor, error) {

	pubKey, err := btcec.ParsePubKey(key)
	if err != nil {
		return nil, fmt.Errorf("unable to  key descriptor: "+
			"%v", err)
	}

	return &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: keychain.KeyFamily(family),
			Index:  uint32(index),
		},
		PubKey: pubKey,
	}, nil
}
