package subastadb

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
	"github.com/btcsuite/btcutil"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
	conc "go.etcd.io/etcd/client/v3/concurrency"
)

var (
	// InitialBatchKey serves as our initial global batch key. This key will
	// be incremented by the curve's base point every time a new batch is
	// cleared.
	initialBatchKeyBytes, _ = hex.DecodeString(
		"02824d0cbac65e01712124c50ff2cc74ce22851d7b444c1bf2ae66afefb8eaf27f",
	)
	InitialBatchKey, _ = btcec.ParsePubKey(initialBatchKeyBytes, btcec.S256())

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
	// bitcoin/clm/subasta/<network>/batch/key.
	parts := []string{batchDir, perBatchKey}
	return s.getKeyPrefix(strings.Join(parts, keyDelimiter))
}

// batchSnapshotKeyPath returns the full path under which we store a batch
// snapshot.
func (s *EtcdStore) batchSnapshotKeyPath(id orderT.BatchID) string {
	// bitcoin/clm/subasta/<network>/batch/<batchID>.
	parts := []string{batchDir, hex.EncodeToString(id[:])}
	return s.getKeyPrefix(strings.Join(parts, keyDelimiter))
}

// batchStatusKeyPath returns the full path under which we store a batch
// status.
func (s *EtcdStore) batchStatusKeyPath(id orderT.BatchID) string {
	// bitcoin/clm/subasta/<network>/batch/<batchID>/status.
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

// PersistBatchResult atomically updates all modified orders/accounts, persists
// a snapshot of the batch and switches to the next batch ID. If any single
// operation fails, the whole set of changes is rolled back.
func (s *EtcdStore) PersistBatchResult(ctx context.Context,
	orders []orderT.Nonce, orderModifiers [][]order.Modifier,
	accounts []*btcec.PublicKey, accountModifiers [][]account.Modifier,
	masterAccount *account.Auctioneer, batchID orderT.BatchID,
	batchSnapshot *BatchSnapshot, newBatchKey *btcec.PublicKey,
	lifetimePkgs []*chanenforcement.LifetimePackage) error {

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

	// Before we can update the database, we must obtain an exclusive lock
	// for all the nonces in the transaction, to guarantee consistency with
	// the cache.
	s.nonceMtx.lock(orders...)
	defer s.nonceMtx.unlock(orders...)

	// Similarly we need to ensure that accounts are not updated until we
	// can be sure that all account sate is mirrored to SQL.
	s.accountUpdateMtx.Lock()
	defer s.accountUpdateMtx.Unlock()

	var updatedAccounts []*account.Account

	// Wrap the whole batch update in one large isolated STM transaction.
	var cacheUpdates map[orderT.Nonce]order.ServerOrder
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		// Update orders first.
		var err error
		cacheUpdates, err = s.updateOrdersSTM(stm, orders, orderModifiers)
		if err != nil {
			return err
		}

		// Update accounts next.
		for idx, acctKey := range accounts {
			acct, err := s.updateAccountSTM(
				stm, acctKey, accountModifiers[idx]...,
			)
			if err != nil {
				return err
			}

			updatedAccounts = append(updatedAccounts, acct)
		}

		// Update the master account output.
		err = s.updateAuctioneerAccountSTM(stm, masterAccount)
		if err != nil {
			return err
		}

		// Store the lifetime packages of each channel created as part
		// of the batch.
		for _, lifetimePkg := range lifetimePkgs {
			err := s.storeLifetimePackage(stm, lifetimePkg)
			if err != nil {
				return err
			}
		}

		// Store a self-contained snapshot of the current batch.
		var buf bytes.Buffer
		err = serializeBatchSnapshot(&buf, batchSnapshot)
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
	if err != nil {
		return err
	}

	// Now that the DB was successfully updated, also update the cache.
	s.updateOrderCache(cacheUpdates)

	// Optionally mirror the updated orders and accounts to SQL.
	if s.sqlMirror != nil {
		err := s.sqlMirror.Transaction(
			ctx,
			func(tx *SQLTransaction) error {
				for _, o := range cacheUpdates {
					err := tx.UpdateOrder(o)
					if err != nil {
						return err
					}
				}

				for _, acct := range updatedAccounts {
					err := tx.UpdateAccount(acct)
					if err != nil {
						return err
					}
				}

				return tx.UpdateBatch(batchID, batchSnapshot)
			},
		)
		if err != nil {
			log.Errorf("Unable to store batch updates to SQL db: "+
				"%v", err)
		}
	}

	return nil
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

// Batches retrieves all existing batches.
func (s *EtcdStore) Batches(ctx context.Context) (
	map[orderT.BatchID]*BatchSnapshot, error) {
	if !s.initialized {
		return nil, errNotInitialized
	}

	resp, err := s.getAllValuesByPrefix(ctx, s.getKeyPrefix(batchDir))
	if err != nil {
		return nil, err
	}

	batches := make(map[orderT.BatchID]*BatchSnapshot)
	for k, v := range resp {
		if strings.HasSuffix(k, perBatchKey) ||
			strings.HasSuffix(k, batchStatusKey) {
			continue
		}

		snapshot, err := deserializeBatchSnapshot(bytes.NewReader(v))
		if err != nil {
			return nil, err
		}

		_, err = s.defaultSTM(ctx, func(stm conc.STM) error {
			return s.supplementSnapshotData(stm, snapshot)
		})
		if err != nil {
			return nil, err
		}

		var batchID orderT.BatchID
		keyParts := strings.Split(k, keyDelimiter)
		rawBatchID, err := hex.DecodeString(keyParts[len(keyParts)-1])
		if err != nil {
			return nil, err
		}
		copy(batchID[:], rawBatchID)

		batches[batchID] = snapshot
	}

	return batches, nil
}

// GetBatchSnapshot returns the self-contained snapshot of a batch with
// the given ID as it was recorded at the time.
func (s *EtcdStore) GetBatchSnapshot(ctx context.Context, id orderT.BatchID) (
	*BatchSnapshot, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	var snapshot *BatchSnapshot
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		resp := stm.Get(s.batchSnapshotKeyPath(id))
		if resp == "" {
			return errBatchSnapshotNotFound
		}

		var err error
		snapshot, err = deserializeBatchSnapshot(strings.NewReader(resp))
		if err != nil {
			return err
		}

		// Now that we know what's in the batch, we'll do a second pass
		// to populate all the node tier information for each batch.
		return s.supplementSnapshotData(stm, snapshot)
	})
	if err != nil {
		return nil, err
	}

	return snapshot, nil
}

// supplementSnapshotData takes the base batch snapshot, and supplements it
// with all the additional information that may not be stored as part of the
// batch matched order. This includes any new order fields that may have been
// added over time such as the min node tier.
func (s *EtcdStore) supplementSnapshotData(stm conc.STM,
	snapshot *BatchSnapshot) error {

	// For each bid included in the snapshot, we'll fetch the extra
	// information to obtain what node tier what associated with the bid,
	// and then attach that to the bid itself.
	for _, matchedOrder := range snapshot.OrderBatch.Orders {
		// In addition to the base order, we'll also need to obtain the
		// node tier for this order. Note that this key won't exist if
		// it wasn't populated for the order, or if it's an Ask.
		bidNonce := matchedOrder.Details.Bid.Nonce()
		nodeTier, err := s.fetchMinNodeTierSTM(stm, bidNonce)
		if err != nil {
			return fmt.Errorf("node tier: %v", err)
		}
		matchedOrder.Details.Bid.MinNodeTier = nodeTier

		// We'll also obtain the min units match for both orders.
		askNonce := matchedOrder.Details.Ask.Nonce()
		askMinUnitsMatch, err := s.fetchMinUnitsMatchSTM(stm, askNonce)
		if err != nil {
			return fmt.Errorf("ask min units match: %v", err)
		}
		matchedOrder.Details.Ask.MinUnitsMatch = askMinUnitsMatch

		bidMinUnitsMatch, err := s.fetchMinUnitsMatchSTM(stm, bidNonce)
		if err != nil {
			return fmt.Errorf("bid min units match: %v", err)
		}
		matchedOrder.Details.Bid.MinUnitsMatch = bidMinUnitsMatch

		// Finally also add any extra data that's encoded in the tlv
		// stream.
		err = s.fetchTlvSTM(stm, matchedOrder.Details.Ask)
		if err != nil {
			return fmt.Errorf("ask tlv data: %v", err)
		}
		err = s.fetchTlvSTM(stm, matchedOrder.Details.Bid)
		if err != nil {
			return fmt.Errorf("bid tlv data: %v", err)
		}
	}

	return nil
}

// fetchMinNodeTierSTM attempts to fetch the min node tier for a given order
// nonce using an existing STM context.
func (s *EtcdStore) fetchMinNodeTierSTM(stm conc.STM,
	bidNonce orderT.Nonce) (orderT.NodeTier, error) {

	// Since we don't know the state of the order, we'll need to check both
	// possible branches (archived vs active).
	nodeTierKey := s.getOrderTierKey(false, bidNonce)
	nodeTierResp := stm.Get(nodeTierKey)

	// If the order has been archived, we'll check for that branch.
	if nodeTierResp == "" {
		nodeTierKey = s.getOrderTierKey(true, bidNonce)
		nodeTierResp = stm.Get(nodeTierKey)

		// If the value still hasn't been found, then this is an old
		// order that was not aware of the value, so we'll fall back to
		// the default.
		if nodeTierResp == "" {
			return orderT.DefaultMinNodeTier, nil
		}
	}

	var minNodeTier orderT.NodeTier
	err := ReadElement(strings.NewReader(nodeTierResp), &minNodeTier)
	if err != nil {
		return 0, err
	}

	return minNodeTier, nil
}

// fetchMinUnitsMatchSTM attempts to fetch the min units match for a given order
// nonce using an existing STM context.
func (s *EtcdStore) fetchMinUnitsMatchSTM(stm conc.STM,
	nonce orderT.Nonce) (orderT.SupplyUnit, error) {

	// Since we don't know the state of the order, we'll need to check both
	// possible branches (archived vs active).
	minUnitsMatchKey := s.getOrderMinUnitsMatchKey(false, nonce)
	minUnitsMatchResp := stm.Get(minUnitsMatchKey)

	// If the order has been archived, we'll check for that branch.
	if minUnitsMatchResp == "" {
		minUnitsMatchKey = s.getOrderMinUnitsMatchKey(true, nonce)
		minUnitsMatchResp = stm.Get(minUnitsMatchKey)

		// If the value still hasn't been found, then this is an old
		// order that was not aware of the value, so we'll fall back to
		// the default.
		if minUnitsMatchResp == "" {
			return 1, nil
		}
	}

	var minUnitsMatch orderT.SupplyUnit
	err := ReadElement(strings.NewReader(minUnitsMatchResp), &minUnitsMatch)
	if err != nil {
		return 0, err
	}

	return minUnitsMatch, nil
}

// fetchTlvSTM attempts to fetch the additional tlv data for a given order nonce
// using an existing STM context.
func (s *EtcdStore) fetchTlvSTM(stm conc.STM, o order.ServerOrder) error {
	// Since we don't know the state of the order, we'll need to check both
	// possible branches (archived vs active).
	tlvKey := s.getOrderTlvKey(false, o.Nonce())
	tlvResp := stm.Get(tlvKey)

	// If the order has been archived, we'll check for that branch.
	if tlvResp == "" {
		tlvKey = s.getOrderTlvKey(true, o.Nonce())
		tlvResp = stm.Get(tlvKey)

		// If the value still hasn't been found, then this is an old
		// order that was not aware of the value, so we'll fall back to
		// the default.
		if tlvResp == "" {
			return nil
		}
	}

	return deserializeOrderTlvData(strings.NewReader(tlvResp), o)
}

// serializeBatchSnapshot binary serializes a batch snapshot by using the LN
// wire format.
func serializeBatchSnapshot(w *bytes.Buffer, b *BatchSnapshot) error {
	// First, we'll encode the finalized batch tx itself.
	if err := b.BatchTx.Serialize(w); err != nil {
		return err
	}

	// The previous batch versions had a single clearing price but because
	// we now always store the price map afterwards, we signal a new batch
	// by storing an explicit zero price.
	var zeroPrice orderT.FixedRatePremium
	err := WriteElements(
		w, b.BatchTxFee, zeroPrice, uint32(len(b.OrderBatch.Orders)),
	)
	if err != nil {
		return err
	}

	// Now the matched orders and the fee report nested structure.
	for idx := range b.OrderBatch.Orders {
		err := serializeMatchedOrder(w, &b.OrderBatch.Orders[idx])
		if err != nil {
			return err
		}
	}
	err = serializeTradingFeeReport(w, &b.OrderBatch.FeeReport)
	if err != nil {
		return err
	}

	// For new batches, we also store its version, timestamp and the map of
	// lease duration bucket -> clearing price.
	err = WriteElements(
		w, b.OrderBatch.Version, b.OrderBatch.CreationTimestamp,
	)
	if err != nil {
		return err
	}
	numPrices := uint32(len(b.OrderBatch.ClearingPrices))
	err = WriteElements(w, numPrices)
	if err != nil {
		return err
	}
	for duration, price := range b.OrderBatch.ClearingPrices {
		err = WriteElements(w, duration, price)
		if err != nil {
			return err
		}
	}

	return nil
}

// serializeMatchedOrder binary serializes a matched order by using the LN wire
// format.
func serializeMatchedOrder(w *bytes.Buffer, m *matching.MatchedOrder) error {
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
func serializeTrader(w *bytes.Buffer, t *matching.Trader) error {
	return WriteElements(
		w, t.AccountKey, t.BatchKey, t.VenueSecret,
		t.AccountExpiry, t.AccountOutPoint, t.AccountBalance,
	)
}

// serializeTrader binary serializes an order pair by using the LN wire format.
func serializeOrderPair(w *bytes.Buffer, p *matching.OrderPair) error {
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
func serializePriceQuote(w *bytes.Buffer, q *matching.PriceQuote) error {
	return WriteElements(
		w, q.MatchingRate, q.TotalSatsCleared, q.UnitsMatched,
		q.UnitsUnmatched, q.Type,
	)
}

// serializeTradingFeeReport binary serializes a trading fee report by using the
// LN wire format.
func serializeTradingFeeReport(w *bytes.Buffer, f *matching.TradingFeeReport) error {
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
func serializeAccountDiff(w *bytes.Buffer, d *matching.AccountDiff) error {
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
func serializeAccountTally(w *bytes.Buffer, t *orderT.AccountTally) error {
	return WriteElements(
		w, t.EndingBalance, t.TotalExecutionFeesPaid,
		t.TotalTakerFeesPaid, t.TotalMakerFeesAccrued,
		t.NumChansCreated,
	)
}

// deserializeBatchSnapshot reconstructs a batch snapshot from binary data in
// the LN wire format.
func deserializeBatchSnapshot(r io.Reader) (*BatchSnapshot, error) {
	var (
		txFee btcutil.Amount
		b     = &matching.OrderBatch{
			SubBatches:     make(map[uint32][]matching.MatchedOrder),
			ClearingPrices: make(map[uint32]orderT.FixedRatePremium),
		}
		numMatchedOrders uint32
		clearingPrice    orderT.FixedRatePremium
	)

	// First, we'll read out the batch tx itself.
	batchTx := &wire.MsgTx{}
	if err := batchTx.Deserialize(r); err != nil {
		return nil, err
	}

	// Next read the scalar values.
	err := ReadElements(
		r, &txFee, &clearingPrice, &numMatchedOrders,
	)
	if err != nil {
		return nil, err
	}

	// Now we know how many orders to read.
	b.Orders = make([]matching.MatchedOrder, numMatchedOrders)
	for i := uint32(0); i < numMatchedOrders; i++ {
		o, err := deserializeMatchedOrder(r)
		if err != nil {
			return nil, err
		}
		b.Orders[i] = *o
	}

	// Finally deserialize the trading fee report nested structure.
	feeReport, err := deserializeTradingFeeReport(r)
	if err != nil {
		return nil, err
	}
	b.FeeReport = *feeReport

	// Older batches had a single clearing price instead of a map. If we
	// have a non-zero price, it means this is an old snapshot and we don't
	// need to read any further.
	if clearingPrice > 0 {
		b.Version = orderT.DefaultBatchVersion
		b.ClearingPrices[orderT.LegacyLeaseDurationBucket] = clearingPrice
		b.SubBatches[orderT.LegacyLeaseDurationBucket] = b.Orders

		return &BatchSnapshot{
			BatchTx:    batchTx,
			BatchTxFee: txFee,
			OrderBatch: b,
		}, nil
	}

	// The version and timestamp field was added to the serialized format at
	// the same time as the changes to the clearing price. Therefore if the
	// clearing price is zero, we also expect a version to be stored.
	err = ReadElements(r, &b.Version, &b.CreationTimestamp)
	switch err {
	// Successful read, continue reading all prices.
	case nil:
		break

	// It's an old but empty batch that has no matched orders and therefore
	// a clearingPrice of 0 so the above if didn't catch it. We still don't
	// have a version or multiple buckets so we also need to return early.
	case io.EOF, io.ErrUnexpectedEOF:
		b.Version = orderT.DefaultBatchVersion
		b.ClearingPrices[orderT.LegacyLeaseDurationBucket] = clearingPrice
		b.SubBatches[orderT.LegacyLeaseDurationBucket] = b.Orders

		return &BatchSnapshot{
			BatchTx:    batchTx,
			BatchTxFee: txFee,
			OrderBatch: b,
		}, nil

	// Unexpected error, return.
	default:
		return nil, err
	}

	// If we got here, we have the new serialization format that also stores
	// multiple prices, one per duration.
	var numPrices uint32
	err = ReadElements(r, &numPrices)
	if err != nil {
		return nil, err
	}

	for i := uint32(0); i < numPrices; i++ {
		var (
			duration uint32
			price    orderT.FixedRatePremium
		)
		err = ReadElements(r, &duration, &price)
		if err != nil {
			return nil, err
		}

		b.ClearingPrices[duration] = price
	}

	// Distribute the orders into their duration buckets.
	for _, o := range b.Orders {
		// If we get here, we have a new batch where both ask and bid
		// need to have the same duration, doesn't matter which one we
		// use.
		duration := o.Details.Ask.LeaseDuration()
		b.SubBatches[duration] = append(
			b.SubBatches[duration], o,
		)
	}

	return &BatchSnapshot{
		BatchTx:    batchTx,
		BatchTxFee: txFee,
		OrderBatch: b,
	}, nil
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
	nextKey := poolscript.IncrementKey(batchKey)
	copy(t.NextBatchKey[:], nextKey.SerializeCompressed())
	return t, nil
}

// deserializeOrderPair reconstructs an order pair from binary data in the LN
// wire format.
//
// NOTE: Orders have additional data outside of the single byte stream given to
// this method.
func deserializeOrderPair(r io.Reader) (*matching.OrderPair, error) {
	var askNonce, bidNonce orderT.Nonce
	err := ReadElements(r, &askNonce, &bidNonce)
	if err != nil {
		return nil, err
	}
	ask, err := deserializeBaseOrder(r, askNonce)
	if err != nil {
		return nil, err
	}
	bid, err := deserializeBaseOrder(r, bidNonce)
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
