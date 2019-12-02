package agoradb

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/coreos/etcd/clientv3"
)

var (
	byteOrder = binary.BigEndian

	errAlreadyInitialized = errors.New("db already initialized")
	errDbVersionMismatch  = errors.New("wrong db version")

	currentDbVersion = uint32(0)
)

var (
	// topLevelDir is the top level directory that we'll use to store all
	// the production agora data.
	topLevelDir = []byte("bitcoin/clm/agora")

	// versionPrefix is the key prefix that we'll use to store the current
	// version of the auction data for the target network.
	versionPrefix = []byte("version")

	// keyDelimiter is the special token that we'll use to delimit entries
	// in a key's path.
	keyDelimiter = []byte("/")
)

type Store interface {
	Init(ctx context.Context) error

	// TODO: add business logic methods to store/retrieve information.
}

type EtcdStore struct {
	client      *clientv3.Client
	networkID   []byte
	initialized bool
}

// NewEtcdStore creates a new etcd store instance. Chain indicates the chain to
// use by its genesis block hash. The specified user and password should be
// able to access al keys below that topLevelDir above.
func NewEtcdStore(activeNet chaincfg.Params,
	host, user, pass string) (*EtcdStore, error) {

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{host},
		DialTimeout: 5 * time.Second,
		Username:    user,
		Password:    pass,
	})
	if err != nil {
		return nil, err
	}

	s := &EtcdStore{
		client:    cli,
		networkID: []byte(activeNet.Name),
	}

	return s, nil
}

// getKeyPrefix returns the key prefix path for the given prefix.
func (s *EtcdStore) getKeyPrefix(prefix []byte) []byte {
	// /bitcoin/clm/agora/<network>/<prefix>.
	return bytes.Join(
		[][]byte{topLevelDir, s.networkID, prefix},
		keyDelimiter,
	)
}

// Init should be called the first time the database is created. It will
// initialize the necessary versioning state, and also ensure that the database
// hasn't already been created in the past.
func (s *EtcdStore) Init(ctx context.Context) error {
	if s.initialized {
		return errAlreadyInitialized
	}

	resp, err := s.client.Get(ctx, string(s.getKeyPrefix(versionPrefix)))
	if err != nil {
		return err
	}

	s.initialized = true

	if resp.Count == 0 {
		log.Infof("Initializing db with version %v", currentDbVersion)
		return s.setVersion(ctx, currentDbVersion)
	}

	version := byteOrder.Uint32(resp.Kvs[0].Value)

	log.Infof("Current db version %v, required version %v",
		version, currentDbVersion)

	if version != currentDbVersion {
		return errDbVersionMismatch
	}

	return nil
}

func (s *EtcdStore) setVersion(ctx context.Context, version uint32) error {
	key := s.getKeyPrefix(versionPrefix)

	scratch := make([]byte, 4)
	byteOrder.PutUint32(scratch, version)
	_, err := s.client.Put(ctx, string(key), string(scratch))

	return err
}
