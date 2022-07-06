package subastadb

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightningnetwork/lnd/tlv"
	conc "go.etcd.io/etcd/client/v3/concurrency"
)

const (
	// reservationDir is the directory name key under which we'll store all
	// account reservations. This needs to be prefixed with topLevelDir to
	// obtain the full path.
	reservationDir = "reservation"

	// accountDir is the directory name under which we'll store all
	// accounts. This needs to be prefixed with topLevelDir to obtain the
	// full path.
	accountDir = "account"

	// accountDir is the directory name under which we'll store an account's
	// pending modifications. This needs be prefixed with the account's key
	// within the store to obtain the full path.
	accountDiffDir = "diff"

	// accountTlvKey is the key that we'll use to store the account's
	// additional tlv encoded data. From the top level directory, this path
	// is:
	//
	// bitcoin/clm/subasta/<network>/account/<trader-pubkey>/tlv.
	accountTlvKey = "tlv"

	// accountKeyLen is the expected number of parts in an account's main
	// key without any additional sub path.
	// Example: bitcoin/clm/subasta/<network>/account/123456.
	accountKeyLen = 6
)

var (
	// ErrAccountDiffAlreadyExists is an error returned when we attempt to
	// store an account diff, but one already exists.
	ErrAccountDiffAlreadyExists = errors.New("found existing account diff")
)

// A compile-time constraint to ensure EtcdStore implements account.Store.
var _ account.Store = (*EtcdStore)(nil)

// getReservationKey returns the key for a reservation associated with the LSAT
// token ID. Assuming a token ID of 123456, the resulting key would be:
//	bitcoin/clm/subasta/<network>/reservation/123456
func (s *EtcdStore) getReservationKey(tokenID lsat.TokenID) string {
	parts := []string{reservationDir, tokenID.String()}
	reservationKey := strings.Join(parts, keyDelimiter)
	return s.getKeyPrefix(reservationKey)
}

// getAccountKey returns the key for an account associated with the trader key.
// Assuming a trader key of 123456, the resulting key would be:
//	bitcoin/clm/subasta/<network>/account/123456
func (s *EtcdStore) getAccountKey(traderKey *btcec.PublicKey) string {
	parts := []string{
		accountDir, hex.EncodeToString(traderKey.SerializeCompressed()),
	}
	accountKey := strings.Join(parts, keyDelimiter)
	return s.getKeyPrefix(accountKey)
}

// getAccountDiffKey returns the key for the diff of an account. Assuming a
// trader key of 123456, the resulting key would be:
//	bitcoin/clm/subasta/<network>/account/123456/diff
func (s *EtcdStore) getAccountDiffKey(traderKey *btcec.PublicKey) string {
	parts := []string{
		accountDir, hex.EncodeToString(traderKey.SerializeCompressed()),
		accountDiffDir,
	}
	accountKey := strings.Join(parts, keyDelimiter)
	return s.getKeyPrefix(accountKey)
}

// getAccountTlvKey returns the key for the tlv encoded additional data of an
// account. Assuming a trader key of 123456, the resulting key would be:
//	bitcoin/clm/subasta/<network>/account/123456/tlv
func (s *EtcdStore) getAccountTlvKey(traderKey *btcec.PublicKey) string {
	parts := []string{
		accountDir, hex.EncodeToString(traderKey.SerializeCompressed()),
		accountTlvKey,
	}
	accountKey := strings.Join(parts, keyDelimiter)
	return s.getKeyPrefix(accountKey)
}

// HasReservation determines whether we have an existing reservation associated
// with a token. account.ErrNoReservation is returned if a reservation does not
// exist.
func (s *EtcdStore) HasReservation(ctx context.Context,
	tokenID lsat.TokenID) (*account.Reservation, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	resp, err := s.getSingleValue(
		ctx, s.getReservationKey(tokenID), account.ErrNoReservation,
	)
	if err != nil {
		return nil, err
	}

	return deserializeReservation(bytes.NewReader(resp.Kvs[0].Value))
}

// HasReservationForKey determines whether we have an existing
// reservation associated with a trader key. ErrNoReservation is
// returned if a reservation does not exist.
func (s *EtcdStore) HasReservationForKey(ctx context.Context,
	traderKey *btcec.PublicKey) (*account.Reservation, *lsat.TokenID,
	error) {

	if !s.initialized {
		return nil, nil, errNotInitialized
	}

	resp, err := s.getAllValuesByPrefix(ctx, s.getKeyPrefix(reservationDir))
	if err != nil {
		return nil, nil, err
	}

	var traderKeyRaw [33]byte
	copy(traderKeyRaw[:], traderKey.SerializeCompressed())

	for k, v := range resp {
		res, err := deserializeReservation(bytes.NewReader(v))
		if err != nil {
			return nil, nil, err
		}

		// Parse the token ID from the last part of the key.
		keyParts := strings.Split(k, keyDelimiter)
		tokenPart := keyParts[len(keyParts)-1]
		tokenID, err := lsat.MakeIDFromString(tokenPart)
		if err != nil {
			return nil, nil, err
		}

		if res.TraderKeyRaw == traderKeyRaw {
			return res, &tokenID, nil
		}
	}

	return nil, nil, account.ErrNoReservation
}

// ReserveAccount makes a reservation for an auctioneer key for a trader
// associated to a token.
func (s *EtcdStore) ReserveAccount(ctx context.Context,
	tokenID lsat.TokenID, reservation *account.Reservation) error {

	if !s.initialized {
		return errNotInitialized
	}

	reservationKey := s.getReservationKey(tokenID)
	var buf bytes.Buffer
	if err := serializeReservation(&buf, reservation); err != nil {
		return err
	}

	// Wrap the update in an STM and execute it.
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		stm.Put(reservationKey, buf.String())
		return nil
	})
	return err
}

// CompleteReservation completes a reservation for an account and adds a record
// for it within the store.
func (s *EtcdStore) CompleteReservation(ctx context.Context,
	a *account.Account) error {

	if !s.initialized {
		return errNotInitialized
	}

	// Make sure we can serialize the new account before trying to store it.
	var buf bytes.Buffer
	if err := serializeAccount(&buf, a); err != nil {
		return err
	}
	traderKey, err := a.TraderKey()
	if err != nil {
		return err
	}

	// If we do, we'll need to remove the reservation and add the account
	// atomically. Create both operations in an STM to commit them under the
	// same database transaction.
	_, err = s.defaultSTM(ctx, func(stm conc.STM) error {
		err := s.removeReservation(stm, a.TokenID)
		if err != nil {
			return err
		}

		accountKey := s.getAccountKey(traderKey)
		acc := stm.Get(accountKey)
		if acc != "" {
			return account.ErrAccountExists
		}

		stm.Put(s.getAccountKey(traderKey), buf.String())

		// Now write all remaining additional data as a tlv encoded
		// stream.
		return s.storeAccountTlv(stm, a, traderKey)
	})
	return err
}

// storeAccountTlv stores an account's tlv encoded additional data using the
// given STM instance.
func (s *EtcdStore) storeAccountTlv(stm conc.STM, a *account.Account,
	acctKey *btcec.PublicKey) error {

	k := s.getAccountTlvKey(acctKey)
	var buf bytes.Buffer
	if err := serializeAccountTlvData(&buf, a); err != nil {
		return err
	}
	stm.Put(k, buf.String())
	return nil
}

// RemoveReservation deletes a reservation identified by the LSAT ID.
func (s *EtcdStore) RemoveReservation(ctx context.Context,
	id lsat.TokenID) error {

	if !s.initialized {
		return errNotInitialized
	}

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		return s.removeReservation(stm, id)
	})
	return err
}

// removeReservation removes a single reservation by its LSAT ID in the given
// STM transaction. If no reservation exists, the whole STM transaction will
// fail.
func (s *EtcdStore) removeReservation(stm conc.STM, id lsat.TokenID) error {
	// First, make sure we have an active reservation for the LSAT
	// token associated with the account.
	reservationKey := s.getReservationKey(id)
	reservation := stm.Get(reservationKey)
	if reservation == "" {
		return account.ErrNoReservation
	}

	stm.Del(reservationKey)
	return nil
}

// UpdateAccount updates an account in the database according to the given
// modifiers.
func (s *EtcdStore) UpdateAccount(ctx context.Context, a *account.Account,
	modifiers ...account.Modifier) (*account.Account, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	s.accountUpdateMtx.Lock()
	defer s.accountUpdateMtx.Unlock()

	// Get the parsed key from the account.
	traderKey, err := a.TraderKey()
	if err != nil {
		return nil, errNotInitialized
	}

	var dbAccount *account.Account
	// Wrap the update in an STM and execute it.
	_, err = s.defaultSTM(ctx, func(stm conc.STM) error {
		var updateErr error
		dbAccount, updateErr = s.updateAccountSTM(
			stm, traderKey, modifiers...,
		)
		return updateErr
	})
	if err != nil {
		return nil, err
	}

	// Optionally mirror account to SQL.
	UpdateAccountsSQL(ctx, s.sqlMirror, dbAccount)

	return dbAccount, nil
}

// updateAccountSTM adds all operations necessary to update an account to the
// given STM transaction. If the account does not yet exist, the whole STM
// transaction will fail.
func (s *EtcdStore) updateAccountSTM(stm conc.STM, acctKey *btcec.PublicKey,
	modifiers ...account.Modifier) (*account.Account, error) {

	// Retrieve the account stored in the database. In STM an empty string
	// means that the key does not exist.
	k := s.getAccountKey(acctKey)
	resp := stm.Get(k)
	if resp == "" {
		return nil, NewAccountNotFoundError(acctKey)
	}
	dbAccount, err := deserializeAccount(bytes.NewReader([]byte(resp)))
	if err != nil {
		return nil, err
	}

	// Decode any additional data that's stored in a tlv encoded stream.
	tlvKey := s.getAccountTlvKey(acctKey)
	err = deserializeAccountTlvData(
		strings.NewReader(stm.Get(tlvKey)), dbAccount,
	)
	if err != nil {
		return nil, err
	}

	// Apply the given modifications to it and serialize it back.
	for _, modifier := range modifiers {
		modifier(dbAccount)
	}

	var buf bytes.Buffer
	if err := serializeAccount(&buf, dbAccount); err != nil {
		return nil, err
	}

	// Add the put operation to the queue.
	stm.Put(k, buf.String())

	// Now write all remaining additional data as a tlv encoded stream.
	if err := s.storeAccountTlv(stm, dbAccount, acctKey); err != nil {
		return nil, err
	}

	return dbAccount, nil
}

// StoreAccount inserts a new account in the db.
func (s *EtcdStore) StoreAccount(ctx context.Context,
	acc *account.Account) error {

	if !s.initialized {
		return errNotInitialized
	}

	// Make sure we can serialize the new account before trying to store it.
	var buf bytes.Buffer
	if err := serializeAccount(&buf, acc); err != nil {
		return err
	}
	traderKey, err := acc.TraderKey()
	if err != nil {
		return err
	}

	// If we do, we'll need to remove the reservation and add the account
	// atomically. Create both operations in an STM to commit them under the
	// same database transaction.
	_, err = s.defaultSTM(ctx, func(stm conc.STM) error {
		accountKey := s.getAccountKey(traderKey)
		if stm.Get(accountKey) != "" {
			return account.ErrAccountExists
		}

		stm.Put(s.getAccountKey(traderKey), buf.String())

		// Now write all remaining additional data as a tlv encoded
		// stream.
		return s.storeAccountTlv(stm, acc, traderKey)
	})
	return err
}

// Reservations returns all the reservation in the db.
func (s *EtcdStore) Reservations(ctx context.Context) ([]*account.Reservation,
	[]lsat.TokenID, error) {

	if !s.initialized {
		return nil, nil, errNotInitialized
	}

	resp, err := s.getAllValuesByPrefix(ctx, s.getKeyPrefix(reservationDir))
	if err != nil {
		return nil, nil, err
	}

	reservations := make([]*account.Reservation, 0, len(resp))
	tokens := make([]lsat.TokenID, 0, len(resp))
	for k, v := range resp {
		res, err := deserializeReservation(bytes.NewReader(v))
		if err != nil {
			return nil, nil, err
		}
		reservations = append(reservations, res)

		// Parse the token ID from the last part of the key.
		keyParts := strings.Split(k, keyDelimiter)
		tokenPart := keyParts[len(keyParts)-1]
		tokenID, err := lsat.MakeIDFromString(tokenPart)
		if err != nil {
			return nil, nil, err
		}
		tokens = append(tokens, tokenID)
	}
	return reservations, tokens, nil
}

// CreateAccountDiff inserts a new account diff in the db.
func (s *EtcdStore) CreateAccountDiff(ctx context.Context,
	acc *account.Account) error {

	if !s.initialized {
		return errNotInitialized
	}

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		var buf bytes.Buffer
		if err := serializeAccount(&buf, acc); err != nil {
			return err
		}

		traderKey, err := acc.TraderKey()
		if err != nil {
			return err
		}
		accountDiffKey := s.getAccountDiffKey(traderKey)
		stm.Put(accountDiffKey, buf.String())
		return nil
	})
	return err
}

// ListAccountDiffs returns all the account diffs in the db.
func (s *EtcdStore) ListAccountDiffs(ctx context.Context) ([]*account.Account,
	error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	resp, err := s.getAllValuesByPrefix(ctx, s.getKeyPrefix(accountDir))
	if err != nil {
		return nil, err
	}

	accounts := make([]*account.Account, 0, len(resp))
	for k, v := range resp {
		if !strings.HasSuffix(k, "/diff") {
			continue
		}

		acct, err := deserializeAccount(bytes.NewReader(v))
		if err != nil {
			return nil, err
		}

		// Decode any additional data that's stored in a tlv encoded
		// stream. If there is no data in the map we'll just get a nil
		// byte slice from the map which the reader and tlv decoder can
		// handle.
		traderKey, err := acct.TraderKey()
		if err != nil {
			return nil, err
		}
		tlvKey := s.getAccountTlvKey(traderKey)
		err = deserializeAccountTlvData(
			bytes.NewReader(resp[tlvKey]), acct,
		)
		if err != nil {
			return nil, err
		}

		accounts = append(accounts, acct)
	}

	return accounts, nil
}

// StoreAccountDiff stores a pending set of updates that should be applied to an
// account after an invocation of CommitAccountDiff.
//
// In contrast to UpdateAccount, this should be used whenever we need to stage a
// pending update of the account that will be committed at some later point.
func (s *EtcdStore) StoreAccountDiff(ctx context.Context,
	traderKey *btcec.PublicKey, modifiers []account.Modifier) error {

	if !s.initialized {
		return errNotInitialized
	}

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		// First, we'll make sure the account we're attempting to store
		// a diff for exists.
		accountKey := s.getAccountKey(traderKey)
		rawAccount := stm.Get(accountKey)
		if len(rawAccount) == 0 {
			return NewAccountNotFoundError(traderKey)
		}

		// We'll also make sure a diff is not already present.
		accountDiffKey := s.getAccountDiffKey(traderKey)
		rawAccountDiff := stm.Get(accountDiffKey)
		if len(rawAccountDiff) > 0 {
			return ErrAccountDiffAlreadyExists
		}

		// Then, we'll deserialize the account, apply the diff, and
		// stage it.
		acct, err := deserializeAccount(strings.NewReader(rawAccount))
		if err != nil {
			return err
		}

		var buf bytes.Buffer
		acctDiff := acct.Copy(modifiers...)
		if err := serializeAccount(&buf, acctDiff); err != nil {
			return err
		}

		stm.Put(accountDiffKey, buf.String())
		return nil
	})
	return err
}

// CommitAccountDiff commits the latest stored pending set of updates for an
// account after a successful modification. If a diff does not exist,
// account.ErrNoDiff is returned.
func (s *EtcdStore) CommitAccountDiff(ctx context.Context,
	traderKey *btcec.PublicKey) error {

	if !s.initialized {
		return errNotInitialized
	}

	s.accountUpdateMtx.Lock()
	defer s.accountUpdateMtx.Unlock()

	var rawAccountDiff string
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		accountKey := s.getAccountKey(traderKey)
		if len(stm.Get(accountKey)) == 0 {
			return NewAccountNotFoundError(traderKey)
		}

		accountDiffKey := s.getAccountDiffKey(traderKey)
		rawAccountDiff = stm.Get(accountDiffKey)
		if len(rawAccountDiff) == 0 {
			return account.ErrNoDiff
		}

		stm.Put(accountKey, rawAccountDiff)
		stm.Del(accountDiffKey)

		return nil
	})
	if err != nil {
		return err
	}

	// Optionally update the accunt in SQL.
	if s.sqlMirror != nil {
		dbAccount, err := deserializeAccount(
			bytes.NewReader([]byte(rawAccountDiff)),
		)
		if err != nil {
			return err
		}

		UpdateAccountsSQL(ctx, s.sqlMirror, dbAccount)
	}

	return nil
}

// UpdateAccountDiff updates an account's pending diff.
func (s *EtcdStore) UpdateAccountDiff(ctx context.Context,
	accountKey *btcec.PublicKey, modifiers []account.Modifier) error {

	if !s.initialized {
		return errNotInitialized
	}

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		// First, we'll make sure the account we're attempting to store
		// a diff for exists.
		dbAccountKey := s.getAccountKey(accountKey)
		if len(stm.Get(dbAccountKey)) == 0 {
			return NewAccountNotFoundError(accountKey)
		}

		// We'll also make sure a diff already exists.
		dbAccountDiffKey := s.getAccountDiffKey(accountKey)
		rawAccountDiff := stm.Get(dbAccountDiffKey)
		if len(rawAccountDiff) == 0 {
			return account.ErrNoDiff
		}

		// Then, we'll update the staged account diff.
		acctDiff, err := deserializeAccount(
			strings.NewReader(rawAccountDiff),
		)
		if err != nil {
			return err
		}

		var buf bytes.Buffer
		newAcctDiff := acctDiff.Copy(modifiers...)
		if err := serializeAccount(&buf, newAcctDiff); err != nil {
			return err
		}

		stm.Put(dbAccountDiffKey, buf.String())
		return nil
	})
	return err
}

// DeleteAccountDiff deletes an account's pending diff.
func (s *EtcdStore) DeleteAccountDiff(ctx context.Context,
	accountKey *btcec.PublicKey) error {

	if !s.initialized {
		return errNotInitialized
	}

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		stm.Del(s.getAccountDiffKey(accountKey))
		return nil
	})
	return err
}

// Account retrieves the account associated with the given trader key.  The
// boolean indicates whether the account's diff should be returned instead. If a
// diff does not exist, then the existing account state is returned.
func (s *EtcdStore) Account(ctx context.Context,
	traderKey *btcec.PublicKey, includeDiff bool) (*account.Account, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	var acct *account.Account
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		var err error

		accountDiffKey := s.getAccountDiffKey(traderKey)
		rawAccountDiff := stm.Get(accountDiffKey)

		// If we need to return the account's diff, and one exists,
		// return it.
		if includeDiff && len(rawAccountDiff) > 0 {
			acct, err = deserializeAccount(
				strings.NewReader(rawAccountDiff),
			)
		} else {
			// Otherwise, return the existing account state.
			accountKey := s.getAccountKey(traderKey)
			rawAccount := stm.Get(accountKey)
			if len(rawAccount) == 0 {
				return NewAccountNotFoundError(traderKey)
			}

			acct, err = deserializeAccount(
				strings.NewReader(rawAccount),
			)
		}
		if err != nil {
			return err
		}

		// Decode any additional data that's stored in a tlv encoded
		// stream.
		tlvKey := s.getAccountTlvKey(traderKey)
		return deserializeAccountTlvData(
			strings.NewReader(stm.Get(tlvKey)), acct,
		)
	})
	return acct, err
}

// Accounts retrieves all existing accounts. If an account has a diff that is
// not yet committed, the diff will not be included. To get an account with its
// diff applied, query it individually.
func (s *EtcdStore) Accounts(ctx context.Context) ([]*account.Account, error) {
	if !s.initialized {
		return nil, errNotInitialized
	}

	resp, err := s.getAllValuesByPrefix(ctx, s.getKeyPrefix(accountDir))
	if err != nil {
		return nil, err
	}

	accounts := make([]*account.Account, 0, len(resp))
	for k, v := range resp {
		// We queried by prefix and will therefore also get keys for
		// account diffs or other extra data. We only want the main
		// account value here so we skip everything else.
		parts := strings.Split(k, keyDelimiter)
		if len(parts) != accountKeyLen {
			continue
		}

		acct, err := deserializeAccount(bytes.NewReader(v))
		if err != nil {
			return nil, err
		}

		// Decode any additional data that's stored in a tlv encoded
		// stream. If there is no data in the map we'll just get a nil
		// byte slice from the map which the reader and tlv decoder can
		// handle.
		traderKey, err := acct.TraderKey()
		if err != nil {
			return nil, err
		}
		tlvKey := s.getAccountTlvKey(traderKey)
		err = deserializeAccountTlvData(
			bytes.NewReader(resp[tlvKey]), acct,
		)
		if err != nil {
			return nil, err
		}

		accounts = append(accounts, acct)
	}

	return accounts, nil
}

func serializeReservation(w *bytes.Buffer, reservation *account.Reservation) error {
	return WriteElements(
		w, reservation.Value, reservation.AuctioneerKey,
		reservation.InitialBatchKey, reservation.Expiry,
		reservation.HeightHint, reservation.TraderKeyRaw,
	)
}

func deserializeReservation(r io.Reader) (*account.Reservation, error) {
	var reservation account.Reservation
	err := ReadElements(
		r, &reservation.Value, &reservation.AuctioneerKey,
		&reservation.InitialBatchKey, &reservation.Expiry,
		&reservation.HeightHint, &reservation.TraderKeyRaw,
	)
	return &reservation, err
}

func serializeAccount(w *bytes.Buffer, a *account.Account) error {
	err := WriteElements(
		w, a.TokenID, a.Value, a.Expiry, a.TraderKeyRaw,
		a.AuctioneerKey, a.BatchKey, a.Secret, a.State,
		a.HeightHint, a.OutPoint,
	)
	if err != nil {
		return err
	}

	// The latest transaction is not known while it's pending open.
	if a.State != account.StatePendingOpen {
		return WriteElement(w, a.LatestTx)
	}
	return nil
}

func deserializeAccount(r io.Reader) (*account.Account, error) {
	var a account.Account
	err := ReadElements(
		r, &a.TokenID, &a.Value, &a.Expiry, &a.TraderKeyRaw,
		&a.AuctioneerKey, &a.BatchKey, &a.Secret, &a.State,
		&a.HeightHint, &a.OutPoint,
	)
	if err != nil {
		return nil, err
	}

	// The latest transaction is not known while it's pending open.
	if a.State != account.StatePendingOpen {
		if err := ReadElement(r, &a.LatestTx); err != nil {
			return nil, err
		}
	}
	return &a, nil
}

// deserializeAccountTlvData attempts to decode the remaining bytes in the
// supplied reader by interpreting it as a tlv stream. If successful any
// non-default values of the additional data will be set on the given account.
func deserializeAccountTlvData(r io.Reader, a *account.Account) error {
	var (
		userAgent []byte
	)

	tlvStream, err := tlv.NewStream(
		tlv.MakePrimitiveRecord(userAgentType, &userAgent),
	)
	if err != nil {
		return err
	}

	parsedTypes, err := tlvStream.DecodeWithParsedTypes(r)
	if err != nil {
		return err
	}

	// No need setting the user agent if it wasn't parsed from the stream.
	if t, ok := parsedTypes[userAgentType]; ok && t == nil {
		a.UserAgent = string(userAgent)
	}

	return nil
}

// serializeAccountTlvData encodes all additional data of an account as a single
// tlv stream.
func serializeAccountTlvData(w io.Writer, a *account.Account) error {
	userAgent := []byte(a.UserAgent)

	var tlvRecords []tlv.Record

	// No need adding an empty record.
	if len(userAgent) > 0 {
		tlvRecords = append(tlvRecords, tlv.MakePrimitiveRecord(
			userAgentType, &userAgent,
		))
	}

	tlvStream, err := tlv.NewStream(tlvRecords...)
	if err != nil {
		return err
	}

	return tlvStream.Encode(w)
}
