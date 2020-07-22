package subastadb

import (
	"bytes"
	"context"
	"io"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/subasta/account"
	conc "go.etcd.io/etcd/clientv3/concurrency"
)

const (
	// auctioneerOutputDir is the directory where we'll store the
	// information about the current auctioneer's master account output.
	auctioneerOutputDir = "auctioneerAcct"
)

// auctioneerKey returns the key where we store the information about the
// auctioneer's master account output.
func (s *EtcdStore) auctioneerKey() string {
	return s.getKeyPrefix(auctioneerOutputDir)
}

// FetchAuctioneerAccount retrieves the current information pertaining to the
// current auctioneer output state.
func (s *EtcdStore) FetchAuctioneerAccount(ctx context.Context) (
	*account.Auctioneer, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	acctBytes, err := s.getSingleValue(
		ctx, s.auctioneerKey(), account.ErrNoAuctioneerAccount,
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

// UpdateAuctioneerAccount updates the current auctioneer output in-place and
// also updates the per batch key according to the state in the auctioneer's
// account.
func (s *EtcdStore) UpdateAuctioneerAccount(ctx context.Context,
	acct *account.Auctioneer) error {

	if !s.initialized {
		return errNotInitialized
	}

	// For this update, we aim to update both the batch key and the
	// auctioneer's state in a single transaction, so we'll create updates
	// to apply atomically below.
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		err := s.updateAuctioneerAccountSTM(stm, acct)
		if err != nil {
			return err
		}

		key, err := btcec.ParsePubKey(acct.BatchKey[:], btcec.S256())
		if err != nil {
			return err
		}
		return s.putPerBatchKeySTM(stm, key)
	})
	return err
}

// updateAuctioneerAccountSTM adds all operations necessary to update the master
// account to the given STM transaction.
func (s *EtcdStore) updateAuctioneerAccountSTM(stm conc.STM,
	acct *account.Auctioneer) error {

	var acctBytes bytes.Buffer
	if err := serializeAuctioneerAccount(&acctBytes, acct); err != nil {
		return err
	}

	stm.Put(s.auctioneerKey(), acctBytes.String())
	return nil
}

func serializeAuctioneerAccount(w io.Writer, acct *account.Auctioneer) error {
	return WriteElements(
		w, acct.OutPoint, acct.Balance, acct.AuctioneerKey,
		acct.IsPending,
	)
}

func deserializeAuctioneerAccount(r io.Reader) (*account.Auctioneer, error) {
	var acct account.Auctioneer

	err := ReadElements(
		r, &acct.OutPoint, &acct.Balance, &acct.AuctioneerKey,
		&acct.IsPending,
	)

	return &acct, err
}
