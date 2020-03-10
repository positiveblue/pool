package agoradb

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/coreos/etcd/clientv3"
	"github.com/lightninglabs/agora/account"
)

const (
	// auctioneerOutputDir is the directory where we'll store the
	// information about the current auctioneer's master account output.
	auctioneerOutputDir = "auctioneerAcct"
)

var (
	// ErrNoAuctioneerAccount is returned when a caller attempts to fetch
	// the auctioneer account, but it hasn't been initialized yet.
	ErrNoAuctioneerAccount = fmt.Errorf("no auctioneer acct")
)

// auctioneerKey returns the key where we store the information about the
// auctioneer's master account output.
func (s *EtcdStore) auctioneerKey() string {
	return s.getKeyPrefix(auctioneerOutputDir)
}

// FetchAuctioneerAccount retrieves the current information pertaining to the
// current auctioneer output state.
func (s *EtcdStore) FetchAuctioneerAccount(ctx context.Context) (*account.Auctioneer, error) {
	if !s.initialized {
		return nil, errNotInitialized
	}

	acctBytes, err := s.getSingleValue(
		ctx, s.auctioneerKey(), ErrNoAuctioneerAccount,
	)
	if err != nil {
		return nil, err
	}

	// First, we'll need to fetch and de-serialize the base fields for the
	// auctioneer's output.
	acctBytesReader := bytes.NewReader(acctBytes.Kvs[0].Value)
	acct, err := deserializeAuctioneerAccount(acctBytesReader)
	if err != nil {
		return nil, err
	}

	// Next, we'll fetch the batch key as we store and rotate in in a
	// distinct name space.
	//
	// TODO(roasbeef): need stronger sync here? Use STM?
	batchKey, err := s.perBatchKey(ctx)
	if err != nil {
		return nil, err
	}
	copy(acct.BatchKey[:], batchKey.SerializeCompressed())

	return acct, nil
}

// UpdateAuctioneerAccount updates the current auctioneer output in-place.
//
// TODO(roasbeef): pass in STM struct?
func (s *EtcdStore) UpdateAuctioneerAccount(ctx context.Context,
	acct *account.Auctioneer) error {

	if !s.initialized {
		return errNotInitialized
	}

	var acctBytes bytes.Buffer
	if err := serializeAuctioneerAccount(&acctBytes, acct); err != nil {
		return err
	}

	// For this update, we aim to update both the batch key and the
	// auctioneer's state in a single transaction, so we'll create updates
	// to apply atomically below.
	updateAcctState := clientv3.OpPut(s.auctioneerKey(),
		acctBytes.String())
	updateBatchKey := clientv3.OpPut(
		s.perBatchKeyPath(), string(acct.BatchKey[:]),
	)

	// With our updates crafted, we'll attempt to update both these keys at
	// once.
	_, err := s.client.Txn(ctx).
		If().
		Then(updateBatchKey, updateAcctState).
		Commit()

	return err
}

func serializeAuctioneerAccount(w io.Writer, acct *account.Auctioneer) error {
	return WriteElements(
		w, acct.OutPoint, acct.Balance, acct.AuctioneerKey,
	)
}

func deserializeAuctioneerAccount(r io.Reader) (*account.Auctioneer, error) {
	var acct account.Auctioneer

	err := ReadElements(
		r, &acct.OutPoint, &acct.Balance, &acct.AuctioneerKey,
	)

	return &acct, err
}
