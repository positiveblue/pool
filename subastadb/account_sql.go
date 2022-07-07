package subastadb

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/jackc/pgx/v4"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/subastadb/postgres"
)

// HasReservation determines whether we have an existing reservation
// associated with a token. ErrNoReservation is returned if a
// reservation does not exist.
func (s *SQLStore) HasReservation(ctx context.Context,
	tokenID lsat.TokenID) (*account.Reservation, error) {

	errMsg := "unable to check account reservation using tokenID(%x): %w"

	row, err := s.queries.GetAccountReservationByTokenID(
		ctx, tokenID[:],
	)
	switch {
	case err == pgx.ErrNoRows:
		return nil, fmt.Errorf(errMsg, tokenID,
			account.ErrNoReservation)

	case err != nil:
		return nil, fmt.Errorf(errMsg, tokenID, err)
	}

	reservation, err := unmarshalReservation(row)
	if err != nil {
		return nil, fmt.Errorf(errMsg, tokenID, err)
	}

	return reservation, nil
}

// HasReservationForKey determines whether we have an existing
// reservation associated with a trader key. ErrNoReservation is
// returned if a reservation does not exist.
func (s *SQLStore) HasReservationForKey(ctx context.Context,
	traderKey *btcec.PublicKey) (*account.Reservation, *lsat.TokenID,
	error) {

	errMsg := "unable to check account reservation using accKey(%x): %w"

	accKey := traderKey.SerializeCompressed()
	row, err := s.queries.GetAccountReservationByTraderKey(ctx, accKey)
	switch {
	case err == pgx.ErrNoRows:
		return nil, nil, account.ErrNoReservation

	case err != nil:
		return nil, nil, fmt.Errorf(errMsg, accKey, err)
	}

	reservation, err := unmarshalReservation(row)
	if err != nil {
		return nil, nil, fmt.Errorf(errMsg, accKey, err)
	}

	tokenID := &lsat.TokenID{}
	copy(tokenID[:], row.TokenID)

	return reservation, tokenID, nil
}

// ReserveAccount makes a reservation for an auctioneer key for a trader
// associated to a token.
func (s *SQLStore) ReserveAccount(ctx context.Context, tokenID lsat.TokenID,
	reservation *account.Reservation) error {

	family, index, pubKey := marshalKeyDescriptor(
		reservation.AuctioneerKey,
	)
	batchKey := reservation.InitialBatchKey.SerializeCompressed()

	params := postgres.CreateAccountReservationParams{
		TraderKey:           reservation.TraderKeyRaw[:],
		Value:               int64(reservation.Value),
		AuctioneerKeyFamily: family,
		AuctioneerKeyIndex:  index,
		AuctioneerPublicKey: pubKey,
		InitialBatchKey:     batchKey,
		Expiry:              int64(reservation.Expiry),
		HeightHint:          int64(reservation.HeightHint),
		TokenID:             tokenID[:],
	}
	err := s.queries.CreateAccountReservation(ctx, params)
	if err != nil {
		return fmt.Errorf("unable to create account reservation(%x): "+
			"%v", reservation.TraderKeyRaw, err)
	}
	return nil
}

// CompleteReservation completes a reservation for an account. This
// method should add a record for the account into the store.
func (s *SQLStore) CompleteReservation(ctx context.Context,
	acc *account.Account) error {

	tokenID := acc.TokenID
	txBody := func(txQueries *postgres.Queries) error {
		_, err := txQueries.GetAccount(ctx, acc.TraderKeyRaw[:])
		if err == nil {
			return account.ErrAccountExists
		}

		err = deleteAccountReservationWithTx(ctx, txQueries, tokenID)
		if err != nil {
			return err
		}

		err = upsertAccountWithTx(ctx, txQueries, acc)
		if err != nil {
			return err
		}

		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		accKey := acc.AuctioneerKey.PubKey.SerializeCompressed()
		return fmt.Errorf("unable to complete account reservation "+
			"(%x): %w", accKey, err)
	}
	return nil
}

// UpdateAccount updates an account in the store according to the given
// modifiers. Returns the updated account.
func (s *SQLStore) UpdateAccount(ctx context.Context, acc *account.Account,
	modifiers ...account.Modifier) (*account.Account, error) {

	traderKey, err := acc.TraderKey()
	if err != nil {
		return nil, err
	}

	var dbAccount *account.Account
	txBody := func(txQueries *postgres.Queries) error {
		dbAccount, err = modifyAccountWithTx(
			ctx, txQueries, traderKey, modifiers...,
		)
		return err
	}

	err = s.ExecTx(ctx, txBody)
	if err != nil {
		return nil, fmt.Errorf("unable to update account(%x): %w",
			traderKey.SerializeCompressed(), err)
	}
	return dbAccount, nil
}

// Account retrieves the account associated with the given trader key.
// The boolean indicates whether the account's diff should be returned
// instead. If a diff does not exist, then the existing account state is
// returned.
func (s *SQLStore) Account(ctx context.Context, accKey *btcec.PublicKey,
	includeDiff bool) (*account.Account, error) {

	errMsg := "unable to get account(%x): %v"

	traderKey := accKey.SerializeCompressed()
	// If includeDiff is true and an active accountDiff exits for the
	// trader account we will return the accountDiff.
	if includeDiff {
		row, err := s.queries.GetNotConfirmedAccountDiff(ctx, traderKey)
		switch {
		// Return any unexpected error.
		case err != nil && err != pgx.ErrNoRows:
			return nil, fmt.Errorf(errMsg, traderKey, err)

		// If the account diff exists return that.
		case err == nil:
			accDiff, err := unmarshalAccountDiff(row)
			if err != nil {
				return nil, fmt.Errorf(errMsg, traderKey, err)
			}
			return accDiff, nil

		// If we got here is that err == pgx.ErrNoRows so no account
		// diff exists for the given trader key.
		default:
		}
	}

	row, err := s.queries.GetAccount(ctx, traderKey)
	if err != nil {
		return nil, fmt.Errorf(errMsg, traderKey, err)
	}

	account, err := unmarshalAccount(row)
	if err != nil {
		return nil, fmt.Errorf(errMsg, traderKey, err)
	}

	return account, nil
}

// Accounts retrieves all existing accounts.
func (s *SQLStore) Accounts(ctx context.Context) ([]*account.Account, error) {
	errMsg := "unable to get accounts: %v"

	var rows []postgres.Account
	txBody := func(txQueries *postgres.Queries) error {
		// TODO(positiveblue): use LIMIT/OFFSET for batching the
		// requests.
		var err error
		params := postgres.GetAccountsParams{}
		rows, err = txQueries.GetAccounts(ctx, params)
		if err != nil {
			return err
		}

		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	accounts := make([]*account.Account, 0, len(rows))
	for _, row := range rows {
		account, err := unmarshalAccount(row)
		if err != nil {
			return nil, fmt.Errorf(errMsg, err)
		}
		accounts = append(accounts, account)
	}

	return accounts, nil
}

// StoreAccountDiff stores a pending set of updates that should be
// applied to an account after an invocation of CommitAccountDiff.
//
// In contrast to UpdateAccount, this should be used whenever we need to
// stage a pending update of the account that will be committed at some
// later point.
func (s *SQLStore) StoreAccountDiff(ctx context.Context,
	traderKey *btcec.PublicKey, modifiers []account.Modifier) error {

	accKey := traderKey.SerializeCompressed()
	txBody := func(txQueries *postgres.Queries) error {
		// Make sure there is not already an active account diff.
		_, err := txQueries.GetNotConfirmedAccountDiff(
			ctx, accKey,
		)
		if err != pgx.ErrNoRows {
			return ErrAccountDiffAlreadyExists
		}

		row, err := txQueries.GetAccount(ctx, accKey)
		switch {
		case err == pgx.ErrNoRows:
			return NewAccountNotFoundError(traderKey)

		case err != nil:
			return err
		}

		acct, err := unmarshalAccount(row)
		if err != nil {
			return err
		}

		// Apply the given modifications to it.
		acctDiff := acct.Copy(modifiers...)

		confirmed := false
		err = createAccountDiffWithTx(
			ctx, txQueries, confirmed, acctDiff,
		)
		if err != nil {
			return err
		}

		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to store account diff for trader "+
			"(%x): %w", accKey, err)
	}
	return nil
}

// CommitAccountDiff commits the stored pending set of updates for an
// account after a successful modification. If a diff does not exist,
// account.ErrNoDiff is returned.
func (s *SQLStore) CommitAccountDiff(ctx context.Context,
	traderKey *btcec.PublicKey) error {

	accKey := traderKey.SerializeCompressed()
	txBody := func(txQueries *postgres.Queries) error {
		// Make sure that the diff exists.
		diff, err := txQueries.GetNotConfirmedAccountDiff(
			ctx, accKey,
		)
		switch {
		case err == pgx.ErrNoRows:
			return account.ErrNoDiff

		case err != nil:
			return err
		}

		acc, err := unmarshalAccountDiff(diff)
		if err != nil {
			return err
		}

		// Update (overwrite) the account using the last diff.
		err = upsertAccountWithTx(ctx, txQueries, acc)
		if err != nil {
			return err
		}

		// Mark the diff as confirmed.
		_, err = txQueries.ConfirmAccountDiff(ctx, accKey)
		return err
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to commit account diff for trader "+
			"(%x): %w", accKey, err)
	}
	return nil
}

// UpdateAccountDiff updates an account's pending diff.
func (s *SQLStore) UpdateAccountDiff(ctx context.Context,
	traderKey *btcec.PublicKey, modifiers []account.Modifier) error {

	accKey := traderKey.SerializeCompressed()
	txBody := func(txQueries *postgres.Queries) error {
		row, err := txQueries.GetNotConfirmedAccountDiff(
			ctx, accKey,
		)
		switch {
		case err == pgx.ErrNoRows:
			return account.ErrNoDiff

		case err != nil:
			return err
		}

		acct, err := unmarshalAccountDiff(row)
		if err != nil {
			return err
		}

		// Apply the given modifications to it.
		acctDiff := acct.Copy(modifiers...)

		id := row.ID
		isConfirmed := row.Confirmed
		err = updateAccountDiffWithTx(
			ctx, txQueries, id, isConfirmed, acctDiff,
		)
		if err != nil {
			return err
		}

		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to update account diff for trader "+
			"(%x): %w", traderKey.SerializeCompressed(), err)
	}
	return nil
}

// DeleteAccountDiff deletes an account's pending diff.
//
// NOTE: By deleting, we mean to mark it as merged the account diff in the db.
// We want to keep them as a historic of the user account.
func (s *SQLStore) DeleteAccountDiff(ctx context.Context,
	traderKey *btcec.PublicKey) error {

	accKey := traderKey.SerializeCompressed()
	txBody := func(txQueries *postgres.Queries) error {
		row, err := txQueries.GetNotConfirmedAccountDiff(
			ctx, accKey,
		)
		switch {
		case err == pgx.ErrNoRows:
			return account.ErrNoDiff

		case err != nil:
			return err
		}

		acc, err := unmarshalAccountDiff(row)
		if err != nil {
			return err
		}

		isConfirmed := true
		err = updateAccountDiffWithTx(
			ctx, txQueries, row.ID, isConfirmed, acc,
		)
		if err != nil {
			return err
		}

		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to delete account diff for trader "+
			"(%x): %w", traderKey.SerializeCompressed(), err)
	}
	return nil
}

// CreateAccountDiff inserts a new account diff in the db.
func (s *SQLStore) CreateAccountDiff(ctx context.Context,
	acc *account.Account) error {

	txBody := func(txQueries *postgres.Queries) error {
		return createAccountDiffWithTx(ctx, txQueries, false, acc)
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to create account diff for trader "+
			"(%x): %w", acc.TraderKeyRaw, err)
	}

	return nil
}

// ListAccountDiffs returns all the account diffs in the db.
func (s *SQLStore) ListAccountDiffs(ctx context.Context) ([]*account.Account,
	error) {

	errMsg := "%v %v"
	var accounts []*account.Account
	txBody := func(txQueries *postgres.Queries) error {
		dbAccounts, err := s.Accounts(ctx)
		if err != nil {
			return err
		}

		accounts = make([]*account.Account, 0, len(dbAccounts))
		for _, dbAccount := range dbAccounts {
			traderKey := dbAccount.TraderKeyRaw
			row, err := s.queries.GetNotConfirmedAccountDiff(
				ctx, traderKey[:],
			)
			switch {
			// Return any unexpected error.
			case err != nil && err != pgx.ErrNoRows:
				return fmt.Errorf(errMsg, traderKey, err)

				// If the account diff exists return that.
			case err == nil:
				accDiff, err := unmarshalAccountDiff(row)
				if err != nil {
					return fmt.Errorf(errMsg, traderKey,
						err)
				}
				accounts = append(accounts, accDiff)
			}
		}
		return nil
	}

	err := s.ExecTx(ctx, txBody)
	return accounts, err
}

// StoreAccount inserts a new account in the db.
func (s *SQLStore) StoreAccount(ctx context.Context,
	acc *account.Account) error {

	txBody := func(txQueries *postgres.Queries) error {
		return upsertAccountWithTx(ctx, txQueries, acc)
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to upsert account (%x): %w",
			acc.TraderKeyRaw, err)
	}
	return nil
}

// Reservations returns all the reservation in the db.
func (s *SQLStore) Reservations(ctx context.Context) ([]*account.Reservation,
	[]lsat.TokenID, error) {

	reservations := []*account.Reservation{}
	tokens := []lsat.TokenID{}
	txBody := func(txQueries *postgres.Queries) error {
		params := postgres.GetAccountReservationsParams{}
		rows, err := txQueries.GetAccountReservations(ctx, params)
		if err != nil {
			return err
		}

		for _, row := range rows {
			reservation, err := unmarshalReservation(row)
			if err != nil {
				return err
			}
			var token lsat.TokenID
			copy(token[:], row.TokenID)
			tokens = append(tokens, token)
			reservations = append(reservations, reservation)
		}
		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get reservations: %w",
			err)
	}
	return reservations, tokens, nil
}

// RemoveReservation deletes a reservation identified by the LSAT ID.
func (s *SQLStore) RemoveReservation(ctx context.Context,
	tokenID lsat.TokenID) error {

	errMsg := "unable do remove reservation (%v): %v"
	rowsAffected, err := s.queries.DeleteAccountReservation(ctx, tokenID[:])
	switch {
	case err != nil:
		return fmt.Errorf(errMsg, tokenID, err)

	case rowsAffected == 0:
		return fmt.Errorf(errMsg, tokenID, account.ErrNoReservation)
	}
	return nil
}

// deleteAccountReservationWithTx removes an account reservation using the
// provided queries struct.
func deleteAccountReservationWithTx(ctx context.Context,
	txQueries *postgres.Queries, tokenID lsat.TokenID) error {

	rowsAffected, err := txQueries.DeleteAccountReservation(ctx, tokenID[:])

	switch {
	case err != nil:
		return err

	case rowsAffected == 0:
		return account.ErrNoReservation
	}

	return nil
}

// upsertAccountWithTx inserts/updates an account using the provided queries
// struct.
func upsertAccountWithTx(ctx context.Context, txQueries *postgres.Queries,
	acc *account.Account) error {

	if acc == nil {
		return errors.New("unable to store a <nil> account")
	}

	family, index, pubKey := marshalKeyDescriptor(
		acc.AuctioneerKey,
	)

	outPointHash, outPointIndex := marshalOutPoint(acc.OutPoint)

	params := postgres.UpsertAccountParams{
		TraderKey:           acc.TraderKeyRaw[:],
		TokenID:             acc.TokenID[:],
		Value:               int64(acc.Value),
		Expiry:              int64(acc.Expiry),
		AuctioneerKeyFamily: family,
		AuctioneerKeyIndex:  index,
		AuctioneerPublicKey: pubKey,
		BatchKey:            acc.BatchKey.SerializeCompressed(),
		Secret:              acc.Secret[:],
		State:               int16(acc.State),
		HeightHint:          int64(acc.HeightHint),
		OutPointHash:        outPointHash,
		OutPointIndex:       outPointIndex,
		UserAgent:           acc.UserAgent,
	}

	if acc.LatestTx != nil {
		var latestTx bytes.Buffer
		if err := acc.LatestTx.Serialize(&latestTx); err != nil {
			return err
		}
		params.LatestTx = latestTx.Bytes()
	}

	return txQueries.UpsertAccount(ctx, params)
}

// createAccountDiffWithTx creates a new account diff using the provided queries
// struct.
func createAccountDiffWithTx(ctx context.Context, txQueries *postgres.Queries,
	isConfirmed bool, acc *account.Account) error {

	if acc == nil {
		return errors.New("unable to store a <nil> account")
	}

	family, index, pubKey := marshalKeyDescriptor(
		acc.AuctioneerKey,
	)

	outPointHash, outPointIndex := marshalOutPoint(acc.OutPoint)

	var latestTx bytes.Buffer
	if err := acc.LatestTx.Serialize(&latestTx); err != nil {
		return err
	}

	params := postgres.CreateAccountDiffParams{
		TraderKey:           acc.TraderKeyRaw[:],
		TokenID:             acc.TokenID[:],
		Confirmed:           isConfirmed,
		Value:               int64(acc.Value),
		Expiry:              int64(acc.Expiry),
		AuctioneerKeyFamily: family,
		AuctioneerKeyIndex:  index,
		AuctioneerPublicKey: pubKey,
		BatchKey:            acc.BatchKey.SerializeCompressed(),
		Secret:              acc.Secret[:],
		State:               int16(acc.State),
		HeightHint:          int64(acc.HeightHint),
		OutPointHash:        outPointHash,
		OutPointIndex:       outPointIndex,
		LatestTx:            latestTx.Bytes(),
		UserAgent:           acc.UserAgent,
	}
	return txQueries.CreateAccountDiff(ctx, params)
}

// updateAccountDiffWithTx updates an account diff using the provided queries
// struct.
func updateAccountDiffWithTx(ctx context.Context, txQueries *postgres.Queries,
	id int64, isConfirmed bool, acc *account.Account) error {

	if acc == nil {
		return errors.New("unable to update a <nil> account")
	}

	family, index, pubKey := marshalKeyDescriptor(
		acc.AuctioneerKey,
	)

	outPointHash, outPointIndex := marshalOutPoint(acc.OutPoint)

	var latestTx bytes.Buffer
	if err := acc.LatestTx.Serialize(&latestTx); err != nil {
		return err
	}

	params := postgres.UpdateAccountDiffParams{
		ID:                  id,
		TraderKey:           acc.TraderKeyRaw[:],
		TokenID:             acc.TokenID[:],
		Confirmed:           isConfirmed,
		Value:               int64(acc.Value),
		Expiry:              int64(acc.Expiry),
		AuctioneerKeyFamily: family,
		AuctioneerKeyIndex:  index,
		AuctioneerPublicKey: pubKey,
		BatchKey:            acc.BatchKey.SerializeCompressed(),
		Secret:              acc.Secret[:],
		State:               int16(acc.State),
		HeightHint:          int64(acc.HeightHint),
		OutPointHash:        outPointHash,
		OutPointIndex:       outPointIndex,
		LatestTx:            latestTx.Bytes(),
		UserAgent:           acc.UserAgent,
	}
	return txQueries.UpdateAccountDiff(ctx, params)
}

// modifyAccountWithTx modifies and updates an account using the provided
// queries struct.
func modifyAccountWithTx(ctx context.Context, txQueries *postgres.Queries,
	accKey *btcec.PublicKey,
	modifiers ...account.Modifier) (*account.Account, error) {

	traderKey := accKey.SerializeCompressed()
	row, err := txQueries.GetAccount(ctx, traderKey)
	switch {
	case err == pgx.ErrNoRows:
		return nil, NewAccountNotFoundError(accKey)

	case err != nil:
		return nil, fmt.Errorf("unable to update account: %v", err)
	}

	dbAccount, err := unmarshalAccount(row)
	if err != nil {
		return nil, fmt.Errorf("unable to update account: %v", err)
	}

	// Apply the given modifications to it and serialize it back.
	for _, modifier := range modifiers {
		modifier(dbAccount)
	}

	err = upsertAccountWithTx(ctx, txQueries, dbAccount)
	if err != nil {
		return nil, fmt.Errorf("unable to update account: %v", err)
	}

	return dbAccount, nil
}

// marshalOutPoint maps a wire.OutPoint to its serialized version used in
// the db.
func marshalOutPoint(op wire.OutPoint) ([]byte, int64) {
	return op.Hash[:], int64(op.Index)
}

// unmarshalOutPoint deserializes a wire.OutPoint from its serialized version
// used in the db.
func unmarshalOutPoint(hash []byte, index int64) (*wire.OutPoint, error) {
	opHash, err := chainhash.NewHash(hash)
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshal out point: %v", err)
	}
	return wire.NewOutPoint(opHash, uint32(index)), nil
}

// unmarshalReservation deserializes an account reservation from its
// serialized version used in the db.
func unmarshalReservation(
	row postgres.AccountReservation) (*account.Reservation, error) {

	errMsg := "unable to unmarshal reservation: %v"
	auctioneerKey, err := unmarshalKeyDescriptor(
		row.AuctioneerKeyFamily,
		row.AuctioneerKeyIndex,
		row.AuctioneerPublicKey,
	)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	batchKey, err := btcec.ParsePubKey(row.InitialBatchKey)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	var traderKeyRaw [33]byte
	copy(traderKeyRaw[:], row.TraderKey)

	return &account.Reservation{
		Value:           btcutil.Amount(row.Value),
		AuctioneerKey:   auctioneerKey,
		InitialBatchKey: batchKey,
		Expiry:          uint32(row.Expiry),
		HeightHint:      uint32(row.HeightHint),
		TraderKeyRaw:    traderKeyRaw,
	}, nil
}

// unmarshalAccount deserializes an account from its serialized version used
// in the db.
func unmarshalAccount(row postgres.Account) (*account.Account, error) {
	var (
		tokenID      lsat.TokenID
		traderKeyRaw [33]byte
		secret       [32]byte
	)

	errMsg := "unable to unmarshal account: %v"

	copy(traderKeyRaw[:], row.TraderKey)
	copy(secret[:], row.Secret)
	copy(tokenID[:], row.TokenID)

	auctioneerKey, err := unmarshalKeyDescriptor(
		row.AuctioneerKeyFamily,
		row.AuctioneerKeyIndex,
		row.AuctioneerPublicKey,
	)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	batchKey, err := btcec.ParsePubKey(row.BatchKey)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	outpoint, err := unmarshalOutPoint(row.OutPointHash, row.OutPointIndex)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	var latestTx *wire.MsgTx
	if len(row.LatestTx) > 0 {
		latestTx = &wire.MsgTx{}
		err = latestTx.Deserialize(bytes.NewReader(row.LatestTx))
		if err != nil {
			return nil, fmt.Errorf(errMsg, err)
		}
	}

	return &account.Account{
		TokenID:       tokenID,
		Value:         btcutil.Amount(row.Value),
		Expiry:        uint32(row.Expiry),
		TraderKeyRaw:  traderKeyRaw,
		AuctioneerKey: auctioneerKey,
		BatchKey:      batchKey,
		Secret:        secret,
		State:         account.State(row.State),
		HeightHint:    uint32(row.HeightHint),
		OutPoint:      *outpoint,
		LatestTx:      latestTx,
		UserAgent:     row.UserAgent,
	}, nil
}

// unmarshalAccountDiff deserializes an account diff from its serialized version
// used in the db.
func unmarshalAccountDiff(row postgres.AccountDiff) (*account.Account, error) {
	var (
		tokenID      lsat.TokenID
		traderKeyRaw [33]byte
		secret       [32]byte
	)

	errMsg := "unable to unmarshal account diff: %v"

	copy(traderKeyRaw[:], row.TraderKey)
	copy(secret[:], row.Secret)
	copy(tokenID[:], row.TokenID)

	auctioneerKey, err := unmarshalKeyDescriptor(
		row.AuctioneerKeyFamily,
		row.AuctioneerKeyIndex,
		row.AuctioneerPublicKey,
	)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	batchKey, err := btcec.ParsePubKey(row.BatchKey)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	outpoint, err := unmarshalOutPoint(row.OutPointHash, row.OutPointIndex)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	latestTx := &wire.MsgTx{}
	err = latestTx.Deserialize(bytes.NewReader(row.LatestTx))
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	return &account.Account{
		TokenID:       tokenID,
		Value:         btcutil.Amount(row.Value),
		Expiry:        uint32(row.Expiry),
		TraderKeyRaw:  traderKeyRaw,
		AuctioneerKey: auctioneerKey,
		BatchKey:      batchKey,
		Secret:        secret,
		State:         account.State(row.State),
		HeightHint:    uint32(row.HeightHint),
		OutPoint:      *outpoint,
		LatestTx:      latestTx,
		UserAgent:     row.UserAgent,
	}, nil
}
