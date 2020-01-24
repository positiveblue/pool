package agoradb

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/btcsuite/btcd/btcec"
	"github.com/coreos/etcd/clientv3"
	"github.com/lightninglabs/agora/client/clmscript"
)

var (
	// initialBatchKey serves as our initial global batch key. This key will
	// be incremented by the curve's base point every time a new batch is
	// cleared.
	initialBatchKeyBytes, _ = hex.DecodeString(
		"02824d0cbac65e01712124c50ff2cc74ce22851d7b444c1bf2ae66afefb8eaf27f",
	)
	initialBatchKey, _ = btcec.ParsePubKey(initialBatchKeyBytes, btcec.S256())

	// batchDir is the directory name under which we'll store all
	// transaction batch related information. This needs be prefixed with
	// topLevelDir to obtain the full path.
	batchDir = "batch"

	// perBatchKey is the database key we'll store our current per-batch key
	// under. This must be prefixed with batchDir and topLevelDir to obtain
	// the full path.
	perBatchKey = "key"

	// errPerBatchKeyNotFound is an error returned when we can't locate the
	// per-batch key at its expected path.
	errPerBatchKeyNotFound = errors.New("per-batch key not found")
)

// perBatchKeyPath returns the full path under which we store the current
// per-batch key.
func (s *EtcdStore) perBatchKeyPath() string {
	parts := []string{batchDir, perBatchKey}
	return s.getKeyPrefix(strings.Join(parts, keyDelimiter))
}

// perBatchKey returns the current per-batch key.
func (s *EtcdStore) perBatchKey(ctx context.Context) (*btcec.PublicKey, error) {
	resp, err := s.getSingleValue(
		ctx, s.perBatchKeyPath(), errPerBatchKeyNotFound,
	)
	if err != nil {
		return nil, err
	}

	var batchKey *btcec.PublicKey
	err = ReadElement(bytes.NewReader(resp.Kvs[0].Value), &batchKey)
	if err != nil {
		return nil, err
	}

	return batchKey, nil
}

// putPerBatchKeyOp returns an etcd operation to store the given key under the
// path of the per-batch key.
func (s *EtcdStore) putPerBatchKeyOp(key *btcec.PublicKey) (clientv3.Op, error) {
	perBatchKeyPath := s.perBatchKeyPath()
	var perBatchKeyBuf bytes.Buffer
	if err := WriteElement(&perBatchKeyBuf, key); err != nil {
		return clientv3.Op{}, err
	}
	return clientv3.OpPut(perBatchKeyPath, perBatchKeyBuf.String()), nil
}

// BatchKey returns the current per-batch key that must be used to tweak account
// trader keys with.
func (s *EtcdStore) BatchKey(ctx context.Context) (*btcec.PublicKey, error) {
	if !s.initialized {
		return nil, errNotInitialized
	}

	return s.perBatchKey(ctx)
}

// NextBatchKey updates the currently running batch key by incrementing it
// with the backing curve's base point.
func (s *EtcdStore) NextBatchKey(ctx context.Context) (*btcec.PublicKey, error) {
	if !s.initialized {
		return nil, errNotInitialized
	}

	// Obtain the current per-batch key, increment it by the curve's base
	// point, and store the result.
	perBatchKey, err := s.perBatchKey(ctx)
	if err != nil {
		return nil, err
	}

	newPerBatchKey := clmscript.IncrementKey(perBatchKey)

	op, err := s.putPerBatchKeyOp(newPerBatchKey)
	if err != nil {
		return nil, err
	}
	_, err = s.client.Do(ctx, op)
	if err != nil {
		return nil, err
	}

	return newPerBatchKey, err
}
