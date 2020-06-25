package agoradb

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/llm/clmscript"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
	conc "go.etcd.io/etcd/clientv3/concurrency"
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

	// batchStatusKey is the key we'll use to store the current state of a
	// given batch. The state is either a 0 or 1. 0 means the batch is
	// pending, while 1 means the batch has been finalized. A batch is
	// finalized once it has been confirmed.
	batchStatusKey = "status"

	// errPerBatchKeyNotFound is an error returned when we can't locate the
	// per-batch key at its expected path.
	errPerBatchKeyNotFound = errors.New("per-batch key not found")

	// errBatchSnapshotNotFound is an error returned when we can't locate
	// the batch snapshot that was requested.
	//
	// NOTE: The client relies on this exact error for recovery purposes.
	// When modifying it, it should also be updated at the client level. The
	// client cannot import this error since the server code is private.
	errBatchSnapshotNotFound = errors.New("batch snapshot not found")

	// ErrNoBatchExists is returned when a caller attempts to query for a
	// batch by it's ID, yet one isn't found.
	ErrNoBatchExists = errors.New("no batch found")
)

// perBatchKeyPath returns the full path under which we store the current
// per-batch key.
func (s *EtcdStore) perBatchKeyPath() string {
	// bitcoin/clm/agora/<network>/batch/key.
	parts := []string{batchDir, perBatchKey}
	return s.getKeyPrefix(strings.Join(parts, keyDelimiter))
}

// batchSnapshotKeyPath returns the full path under which we store a batch
// snapshot.
func (s *EtcdStore) batchSnapshotKeyPath(id orderT.BatchID) string {
	// bitcoin/clm/agora/<network>/batch/<batchID>.
	parts := []string{batchDir, hex.EncodeToString(id[:])}
	return s.getKeyPrefix(strings.Join(parts, keyDelimiter))
}

// batchStatusKeyPath returns the full path under which we store a batch
// status.
func (s *EtcdStore) batchStatusKeyPath(id orderT.BatchID) string {
	// bitcoin/clm/agora/<network>/batch/<batchID>/status.
	parts := []string{batchDir, hex.EncodeToString(id[:]), batchStatusKey}
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

// updateAccountSTM adds all operations necessary to store the per batch key to
// the given STM transaction.
func (s *EtcdStore) putPerBatchKeySTM(stm conc.STM, key *btcec.PublicKey) error {
	perBatchKeyPath := s.perBatchKeyPath()
	var perBatchKeyBuf bytes.Buffer
	if err := WriteElement(&perBatchKeyBuf, key); err != nil {
		return err
	}
	stm.Put(perBatchKeyPath, perBatchKeyBuf.String())
	return nil
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

	// Wrap the update in an STM and execute it.
	_, err = s.defaultSTM(ctx, func(stm conc.STM) error {
		return s.putPerBatchKeySTM(stm, newPerBatchKey)
	})
	if err != nil {
		return nil, err
	}

	return newPerBatchKey, err
}

// PersistBatchResult atomically updates all modified orders/accounts, persists
// a snapshot of the batch and switches to the next batch ID. If any single
// operation fails, the whole set of changes is rolled back.
func (s *EtcdStore) PersistBatchResult(ctx context.Context,
	orders []orderT.Nonce, orderModifiers [][]order.Modifier,
	accounts []*btcec.PublicKey, accountModifiers [][]account.Modifier,
	masterAccount *account.Auctioneer, batchID orderT.BatchID,
	batch *matching.OrderBatch, newBatchKey *btcec.PublicKey,
	batchTx *wire.MsgTx) error {

	if !s.initialized {
		return errNotInitialized
	}

	// Catch the most obvious problems first.
	if len(orders) != len(orderModifiers) {
		return fmt.Errorf("order modifier length mismatch")
	}
	if len(accounts) != len(accountModifiers) {
		return fmt.Errorf("account modifier length mismatch")
	}

	// Wrap the whole batch update in one large isolated STM transaction.
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		// Update orders first.
		err := s.updateOrdersSTM(stm, orders, orderModifiers)
		if err != nil {
			return err
		}

		// Update accounts next.
		for idx, acctKey := range accounts {
			err := s.updateAccountSTM(
				stm, acctKey, accountModifiers[idx]...,
			)
			if err != nil {
				return err
			}
		}

		// Update the master account output.
		err = s.updateAuctioneerAccountSTM(stm, masterAccount)
		if err != nil {
			return err
		}

		// Store a self-contained snapshot of the current batch.
		var buf bytes.Buffer
		err = serializeBatch(&buf, batch, batchTx)
		if err != nil {
			return err
		}
		stm.Put(s.batchSnapshotKeyPath(batchID), buf.String())

		// Now that the batch has been inserted, we'll mark it as
		// pending in the DB.
		//
		// TODO(roasbeef): feels weird to write string zero...
		stm.Put(s.batchStatusKeyPath(batchID), "0")

		// And finally, put the new batch key in place.
		return s.putPerBatchKeySTM(stm, newBatchKey)
	})
	return err
}

// BatchConfirmed returns true if the target batch has been marked finalized
// (confirmed) on disk.
func (s *EtcdStore) BatchConfirmed(ctx context.Context,
	batchID orderT.BatchID) (bool, error) {

	var confirmed bool
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		batchStatus := stm.Get(s.batchStatusKeyPath(batchID))
		if batchStatus == "" {
			return ErrNoBatchExists
		}

		confirmed = batchStatus == "1"
		return nil
	})
	if err != nil {
		return false, err
	}

	return confirmed, nil
}

// ConfirmBatch finalizes a batch on disk, marking it as pending (unconfirmed)
// no longer.
func (s *EtcdStore) ConfirmBatch(ctx context.Context,
	batchID orderT.BatchID) error {

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		stm.Put(s.batchStatusKeyPath(batchID), "1")

		return nil
	})
	return err
}

// PersistBatchSnapshot persists a self-contained snapshot of a batch
// including all involved orders and accounts.
func (s *EtcdStore) PersistBatchSnapshot(ctx context.Context, id orderT.BatchID,
	batch *matching.OrderBatch, batchTx *wire.MsgTx) error {

	if !s.initialized {
		return errNotInitialized
	}

	key := s.batchSnapshotKeyPath(id)
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		// Serialize the batch snapshot and store it. If a previous
		// snapshots exists for the given ID, it is overwritten.
		var buf bytes.Buffer
		err := serializeBatch(&buf, batch, batchTx)
		if err != nil {
			return err
		}
		stm.Put(key, buf.String())
		return nil
	})
	return err
}

// GetBatchSnapshot returns the self-contained snapshot of a batch with
// the given ID as it was recorded at the time.
func (s *EtcdStore) GetBatchSnapshot(ctx context.Context, id orderT.BatchID) (
	*matching.OrderBatch, *wire.MsgTx, error) {

	if !s.initialized {
		return nil, nil, errNotInitialized
	}

	resp, err := s.getSingleValue(
		ctx, s.batchSnapshotKeyPath(id), errBatchSnapshotNotFound,
	)
	if err != nil {
		return nil, nil, err
	}

	return deserializeBatch(bytes.NewReader(resp.Kvs[0].Value))
}

// serializeBatch binary serializes a batch by using the LN wire format.
func serializeBatch(w io.Writer, b *matching.OrderBatch,
	batchTx *wire.MsgTx) error {

	// First, we'll encode the finalized batch tx itself.
	if err := batchTx.Serialize(w); err != nil {
		return err
	}

	// Write scalar values next.
	err := WriteElements(w, b.ClearingPrice, uint32(len(b.Orders)))
	if err != nil {
		return err
	}

	// Now the matched orders and the fee report nested structure.
	for idx := range b.Orders {
		err := serializeMatchedOrder(w, &b.Orders[idx])
		if err != nil {
			return err
		}
	}
	return serializeTradingFeeReport(w, &b.FeeReport)
}

// serializeMatchedOrder binary serializes a matched order by using the LN wire
// format.
func serializeMatchedOrder(w io.Writer, m *matching.MatchedOrder) error {
	err := serializeTrader(w, &m.Asker)
	if err != nil {
		return err
	}
	err = serializeTrader(w, &m.Bidder)
	if err != nil {
		return err
	}
	return serializeOrderPair(w, &m.Details)
}

// serializeTrader binary serializes a trader by using the LN wire format.
func serializeTrader(w io.Writer, t *matching.Trader) error {
	return WriteElements(
		w, t.AccountKey, t.BatchKey, t.VenueSecret,
		t.AccountExpiry, t.AccountOutPoint, t.AccountBalance,
	)
}

// serializeTrader binary serializes an order pair by using the LN wire format.
func serializeOrderPair(w io.Writer, p *matching.OrderPair) error {
	err := WriteElements(w, p.Ask.Nonce(), p.Bid.Nonce())
	if err != nil {
		return err
	}
	err = serializeOrder(w, p.Ask)
	if err != nil {
		return err
	}
	err = serializeOrder(w, p.Bid)
	if err != nil {
		return err
	}
	return serializePriceQuote(w, &p.Quote)
}

// serializePriceQuote binary serializes a price quote by using the LN wire
// format.
func serializePriceQuote(w io.Writer, q *matching.PriceQuote) error {
	return WriteElements(
		w, q.MatchingRate, q.TotalSatsCleared, q.UnitsMatched,
		q.UnitsUnmatched, q.Type,
	)
}

// serializeTradingFeeReport binary serializes a trading fee report by using the
// LN wire format.
func serializeTradingFeeReport(w io.Writer, f *matching.TradingFeeReport) error {
	// We'll need to flatten the map of the account diffs. For this, we'll
	// first write the number of keys n, then write n pairs of key and
	// value. But first, let's write the easy, scalar values.
	err := WriteElements(
		w, f.AuctioneerFeesAccrued, uint32(len(f.AccountDiffs)),
	)
	if err != nil {
		return err
	}

	// Write the keys and account diffs themselves. Both key and value are
	// fixed length so we can just write them one pair after the other.
	for key := range f.AccountDiffs {
		err := WriteElement(w, key)
		if err != nil {
			return err
		}
		diff := f.AccountDiffs[key]
		err = serializeAccountDiff(w, &diff)
		if err != nil {
			return err
		}
	}
	return nil
}

// serializeAccountDiff binary serializes an account diff by using the LN wire
// format.
func serializeAccountDiff(w io.Writer, d *matching.AccountDiff) error {
	err := serializeAccountTally(w, d.AccountTally)
	if err != nil {
		return err
	}
	err = serializeTrader(w, d.StartingState)
	if err != nil {
		return err
	}
	return WriteElement(w, d.RecreatedOutput)
}

// serializeAccountTally binary serializes an account tally by using the LN wire
// format.
func serializeAccountTally(w io.Writer, t *orderT.AccountTally) error {
	return WriteElements(
		w, t.EndingBalance, t.TotalExecutionFeesPaid,
		t.TotalTakerFeesPaid, t.TotalMakerFeesAccrued,
		t.NumChansCreated,
	)
}

// deserializeBatch reconstructs a batch from binary data in the LN wire
// format.
func deserializeBatch(r io.Reader) (*matching.OrderBatch, *wire.MsgTx, error) {
	var (
		b                = &matching.OrderBatch{}
		numMatchedOrders uint32
	)

	// First, we'll read out the batch tx itself.
	batchTx := &wire.MsgTx{}
	if err := batchTx.Deserialize(r); err != nil {
		return nil, nil, err
	}

	// Next read the scalar values.
	err := ReadElements(r, &b.ClearingPrice, &numMatchedOrders)
	if err != nil {
		return nil, nil, err
	}

	// Now we know how many orders to read.
	for i := uint32(0); i < numMatchedOrders; i++ {
		o, err := deserializeMatchedOrder(r)
		if err != nil {
			return nil, nil, err
		}
		b.Orders = append(b.Orders, *o)
	}

	// Finally deserialize the trading fee report nested structure.
	feeReport, err := deserializeTradingFeeReport(r)
	if err != nil {
		return nil, nil, err
	}
	b.FeeReport = *feeReport

	return b, batchTx, nil
}

// deserializeMatchedOrder reconstructs a matched order from binary data in the
// LN wire format.
func deserializeMatchedOrder(r io.Reader) (*matching.MatchedOrder, error) {
	o := &matching.MatchedOrder{}
	asker, err := deserializeTrader(r)
	if err != nil {
		return nil, err
	}
	o.Asker = *asker
	bidder, err := deserializeTrader(r)
	if err != nil {
		return nil, err
	}
	o.Bidder = *bidder
	orderPair, err := deserializeOrderPair(r)
	if err != nil {
		return nil, err
	}
	o.Details = *orderPair
	return o, nil
}

// deserializeTrader reconstructs a trader from binary data in the LN wire
// format.
func deserializeTrader(r io.Reader) (*matching.Trader, error) {
	t := &matching.Trader{}
	err := ReadElements(
		r, &t.AccountKey, &t.BatchKey, &t.VenueSecret,
		&t.AccountExpiry, &t.AccountOutPoint, &t.AccountBalance,
	)
	if err != nil {
		return nil, err
	}

	// To save some space, we don't store the _next_ batch key as it can
	// easily be derived.
	batchKey, err := btcec.ParsePubKey(t.BatchKey[:], btcec.S256())
	if err != nil {
		return nil, fmt.Errorf("error parsing batch key: %v", err)
	}
	nextKey := clmscript.IncrementKey(batchKey)
	copy(t.NextBatchKey[:], nextKey.SerializeCompressed())
	return t, nil
}

// deserializeOrderPair reconstructs an order pair from binary data in the LN
// wire format.
func deserializeOrderPair(r io.Reader) (*matching.OrderPair, error) {
	var askNonce, bidNonce orderT.Nonce
	err := ReadElements(r, &askNonce, &bidNonce)
	if err != nil {
		return nil, err
	}
	ask, err := deserializeOrder(r, askNonce)
	if err != nil {
		return nil, err
	}
	bid, err := deserializeOrder(r, bidNonce)
	if err != nil {
		return nil, err
	}
	quote, err := deserializePriceQuote(r)
	if err != nil {
		return nil, err
	}
	return &matching.OrderPair{
		Ask:   ask.(*order.Ask),
		Bid:   bid.(*order.Bid),
		Quote: *quote,
	}, nil
}

// deserializePriceQuote reconstructs a price quote from binary data in the LN
// wire format.
func deserializePriceQuote(r io.Reader) (*matching.PriceQuote, error) {
	q := &matching.PriceQuote{}
	err := ReadElements(
		r, &q.MatchingRate, &q.TotalSatsCleared, &q.UnitsMatched,
		&q.UnitsUnmatched, &q.Type,
	)
	if err != nil {
		return nil, err
	}
	return q, nil
}

// deserializeTradingFeeReport reconstructs a trading fee report from binary
// data in the LN wire format.
func deserializeTradingFeeReport(r io.Reader) (*matching.TradingFeeReport,
	error) {

	var (
		f       = &matching.TradingFeeReport{}
		numKeys uint32
	)

	// Read the easy, scalar values first so we know how many keys we'll
	// need to read.
	err := ReadElements(r, &f.AuctioneerFeesAccrued, &numKeys)
	if err != nil {
		return nil, err
	}

	// Now that we know how many pairs there are, just loop as many times
	// and read pair by pair.
	f.AccountDiffs = make(map[matching.AccountID]matching.AccountDiff)
	for i := uint32(0); i < numKeys; i++ {
		var key matching.AccountID
		err := ReadElement(r, &key)
		if err != nil {
			return nil, err
		}
		diff, err := deserializeAccountDiff(r)
		if err != nil {
			return nil, err
		}
		f.AccountDiffs[key] = *diff
	}
	return f, nil
}

// deserializeAccountDiff reconstructs an account diff from binary data in the
// LN wire format.
func deserializeAccountDiff(r io.Reader) (*matching.AccountDiff, error) {
	tally, err := deserializeAccountTally(r)
	if err != nil {
		return nil, err
	}
	trader, err := deserializeTrader(r)
	if err != nil {
		return nil, err
	}
	diff := &matching.AccountDiff{
		AccountTally:  tally,
		StartingState: trader,
	}
	err = ReadElement(r, &diff.RecreatedOutput)
	if err != nil {
		return nil, err
	}
	return diff, nil
}

// deserializeAccountTally reconstructs an account tally from binary data in the
// LN wire format.
func deserializeAccountTally(r io.Reader) (*orderT.AccountTally, error) {
	t := &orderT.AccountTally{}
	err := ReadElements(
		r, &t.EndingBalance, &t.TotalExecutionFeesPaid,
		&t.TotalTakerFeesPaid, &t.TotalMakerFeesAccrued,
		&t.NumChansCreated,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}
