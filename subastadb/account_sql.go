package subastadb

import (
	"context"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/subasta/account"
)

// HasReservation determines whether we have an existing reservation
// associated with a token. ErrNoReservation is returned if a
// reservation does not exist.
func (s *SQLStore) HasReservation(context.Context,
	lsat.TokenID) (*account.Reservation, error) {

	return nil, ErrNotImplemented
}

// HasReservationForKey determines whether we have an existing
// reservation associated with a trader key. ErrNoReservation is
// returned if a reservation does not exist.
func (s *SQLStore) HasReservationForKey(context.Context,
	*btcec.PublicKey) (*account.Reservation, *lsat.TokenID, error) {

	return nil, nil, ErrNotImplemented
}

// ReserveAccount makes a reservation for an auctioneer key for a trader
// associated to a token.
func (s *SQLStore) ReserveAccount(context.Context, lsat.TokenID,
	*account.Reservation) error {

	return ErrNotImplemented
}

// CompleteReservation completes a reservation for an account. This
// method should add a record for the account into the store.
func (s *SQLStore) CompleteReservation(context.Context,
	*account.Account) error {

	return ErrNotImplemented
}

// UpdateAccount updates an account in the store according to the given
// modifiers. Returns the updated account.
func (s *SQLStore) UpdateAccount(context.Context, *account.Account,
	...account.Modifier) (*account.Account, error) {

	return nil, ErrNotImplemented
}

// Account retrieves the account associated with the given trader key.
// The boolean indicates whether the account's diff should be returned
// instead. If a diff does not exist, then the existing account state is
// returned.
func (s *SQLStore) Account(context.Context, *btcec.PublicKey,
	bool) (*account.Account, error) {

	return nil, ErrNotImplemented
}

// Accounts retrieves all existing accounts.
func (s *SQLStore) Accounts(context.Context) ([]*account.Account, error) {
	return nil, ErrNotImplemented
}

// StoreAccountDiff stores a pending set of updates that should be
// applied to an account after an invocation of CommitAccountDiff.
//
// In contrast to UpdateAccount, this should be used whenever we need to
// stage a pending update of the account that will be committed at some
// later point.
func (s *SQLStore) StoreAccountDiff(context.Context, *btcec.PublicKey,
	[]account.Modifier) error {

	return ErrNotImplemented
}

// CommitAccountDiff commits the stored pending set of updates for an
// account after a successful modification. If a diff does not exist,
// account.ErrNoDiff is returned.
func (s *SQLStore) CommitAccountDiff(context.Context,
	*btcec.PublicKey) error {

	return ErrNotImplemented
}

// UpdateAccountDiff updates an account's pending diff.
func (s *SQLStore) UpdateAccountDiff(ctx context.Context,
	accountKey *btcec.PublicKey, modifiers []account.Modifier) error {

	return ErrNotImplemented
}

// DeleteAccountDiff deletes an account's pending diff.
func (s *SQLStore) DeleteAccountDiff(ctx context.Context,
	accountKey *btcec.PublicKey) error {

	return ErrNotImplemented
}

// RemoveReservation deletes a reservation identified by the LSAT ID.
func (s *SQLStore) RemoveReservation(ctx context.Context,
	id lsat.TokenID) error {

	return ErrNotImplemented
}
