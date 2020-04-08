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
	conc "github.com/coreos/etcd/clientv3/concurrency"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/client/clmscript"
	orderT "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
	"github.com/lightninglabs/agora/venue/matching"
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

// PersistBatchResult atomically updates all modified orders/accounts and
// switches to the next batch ID. If any single operation fails, the whole
// set of changes is rolled back.
func (s *EtcdStore) PersistBatchResult(ctx context.Context,
	orders []orderT.Nonce, orderModifiers [][]order.Modifier,
	accounts []*btcec.PublicKey, accountModifiers [][]account.Modifier,
	masterAccount *account.Auctioneer, newBatchkey *btcec.PublicKey) error {

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

		// And finally, put the new batch key in place.
		return s.putPerBatchKeySTM(stm, newBatchkey)
	})
	return err
}

// serializeBatch binary serializes a batch by using the LN wire format.
func serializeBatch(w io.Writer, b *matching.OrderBatch) error {
	// Write scalar values first.
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
func deserializeBatch(r io.Reader) (*matching.OrderBatch, error) {
	var (
		b                = &matching.OrderBatch{}
		numMatchedOrders uint32
	)

	// First read the scalar values.
	err := ReadElements(r, &b.ClearingPrice, &numMatchedOrders)
	if err != nil {
		return nil, err
	}

	// Now we know how many orders to read.
	for i := uint32(0); i < numMatchedOrders; i++ {
		o, err := deserializeMatchedOrder(r)
		if err != nil {
			return nil, err
		}
		b.Orders = append(b.Orders, *o)
	}

	// Finally deserialize the trading fee report nested structure.
	feeReport, err := deserializeTradingFeeReport(r)
	if err != nil {
		return nil, err
	}
	b.FeeReport = *feeReport
	return b, nil
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
