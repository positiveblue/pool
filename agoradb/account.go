package agoradb

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/btcsuite/btcd/btcec"
	"github.com/coreos/etcd/clientv3"
	conc "github.com/coreos/etcd/clientv3/concurrency"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/loop/lsat"
)

const (
	// reservationDir is the directory name key under which we'll store all
	// account reservations. This needs be prefixed with topLevelDir to
	// obtain the full path.
	reservationDir = "reservation"

	// accountDir is the directory name under which we'll store all
	// accounts. This needs be prefixed with topLevelDir to obtain the full
	// path.
	accountDir = "account"
)

var (
	// ErrAccountNotFound is an error returned when we attempt to retrieve
	// information about an account but it is not found.
	ErrAccountNotFound = errors.New("account not found")
)

// A compile-time constraint to ensure EtcdStore implements account.Store.
var _ account.Store = (*EtcdStore)(nil)

// getReservationKey returns the key for a reservation associated with the LSAT
// token ID. Assuming a token ID of 123456, the resulting key would be:
//	/bitcoin/clm/agora/reservation/123456
func (s *EtcdStore) getReservationKey(tokenID lsat.TokenID) string {
	parts := []string{reservationDir, tokenID.String()}
	reservationKey := strings.Join(parts, keyDelimiter)
	return s.getKeyPrefix(reservationKey)
}

// getAccountKey returns the key for an account associated with the trader key.
// Assuming a trader key of 123456, the resulting key would be:
//	/bitcoin/clm/agora/account/123456
func (s *EtcdStore) getAccountKey(traderKey *btcec.PublicKey) string {
	parts := []string{
		accountDir, hex.EncodeToString(traderKey.SerializeCompressed()),
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
		// First, make sure we have an active reservation for the LSAT
		// token associated with the account.
		reservationKey := s.getReservationKey(a.TokenID)
		reservation := stm.Get(reservationKey)
		if reservation == "" {
			return account.ErrNoReservation
		}

		stm.Del(reservationKey)
		stm.Put(s.getAccountKey(traderKey), buf.String())
		return nil
	})
	return err
}

// UpdateAccount updates an account in the database according to the given
// modifiers.
func (s *EtcdStore) UpdateAccount(ctx context.Context, account *account.Account,
	modifiers ...account.Modifier) error {

	if !s.initialized {
		return errNotInitialized
	}

	// Get the parsed key from the account.
	traderKey, err := account.TraderKey()
	if err != nil {
		return err
	}

	// Wrap the update in an STM and execute it.
	_, err = s.defaultSTM(ctx, func(stm conc.STM) error {
		return s.updateAccountSTM(stm, traderKey, modifiers...)
	})
	if err != nil {
		return err
	}

	// With the on-disk state successfully updated, apply the modifications
	// to the in-memory account state.
	for _, modifier := range modifiers {
		modifier(account)
	}

	return nil
}

// updateAccountSTM adds all operations necessary to update an account to the
// given STM transaction. If the account does not yet exist, the whole STM
// transaction will fail.
func (s *EtcdStore) updateAccountSTM(stm conc.STM, acctKey *btcec.PublicKey,
	modifiers ...account.Modifier) error {

	// Retrieve the account stored in the database. In STM an empty string
	// means that the key does not exist.
	k := s.getAccountKey(acctKey)
	resp := stm.Get(k)
	if resp == "" {
		return ErrAccountNotFound
	}
	dbAccount, err := deserializeAccount(bytes.NewReader([]byte(resp)))
	if err != nil {
		return err
	}

	// Apply the given modifications to it and serialize it back.
	for _, modifier := range modifiers {
		modifier(dbAccount)
	}

	var buf bytes.Buffer
	if err := serializeAccount(&buf, dbAccount); err != nil {
		return err
	}

	// Add the put operation to the queue.
	stm.Put(k, buf.String())
	return nil
}

// Account retrieves the account associated with the given trader key.
func (s *EtcdStore) Account(ctx context.Context,
	traderKey *btcec.PublicKey) (*account.Account, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	resp, err := s.getSingleValue(
		ctx, s.getAccountKey(traderKey), ErrAccountNotFound,
	)
	if err != nil {
		return nil, err
	}

	return deserializeAccount(bytes.NewReader(resp.Kvs[0].Value))
}

// Accounts retrieves all existing accounts.
func (s *EtcdStore) Accounts(ctx context.Context) ([]*account.Account, error) {
	if !s.initialized {
		return nil, errNotInitialized
	}

	k := s.getKeyPrefix(accountDir)
	resp, err := s.client.Get(ctx, k, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, nil
	}

	accounts := make([]*account.Account, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		acct, err := deserializeAccount(bytes.NewReader(kv.Value))
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, acct)
	}

	return accounts, nil
}

func serializeReservation(w io.Writer, reservation *account.Reservation) error {
	return WriteElements(
		w, reservation.AuctioneerKey, reservation.InitialBatchKey,
	)
}

func deserializeReservation(r io.Reader) (*account.Reservation, error) {
	var reservation account.Reservation
	err := ReadElements(
		r, &reservation.AuctioneerKey, &reservation.InitialBatchKey,
	)
	return &reservation, err
}

func serializeAccount(w io.Writer, account *account.Account) error {
	return WriteElements(
		w, account.TokenID, account.Value, account.Expiry,
		account.TraderKeyRaw, account.AuctioneerKey, account.BatchKey,
		account.Secret, account.State, account.HeightHint,
		account.OutPoint,
	)
}

func deserializeAccount(r io.Reader) (*account.Account, error) {
	var a account.Account
	err := ReadElements(
		r, &a.TokenID, &a.Value, &a.Expiry, &a.TraderKeyRaw,
		&a.AuctioneerKey, &a.BatchKey, &a.Secret, &a.State,
		&a.HeightHint, &a.OutPoint,
	)
	return &a, err
}
