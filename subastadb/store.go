package subastadb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/multimutex"
	"go.etcd.io/etcd/clientv3"
	conc "go.etcd.io/etcd/clientv3/concurrency"
)

var (
	errNotInitialized     = errors.New("db not initialized")
	errAlreadyInitialized = errors.New("db already initialized")
	errDbVersionMismatch  = errors.New("wrong db version")

	currentDbVersion = uint32(0)

	etcdTimeout = 10 * time.Second

	// stmDefaultIsolation is the default isolation level we use for STM
	// transactions that manipulate accounts and orders. This is also the
	// default as declared in the concurrency package and offers the most
	// strict isolation.
	stmDefaultIsolation = conc.SerializableSnapshot
)

var (
	// topLevelDir is the top level directory that we'll use to store all
	// the production auction data.
	topLevelDir = "bitcoin/clm/subasta"

	// versionPrefix is the key prefix that we'll use to store the current
	// version of the auction data for the target network.
	versionPrefix = "version"

	// keyDelimiter is the special token that we'll use to delimit entries
	// in a key's path.
	keyDelimiter = "/"
)

type Store interface {
	Init(ctx context.Context) error

	account.Store

	order.Store

	// UpdateAuctioneerAccount updates the current auctioneer output
	// in-place and also updates the per batch key according to the state in
	// the auctioneer's account.
	UpdateAuctioneerAccount(context.Context, *account.Auctioneer) error

	// PersistBatchResult atomically updates all modified orders/accounts,
	// persists a snapshot of the batch and switches to the next batch ID.
	// If any single operation fails, the whole set of changes is rolled
	// back.
	PersistBatchResult(context.Context, []orderT.Nonce, [][]order.Modifier,
		[]*btcec.PublicKey, [][]account.Modifier, *account.Auctioneer,
		orderT.BatchID, *BatchSnapshot, *btcec.PublicKey,
		[]*chanenforcement.LifetimePackage) error

	// GetBatchSnapshot returns the self-contained snapshot of a batch with
	// the given ID as it was recorded at the time.
	GetBatchSnapshot(context.Context, orderT.BatchID) (*BatchSnapshot, error)
}

// BatchSnapshot holds a self-contained snapshot of a batch.
type BatchSnapshot struct {
	// BatchTx is the final, signed batch transaction for this batch.
	BatchTx *wire.MsgTx

	// BatchTxFee is the chain fee paid by the above batch tx.
	BatchTxFee btcutil.Amount

	// OrderBatch is the matched orders part of this batch.
	OrderBatch *matching.OrderBatch
}

// nonceMutex is a simple wrapper around a multimutex that casts 32-byte nonces
// to 32-byte hashes.
type nonceMutex struct {
	hashMutex *multimutex.HashMutex
}

func newNonceMutex() *nonceMutex {
	return &nonceMutex{
		hashMutex: multimutex.NewHashMutex(),
	}
}

// lock acquires the mutex for the given nonce(s).
func (n *nonceMutex) lock(nonces ...orderT.Nonce) {
	for _, nonce := range nonces {
		n.hashMutex.Lock(lntypes.Hash(nonce))
	}
}

// unlock releases the mutex for the given nonce(s). Note that the nonces
// should be provided in the same order as when locked.
func (n *nonceMutex) unlock(nonces ...orderT.Nonce) {
	// Unlock in the opposite order of locking.
	for i := len(nonces) - 1; i >= 0; i-- {
		nonce := nonces[i]
		n.hashMutex.Unlock(lntypes.Hash(nonce))
	}
}

type EtcdStore struct {
	client      *clientv3.Client
	networkID   string
	initialized bool

	// activeOrdersCache is map where we cache results from calls to fetch
	// active orders. Will be created and filled on store initialization.
	activeOrdersCache    map[orderT.Nonce]order.ServerOrder
	activeOrdersCacheMtx sync.RWMutex

	// nonceMtx is a mutex we'll lock when doing write operations for a
	// particular nonce to the DB, in order to ensure concurrent writes
	// don't lead to inconsitencies between the cache and the DB.
	nonceMtx *nonceMutex
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
		client:            cli,
		networkID:         activeNet.Name,
		nonceMtx:          newNonceMutex(),
		activeOrdersCache: make(map[orderT.Nonce]order.ServerOrder),
	}

	return s, nil
}

// getKeyPrefix returns the key prefix path for the given prefix.
func (s *EtcdStore) getKeyPrefix(prefix string) string {
	// bitcoin/clm/subasta/<network>/<prefix>.
	return strings.Join(
		[]string{topLevelDir, s.networkID, prefix}, keyDelimiter,
	)
}

// Init initializes the necessary versioning state if the database hasn't
// already been created in the past.
func (s *EtcdStore) Init(ctx context.Context) error {
	if s.initialized {
		return errAlreadyInitialized
	}

	resp, err := s.client.Get(ctx, s.getKeyPrefix(versionPrefix))
	if err != nil {
		return err
	}

	s.initialized = true

	if resp.Count == 0 {
		log.Infof("Initializing db with version %v", currentDbVersion)
		return s.firstTimeInit(ctx, currentDbVersion)
	}

	version, err := strconv.Atoi(string(resp.Kvs[0].Value))
	if err != nil {
		return err
	}

	log.Infof("Current db version %v, latest version %v", version,
		currentDbVersion)

	if uint32(version) != currentDbVersion {
		return errDbVersionMismatch
	}

	// We end initialization by filling the active orders cache.
	return s.fillActiveOrdersCache(ctx)
}

// firstTimeInit stores all initial required key-value pairs throughout the
// store's initialization atomically.
func (s *EtcdStore) firstTimeInit(ctx context.Context, version uint32) error {
	versionKey := s.getKeyPrefix(versionPrefix)

	// Wrap the update in an STM and execute it.
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		// Write initial version number.
		stm.Put(versionKey, strconv.Itoa(int(version)))

		// TODO(roasbeef): insert place holder auctioneer acct?

		// Store the starting batch key.
		return s.putPerBatchKeySTM(stm, InitialBatchKey)
	})
	return err
}

// getSingleValue is a helper method to retrieve the value for a key that should
// only have a single value mapped to it. If no value is found, the provided
// errNoValue error is returned.
func (s *EtcdStore) getSingleValue(ctx context.Context, key string,
	errNoValue error) (*clientv3.GetResponse, error) {

	resp, err := s.client.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, errNoValue
	}
	if len(resp.Kvs) > 1 {
		return nil, fmt.Errorf("invalid entry for key %s, found %d "+
			"keys, expected %d", key, len(resp.Kvs), 1)
	}

	return resp, nil
}

// getAllValuesByPrefix reads multiple keys from the etcd database and returns
// their content as a map of byte slices, keyed by the storage key.
func (s *EtcdStore) getAllValuesByPrefix(mainCtx context.Context,
	prefix string) (map[string][]byte, error) {

	ctx, cancel := context.WithTimeout(mainCtx, etcdTimeout)
	defer cancel()

	resp, err := s.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		result[string(kv.Key)] = kv.Value
	}
	return result, nil
}

// defaultSTM returns an STM transaction wrapper for the store's etcd client
// with the default isolation level that is suitable for manipulating accounts
// and orders during the order submit phase.
func (s *EtcdStore) defaultSTM(ctx context.Context, apply func(conc.STM) error) (
	*clientv3.TxnResponse, error) {

	ctxt, cancel := context.WithTimeout(ctx, etcdTimeout)
	defer cancel()
	return conc.NewSTM(
		s.client, apply, conc.WithAbortContext(ctxt),
		conc.WithIsolation(stmDefaultIsolation),
	)
}
