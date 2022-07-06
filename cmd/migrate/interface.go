package main

import (
	"context"

	"github.com/lightninglabs/aperture/lsat"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/subastadb"
)

type Store interface {
	subastadb.AdminStore

	// StoreAccount inserts a new account in the db.
	StoreAccount(context.Context, *account.Account) error

	// Reservations returns all the reservation in the db.
	Reservations(context.Context) ([]*account.Reservation, []lsat.TokenID, error)

	// CreateAccountDiff inserts a new account diff in the db.
	CreateAccountDiff(context.Context, *account.Account) error

	// ListAccountDiffs returns all the account diffs in the db.
	ListAccountDiffs(context.Context) ([]*account.Account, error)

	// StoreBatch inserts a batch with its confirmation status in the db.
	StoreBatch(context.Context, orderT.BatchID, *subastadb.BatchSnapshot, bool) error

	// SetCurrentBatch inserts the current batch key in the db.
	SetCurrentBatch(context.Context, orderT.BatchID) error
}
