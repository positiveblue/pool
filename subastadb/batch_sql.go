package subastadb

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/wire"
	"github.com/jackc/pgx/v4"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb/postgres"
	"github.com/lightninglabs/subasta/venue/matching"
)

// BatchKey returns the current per-batch key that must be used to tweak account
// trader keys with.
func (s *SQLStore) BatchKey(ctx context.Context) (*btcec.PublicKey, error) {
	row, err := s.queries.GetCurrentBatchKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get current batch key: %v",
			err)
	}

	batchKey, err := btcec.ParsePubKey(row.BatchKey)
	if err != nil {
		return nil, fmt.Errorf("unable parse current batchKey(%x): %v",
			row.BatchKey, err)
	}

	return batchKey, nil
}

// StoreBatch inserts a batch with its confirmation status in the db.
func (s *SQLStore) StoreBatch(ctx context.Context, batchID orderT.BatchID,
	batch *matching.BatchSnapshot, confirmed bool) error {

	txBody := func(txQueries *postgres.Queries) error {
		err := storeBatchWithTx(ctx, txQueries, batchID, batch)
		if err != nil {
			return err
		}
		return nil
	}
	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to persist batch(%x): %v", batchID,
			err)
	}

	if confirmed {
		err = s.ConfirmBatch(ctx, batchID)
		if err != nil {
			return err
		}
	}

	return nil
}

// SetCurrentBatch inserts the current batch key in the db.
func (s *SQLStore) SetCurrentBatch(ctx context.Context,
	batchID orderT.BatchID) error {

	currentBatchKeyParams := postgres.UpsertCurrentBatchKeyParams{
		BatchKey: batchID[:],
	}
	return s.queries.UpsertCurrentBatchKey(ctx, currentBatchKeyParams)
}

// PersistBatchResult atomically updates all modified orders/accounts,
// persists a snapshot of the batch and switches to the next batch ID.
// If any single operation fails, the whole set of changes is rolled
// back.
func (s *SQLStore) PersistBatchResult(ctx context.Context,
	orders []orderT.Nonce, orderModifiers [][]order.Modifier,
	accounts []*btcec.PublicKey, accountModifiers [][]account.Modifier,
	masterAccount *account.Auctioneer, batchID orderT.BatchID,
	batchSnapshot *matching.BatchSnapshot, newBatchKey *btcec.PublicKey,
	lifetimePkgs []*chanenforcement.LifetimePackage) error {

	// Catch the most obvious problems first.
	if len(orders) != len(orderModifiers) {
		return fmt.Errorf("order modifier length mismatch")
	}
	if len(accounts) != len(accountModifiers) {
		return fmt.Errorf("account modifier length mismatch")
	}

	txBody := func(txQueries *postgres.Queries) error {
		var err error

		// Update orders.
		for idx, nonce := range orders {
			err = modifyOrderWithTx(
				ctx, txQueries, nonce, orderModifiers[idx]...,
			)
			if err != nil {
				return err
			}
		}

		// Update accounts next.
		for idx, acctKey := range accounts {
			_, err := modifyAccountWithTx(
				ctx, txQueries, acctKey,
				accountModifiers[idx]...,
			)
			if err != nil {
				return err
			}
		}

		// Store the lifetime packages of each channel created as part
		// of the batch.
		for _, lifetimePkg := range lifetimePkgs {
			params := upsertLifetimePackageParams(lifetimePkg)
			err = txQueries.UpsertLifetimePackage(ctx, params)
			if err != nil {
				return err
			}
		}

		err = storeBatchWithTx(ctx, txQueries, batchID, batchSnapshot)
		if err != nil {
			return err
		}

		// Store the auctioneer snapshot BEFORE setting the key for the
		// next batch.
		err = createAuctioneerSnapshotWithTx(
			ctx, txQueries, masterAccount,
		)
		if err != nil {
			return err
		}

		copy(
			masterAccount.BatchKey[:],
			newBatchKey.SerializeCompressed(),
		)

		// Update the master account output.
		err = upsertAuctioneerAccountWithTx(
			ctx, txQueries, masterAccount,
		)
		if err != nil {
			return err
		}

		batchKeyRow, err := txQueries.GetCurrentBatchKey(ctx)
		if err != nil {
			return err
		}

		batchKeyParams := postgres.UpsertCurrentBatchKeyParams{
			ID:       batchKeyRow.ID,
			BatchKey: newBatchKey.SerializeCompressed(),
		}
		return txQueries.UpsertCurrentBatchKey(ctx, batchKeyParams)
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return fmt.Errorf("unable to persist batch(%x): %v", batchID,
			err)
	}

	return nil
}

// GetBatchSnapshot returns the self-contained snapshot of a batch with
// the given ID as it was recorded at the time.
func (s *SQLStore) GetBatchSnapshot(ctx context.Context,
	batchID orderT.BatchID) (*matching.BatchSnapshot, error) {

	var batchSnapshot *matching.BatchSnapshot
	txBody := func(txQueries *postgres.Queries) error {
		var err error

		// Get batch.
		batchRow, err := txQueries.GetBatch(ctx, batchID[:])
		switch {
		case err == pgx.ErrNoRows:
			return errBatchSnapshotNotFound

		case err != nil:
			return err
		}

		// Get batch matched orders.
		matchedOrderRows, err := txQueries.GetAllMatchedOrdersByBatchID(
			ctx, batchID[:],
		)
		if err != nil {
			return err
		}

		nonces := make([][]byte, len(matchedOrderRows)*2)
		for idx, matchedOrder := range matchedOrderRows {
			nonces[idx*2] = matchedOrder.AskOrderNonce
			nonces[idx*2+1] = matchedOrder.BidOrderNonce
		}

		ordLst, err := getOrdersWithTx(ctx, txQueries, nonces)
		if err != nil {
			return err
		}
		orders := make(map[orderT.Nonce]order.ServerOrder, len(ordLst))
		for _, o := range ordLst {
			orders[o.Nonce()] = o
		}

		// Get batch account diffs.
		accDiffRows, err := txQueries.GetBatchAccountDiffs(
			ctx, batchID[:],
		)
		if err != nil {
			return nil
		}

		// Get batch clearing prices.
		clearingPriceRows, err := txQueries.GetBatchClearingPrices(
			ctx, batchID[:],
		)
		if err != nil {
			return nil
		}

		// Assemble batch snapshot
		batchSnapshot, err = unmarshalBatchSnapshot(
			batchRow, matchedOrderRows, orders, accDiffRows,
			clearingPriceRows,
		)

		return err
	}
	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return nil, fmt.Errorf("unable to get batch(%x): %v", batchID,
			err)
	}

	return batchSnapshot, nil
}

// ConfirmBatch finalizes a batch on disk, marking it as pending (unconfirmed)
// no longer.
func (s *SQLStore) ConfirmBatch(ctx context.Context,
	batchID orderT.BatchID) error {

	rows, err := s.queries.ConfirmBatch(ctx, batchID[:])
	if err != nil {
		return fmt.Errorf("unable to confirm batch %x: %v ", batchID,
			err)
	} else if rows == 0 {
		return fmt.Errorf("unable to confirm batch %x: batchID not "+
			"found", batchID)
	}

	return nil
}

// BatchConfirmed returns true if the target batch has been marked finalized
// (confirmed) on disk.
func (s *SQLStore) BatchConfirmed(ctx context.Context,
	batchID orderT.BatchID) (bool, error) {

	isConfirmed, err := s.queries.IsBatchConfirmed(ctx, batchID[:])
	switch {
	case err == pgx.ErrNoRows:
		return false, ErrNoBatchExists
	case err != nil:
		return false, fmt.Errorf("unable to check batch(%x) "+
			"confirmation status: %v", batchID, err)
	}

	return isConfirmed, nil
}

// Batches retrieves all existing batches.
func (s *SQLStore) Batches(
	ctx context.Context) (map[orderT.BatchID]*matching.BatchSnapshot, error) {

	errMsg := "unable to get batches: %w"

	// TODO(positiveblue): optimize this method.
	params := postgres.GetBatchKeysParams{}
	batchKeys, err := s.queries.GetBatchKeys(ctx, params)
	if err != nil {
		return nil, fmt.Errorf(errMsg, err)
	}

	batches := make(map[orderT.BatchID]*matching.BatchSnapshot, len(batchKeys))
	for _, batchKey := range batchKeys {
		var batchID orderT.BatchID
		copy(batchID[:], batchKey)
		batch, err := s.GetBatchSnapshot(ctx, batchID)
		if err != nil {
			return nil, fmt.Errorf(errMsg, err)
		}
		batches[batchID] = batch
	}

	return batches, nil
}

// storeBatchWithTx creates all the data related to a batch snapshot in the
// db using the provided queries struct.
func storeBatchWithTx(ctx context.Context, txQueries *postgres.Queries,
	batchID orderT.BatchID, batchSnapshot *matching.BatchSnapshot) error {

	// Create batch.
	var batchTx bytes.Buffer
	if err := batchSnapshot.BatchTx.Serialize(&batchTx); err != nil {
		return fmt.Errorf("unable to serialize batchTx: %v", err)
	}

	version := int64(batchSnapshot.OrderBatch.Version)
	fees := int64(batchSnapshot.OrderBatch.FeeReport.AuctioneerFeesAccrued)
	var createdAt sql.NullTime
	err := createdAt.Scan(batchSnapshot.OrderBatch.CreationTimestamp)
	if err != nil {
		return err
	}
	batchParams := postgres.UpsertBatchParams{
		BatchKey:              batchID[:],
		BatchTx:               batchTx.Bytes(),
		BatchTxFee:            int64(batchSnapshot.BatchTxFee),
		Version:               version,
		AuctioneerFeesAccrued: fees,
		CreatedAt:             createdAt,
	}

	err = txQueries.UpsertBatch(ctx, batchParams)
	if err != nil {
		return err
	}

	// Create batch matched orders.
	var matchedOrderParams []postgres.CreateMatchedOrderParams
	for _, o := range batchSnapshot.OrderBatch.Orders {
		askOrderNonce := o.Details.Ask.Nonce()
		bidOrderNonce := o.Details.Bid.Nonce()
		quote := o.Details.Quote
		params := postgres.CreateMatchedOrderParams{
			BatchKey:         batchID[:],
			AskOrderNonce:    askOrderNonce[:],
			BidOrderNonce:    bidOrderNonce[:],
			LeaseDuration:    int64(o.Details.Ask.LeaseDuration()),
			MatchingRate:     int64(quote.MatchingRate),
			TotalSatsCleared: int64(quote.TotalSatsCleared),
			UnitsMatched:     int64(quote.UnitsMatched),
			UnitsUnmatched:   int64(quote.UnitsUnmatched),
			AskUnitsUnmatched: int64(
				o.Details.Ask.UnitsUnfulfilled,
			),
			BidUnitsUnmatched: int64(
				o.Details.Bid.UnitsUnfulfilled,
			),
			FulfillType:  int16(quote.Type),
			AskState:     int16(o.Details.Ask.State),
			BidState:     int16(o.Details.Bid.State),
			AskerExpiry:  int64(o.Asker.AccountExpiry),
			BidderExpiry: int64(o.Bidder.AccountExpiry),
		}

		matchedOrderParams = append(matchedOrderParams, params)
	}

	_, err = txQueries.CreateMatchedOrder(ctx, matchedOrderParams)
	if err != nil {
		return err
	}

	// Create batch account diffs.
	err = createBatchAccountDiffsWithTx(
		ctx, txQueries, batchID, batchSnapshot,
	)
	if err != nil {
		return err
	}

	// Create clearing prices.
	var clearingPriceParams []postgres.CreateClearingPriceParams
	for duration, rate := range batchSnapshot.OrderBatch.ClearingPrices {
		params := postgres.CreateClearingPriceParams{
			BatchKey:         batchID[:],
			LeaseDuration:    int64(duration),
			FixedRatePremium: int64(rate),
		}
		clearingPriceParams = append(clearingPriceParams, params)
	}

	_, err = txQueries.CreateClearingPrice(ctx, clearingPriceParams)
	if err != nil {
		return err
	}

	return nil
}

// createBatchAccountDiffsWithTx inserts all the account diffs of a batch
// using the provided queries struct.
func createBatchAccountDiffsWithTx(ctx context.Context,
	txQueries *postgres.Queries, batchID orderT.BatchID,
	batchSnapshot *matching.BatchSnapshot) error {

	accDiffs := batchSnapshot.OrderBatch.FeeReport.AccountDiffs
	paramList := make(
		[]postgres.CreateBatchAccountDiffParams, 0, len(accDiffs),
	)
	for _, diff := range accDiffs {
		startingState := diff.StartingState
		outPointHash, outPointIndex := marshalOutPoint(
			startingState.AccountOutPoint,
		)

		executionFees := int64(diff.TotalExecutionFeesPaid)
		makerFees := int64(diff.TotalMakerFeesAccrued)
		startingBalance := int64(startingState.AccountBalance)
		startingExpiry := int64(startingState.AccountExpiry)
		params := postgres.CreateBatchAccountDiffParams{
			BatchKey:               batchID[:],
			TraderKey:              startingState.AccountKey[:],
			TraderBatchKey:         startingState.BatchKey[:],
			TraderNextBatchKey:     startingState.NextBatchKey[:],
			Secret:                 startingState.VenueSecret[:],
			TotalExecutionFeesPaid: executionFees,
			TotalTakerFeesPaid:     int64(diff.TotalTakerFeesPaid),
			TotalMakerFeesAccrued:  makerFees,
			NumChansCreated:        int64(diff.NumChansCreated),
			StartingBalance:        startingBalance,
			StartingAccountExpiry:  startingExpiry,
			StartingOutPointHash:   outPointHash,
			StartingOutPointIndex:  outPointIndex,
			EndingBalance:          int64(diff.EndingBalance),
			NewAccountExpiry:       int64(diff.NewExpiry),
		}

		if diff.RecreatedOutput != nil {
			var value sql.NullInt64
			err := value.Scan(diff.RecreatedOutput.Value)
			if err != nil {
				return err
			}
			params.TxOutValue = value

			pkScript := make(
				[]byte, len(diff.RecreatedOutput.PkScript),
			)
			copy(pkScript, diff.RecreatedOutput.PkScript)
			params.TxOutPkscript = pkScript
		}
		paramList = append(paramList, params)
	}

	_, err := txQueries.CreateBatchAccountDiff(ctx, paramList)
	return err
}

// unmarshalBatchSnapshot deserializes a *BatchSnapshot.
func unmarshalBatchSnapshot(batchRow postgres.Batch,
	matchedOrderRows []postgres.BatchMatchedOrder,
	orders map[orderT.Nonce]order.ServerOrder,
	accDiffRows []postgres.BatchAccountDiff,
	clearingPriceRows []postgres.BatchClearingPrice) (*matching.BatchSnapshot,
	error) {

	batchSnapshot := &matching.BatchSnapshot{}

	batchTx := &wire.MsgTx{}
	err := batchTx.Deserialize(bytes.NewReader(batchRow.BatchTx))
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshal batch: %v", err)
	}
	batchSnapshot.BatchTx = batchTx
	batchSnapshot.BatchTxFee = btcutil.Amount(batchRow.BatchTxFee)

	orderBatch := &matching.OrderBatch{}
	orderBatch.Version = orderT.BatchVersion(batchRow.Version)
	if batchRow.CreatedAt.Valid {
		orderBatch.CreationTimestamp = batchRow.CreatedAt.Time
	}

	accDiffs := make(
		map[matching.AccountID]*matching.AccountDiff, len(accDiffRows),
	)

	for _, row := range accDiffRows {
		var accountID [33]byte
		copy(accountID[:], row.TraderKey)
		accDiff, err := unmarshalBatchAccountDiff(row)
		if err != nil {
			return nil, fmt.Errorf("unable to unmarshal batch "+
				"account diff: %v", err)
		}
		accDiffs[accountID] = accDiff
	}
	auctioneerFees := btcutil.Amount(batchRow.AuctioneerFeesAccrued)
	tradingFeeReport := matching.TradingFeeReport{
		AccountDiffs:          accDiffs,
		AuctioneerFeesAccrued: auctioneerFees,
	}
	orderBatch.FeeReport = tradingFeeReport

	matchedOrders := make(
		[]matching.MatchedOrder, 0, len(matchedOrderRows),
	)
	for _, matchedOrder := range matchedOrderRows {
		var nonce orderT.Nonce
		copy(nonce[:], matchedOrder.AskOrderNonce)

		ask, ok := orders[nonce].(*order.Ask)
		if !ok {
			return nil, fmt.Errorf("unable to cast ask order %v",
				nonce)
		}
		copy(nonce[:], matchedOrder.BidOrderNonce)
		bid, ok := orders[nonce].(*order.Bid)
		if !ok {
			return nil, fmt.Errorf("unable to cast bid order %v",
				nonce)
		}

		ask.Details().State = orderT.State(matchedOrder.AskState)
		ask.Details().UnitsUnfulfilled = orderT.SupplyUnit(
			matchedOrder.AskUnitsUnmatched,
		)

		bid.Details().State = orderT.State(matchedOrder.BidState)
		bid.Details().UnitsUnfulfilled = orderT.SupplyUnit(
			matchedOrder.BidUnitsUnmatched,
		)

		rate := orderT.FixedRatePremium(matchedOrder.MatchingRate)
		satsCleared := btcutil.Amount(matchedOrder.TotalSatsCleared)
		matched := orderT.SupplyUnit(matchedOrder.UnitsMatched)
		unmatched := orderT.SupplyUnit(matchedOrder.UnitsUnmatched)
		fulfillType := matching.FulfillType(matchedOrder.FulfillType)
		quote := matching.PriceQuote{
			MatchingRate:     rate,
			TotalSatsCleared: satsCleared,
			UnitsMatched:     matched,
			UnitsUnmatched:   unmatched,
			Type:             fulfillType,
		}
		details := matching.OrderPair{
			Ask:   ask,
			Bid:   bid,
			Quote: quote,
		}

		askerDiff, ok := accDiffs[details.Ask.AcctKey]
		if !ok {
			return nil, fmt.Errorf("unable to get asker(%x) batch "+
				"account diff", details.Ask.AcctKey)
		}

		bidderDiff, ok := accDiffs[details.Bid.AcctKey]
		if !ok {
			return nil, fmt.Errorf("unable to get bidder(%x) "+
				"batch account diff", details.Bid.AcctKey)
		}

		asker := getTraderFromBatcAccDiff(askerDiff)
		bidder := getTraderFromBatcAccDiff(bidderDiff)

		// This expires are from the snapshot taken during the batch
		// creation. That means that the account expiry could have
		// been exteneded. The new account expiry could be found in the
		// `diff.NewExpiry`.
		asker.AccountExpiry = uint32(matchedOrder.AskerExpiry)
		bidder.AccountExpiry = uint32(matchedOrder.BidderExpiry)

		order := matching.MatchedOrder{
			Asker:   asker,
			Bidder:  bidder,
			Details: details,
		}

		matchedOrders = append(matchedOrders, order)
	}

	orderBatch.Orders = matchedOrders

	orderBatch.SubBatches = make(map[uint32][]matching.MatchedOrder)
	for _, matchedOrder := range matchedOrders {
		duration := matchedOrder.Details.Ask.LeaseDuration()
		_, ok := orderBatch.SubBatches[duration]
		if !ok {
			orderBatch.SubBatches[duration] = make(
				[]matching.MatchedOrder, 0, 32,
			)
		}

		orderBatch.SubBatches[duration] = append(
			orderBatch.SubBatches[duration], matchedOrder,
		)
	}

	batchSnapshot.OrderBatch = orderBatch

	clearingPrices := make(
		map[uint32]orderT.FixedRatePremium, len(clearingPriceRows),
	)
	for _, row := range clearingPriceRows {
		rate := orderT.FixedRatePremium(row.FixedRatePremium)
		clearingPrices[uint32(row.LeaseDuration)] = rate
	}
	batchSnapshot.OrderBatch.ClearingPrices = clearingPrices

	return batchSnapshot, nil
}

// unmarshalBatchAccountDiff deserializes a *matching.AccountDiff.
func unmarshalBatchAccountDiff(
	row postgres.BatchAccountDiff) (*matching.AccountDiff, error) {

	executionFees := btcutil.Amount(row.TotalExecutionFeesPaid)
	makerFees := btcutil.Amount(row.TotalMakerFeesAccrued)
	accountTally := &orderT.AccountTally{
		EndingBalance:          btcutil.Amount(row.EndingBalance),
		TotalExecutionFeesPaid: executionFees,
		TotalTakerFeesPaid:     btcutil.Amount(row.TotalTakerFeesPaid),
		TotalMakerFeesAccrued:  makerFees,
		NumChansCreated:        uint32(row.NumChansCreated),
	}

	outpoint, err := unmarshalOutPoint(
		row.StartingOutPointHash, row.StartingOutPointIndex,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshall batch account "+
			"diff: %v", err)
	}
	startingState := &matching.Trader{
		AccountExpiry:   uint32(row.StartingAccountExpiry),
		AccountOutPoint: *outpoint,
		AccountBalance:  btcutil.Amount(row.StartingBalance),
	}
	copy(startingState.AccountKey[:], row.TraderKey)
	copy(startingState.BatchKey[:], row.TraderBatchKey)
	copy(startingState.NextBatchKey[:], row.TraderNextBatchKey)
	copy(startingState.VenueSecret[:], row.Secret)

	diff := &matching.AccountDiff{
		AccountTally:  accountTally,
		StartingState: startingState,
		NewExpiry:     uint32(row.NewAccountExpiry),
	}

	if row.TxOutValue.Valid {
		diff.RecreatedOutput = &wire.TxOut{
			Value:    row.TxOutValue.Int64,
			PkScript: row.TxOutPkscript,
		}
	}

	return diff, nil
}

// getTraderFromBatcAccDiff creates a matching.Trader by mapping the
// information of a *matching.AccountDiff.
func getTraderFromBatcAccDiff(diff *matching.AccountDiff) matching.Trader {
	trader := matching.Trader{
		AccountOutPoint: diff.StartingState.AccountOutPoint,
		AccountBalance:  diff.StartingState.AccountBalance,
	}

	copy(trader.AccountKey[:], diff.StartingState.AccountKey[:])
	copy(trader.BatchKey[:], diff.StartingState.BatchKey[:])
	copy(trader.NextBatchKey[:], diff.StartingState.NextBatchKey[:])
	copy(trader.VenueSecret[:], diff.StartingState.VenueSecret[:])

	return trader
}
