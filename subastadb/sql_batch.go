package subastadb

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/venue/matching"
	"gorm.io/gorm/clause"
)

// SQLBatchSnapshot maps a Batch to SQL.
type SQLBatchSnapshot struct {
	BatchID string `gorm:"primaryKey"`

	// BatchTx is the final, signed batch transaction for this batch.
	BatchTx string

	// BatchTxFee is the chain fee paid by the above batch tx.
	BatchTxFee int64

	SQLOrderBatch
}

// TableName overrides the default table name.
func (SQLBatchSnapshot) TableName() string {
	return "batches"
}

// SQLOrderBatch maps a matching.OrderBatch to SQL. Part of a SQLBatchSnapshot.
type SQLOrderBatch struct {
	// Version is the version of the batch execution protocol.
	Version uint32

	// Orders is the set of matched orders in this batch.
	Orders []SQLMatchedOrder `gorm:"foreignKey:BatchID;references:BatchID"`

	// FeeReport is a report describing all the fees paid in the batch.
	// Note that these are only _trading_ fees and don't yet included any
	// fee that need to be paid on chain within the batch execution
	// transaction.
	SQLTradingFeeReport

	// CreationTimestamp is the timestamp at which the batch was first
	// persisted.
	CreationTimestamp time.Time
}

// SQLMatchedOrder maps a matching.MatchedOrder to SQL.
type SQLMatchedOrder struct {
	// BatchID refers to the parent batch.
	BatchID string `gorm:"primaryKey"`

	// Asker is the trader that the ask belongs to.
	Asker SQLTrader `gorm:"embedded;embeddedPrefix:asker_"`

	// Bidder is the trader that the bid belong to.
	Bidder SQLTrader `gorm:"embedded;embeddedPrefix:bidder_"`

	// LeaseDuration identifies how long this order wishes to acquire or
	// lease out capital in the Lightning Network for.
	LeaseDuration uint32

	// ClearingPrices is the clearing price mapped to the lease (unit is
	// FixedRatePremium)
	ClearingPrice uint32

	// SQLOrderPair holds the matched order details and the price quote.
	SQLOrderPair
}

// TableName overrides the default table name.
func (SQLMatchedOrder) TableName() string {
	return "matched_orders"
}

// SQLTrader maps a matching.Trader to SQL. Part of the SQLAccountDiff.
type SQLTrader struct {
	// AccountKey is the account key of a trader.
	AccountKey string `gorm:"primaryKey"`

	// BatchKey is the CURRENT batch key of the trader, this will be used to
	// generate the script when spending from a trader's account.
	BatchKey string

	// NextBatchKey is the NEXT batch key of the trader, this will be used
	// to generate all the scripts we need for the trader's outputs in the
	// batch execution transaction.
	NextBatchKey string

	// VenueSecret is a shared secret that the venue shares with the
	// trader.
	VenueSecret string

	// AccountExpiry is the absolute block height that this account expires
	// after.
	AccountExpiry uint32

	// AccountOutPoint is the account point that will be used to generate the
	// batch execution transaction for this trader to re-spend their
	// account.
	AccountOutPoint string

	// AccountBalance is the current account balance of this trader. All
	// trading fees and chain fees will be extracted from this value.
	AccountBalance int64
}

// SQLOrderPair maps an OrderPair to SQL.
type SQLOrderPair struct {
	// AskNonce is the nonce of the ask order.
	Ask SQLAskOrder `gorm:"embedded;embeddedPrefix:ask_"`

	// Bid is the nonce of the bid order.
	Bid SQLBidOrder `gorm:"embedded;embeddedPrefix:bid_"`

	// SQLPriceQuote is the price quote for the matched order.
	SQLPriceQuote
}

type SQLMatchedBidOrder struct {
	SQLBidOrder
}

func (SQLMatchedBidOrder) TableName() string {
	return "batch_bids"
}

type SQLMatchedAskOrder struct {
	SQLBidOrder
}

func (SQLMatchedAskOrder) TableName() string {
	return "batch_asks"
}

// SQLPriceQuote maps a matching.PriceQuote to SQL. Part of the SQLMatchedOrder.
type SQLPriceQuote struct {
	// MatchingRate is the rate that the two orders matched at. This rate
	// is the bidder's price.
	MatchingRate uint32

	// TotalSatsCleared is the total amount of satoshis cleared, or the
	// channel size that will ultimately be created once the order is
	// executed.
	TotalSatsCleared int64

	// UnitsMatched is the total amount of units matched in this price
	// quote.
	UnitsMatched uint64

	// UnitsUnmatched is the total amount of units that remain unmatched in
	// this price quote.
	UnitsUnmatched uint64

	// Type is the type of fulfil possibly with this price quote.
	Type uint8
}

// SQLTradingFeeReport maps a matching.TradingFeeReport to SQL. Part of
// SQLBatchSnapshot (as a member of the SQLOrderBatch).
type SQLTradingFeeReport struct {
	// accountdiffs maps a trader's account id to an account diff.
	AccountDiffs []SQLAccountDiff `gorm:"foreignKey:BatchID;references:BatchID"`

	// AuctioneerFees is the total amount of satoshis the auctioneer gained
	// in this batch. This should be the sum of the TotalExecutionFeesPaid
	// for all accounts in the AccountDiffs map above.
	AuctioneerFeesAccrued int64
}

// SQLAccountDiff maps a trader's account id to an account diff. Part of the
// fee report for a batch along with SQLBatchSnapshot.AuctioneerFeesAccrued.
type SQLAccountDiff struct {
	BatchID string `gorm:"primaryKey"`

	// StartingState is the starting state for a trader's account.
	StartingState SQLTrader `gorm:"embedded;embeddedPrefix:starting_state_"`

	Tally SQLAccountTally `gorm:"embedded:embeddedPrefix:tally_"`

	// RecreatedOutput is the recreated account output in the batch
	// transaction. This is only set if the account had sufficient balance
	// left for a new on-chain output and wasn't considered to be dust.
	RecreatedOutput SQLTxOut `gorm:"embedded;embeddedPrefix:recreated_output_"`
}

// TableName overrides the default table name.
func (SQLAccountDiff) TableName() string {
	return "batch_account_diffs"
}

// SQLAccountTally maps an orderT.AccountTally to SQL. Part of the
// SQLAccountDiff.
type SQLAccountTally struct {
	// EndingBalance is the ending balance for a trader's account.
	EndingBalance int64

	// TotalExecutionFeesPaid is the total amount of fees a trader paid to
	// the venue.
	TotalExecutionFeesPaid int64

	// TotalTakerFeesPaid is the total amount of fees the trader paid to
	// purchase any channels in this batch.
	TotalTakerFeesPaid int64

	// TotalMakerFeesAccrued is the total amount of fees the trader gained
	// by selling channels in this batch.
	TotalMakerFeesAccrued int64

	// NumChansCreated is the number of new channels that were created for
	// one account in a batch. This is needed to calculate the chain fees
	// that need to be paid from that account.
	NumChansCreated uint32
}

// SQLTxOut maps a wire.TxOut to SQL.
type SQLTxOut struct {
	// HasValue is true if the TxOut is valid (isn't nil on the Go side).
	HasValue bool

	// Value is the value of the TxOut in sats.
	Value int64

	// PkScript is the PkScript for the TxOut.
	PkScript string
}

// MakeSQLTxOut creates a new SQLTxOut from a wire.TxOut.
func NewSQLTxOut(txOut *wire.TxOut) *SQLTxOut {
	sqlTxOut := &SQLTxOut{}
	if txOut != nil {
		sqlTxOut.HasValue = true
		sqlTxOut.Value = txOut.Value
		sqlTxOut.PkScript = hex.EncodeToString(txOut.PkScript)
	}

	return sqlTxOut
}

// TxOut will return the wire.TxOut corresponding to this SQLTxOut.
func (s *SQLTxOut) TxOut() (*wire.TxOut, error) {
	if !s.HasValue {
		return nil, nil
	}

	pkScript, err := hex.DecodeString(s.PkScript)
	if err != nil {
		return nil, err
	}

	return &wire.TxOut{
		Value:    s.Value,
		PkScript: pkScript,
	}, nil
}

// traderToSQLTrader converts a matching.Trader to SQLTrader.
func traderToSQLTrader(trader *matching.Trader) SQLTrader {
	return SQLTrader{
		AccountKey:      hex.EncodeToString(trader.AccountKey[:]),
		BatchKey:        hex.EncodeToString(trader.BatchKey[:]),
		NextBatchKey:    hex.EncodeToString(trader.NextBatchKey[:]),
		VenueSecret:     hex.EncodeToString(trader.VenueSecret[:]),
		AccountExpiry:   trader.AccountExpiry,
		AccountOutPoint: trader.AccountOutPoint.String(),
		AccountBalance:  int64(trader.AccountBalance),
	}
}

// traderFromSQLTrader converts a SQLTrader to a matching.Trader.
func traderFromSQLTrader(sqlTrader *SQLTrader) (*matching.Trader, error) {
	accountKey, err := keyFromHexString(sqlTrader.AccountKey)
	if err != nil {
		return nil, err
	}

	batchKey, err := keyFromHexString(sqlTrader.BatchKey)
	if err != nil {
		return nil, err
	}

	nextBatchKey, err := keyFromHexString(sqlTrader.NextBatchKey)
	if err != nil {
		return nil, err
	}

	venueSecretSlice, err := hex.DecodeString(sqlTrader.VenueSecret)
	if err != nil {
		return nil, err
	}
	var venueSecret [32]byte
	copy(venueSecret[:], venueSecretSlice)

	accountOutPoint, err := outpointFromString(sqlTrader.AccountOutPoint)
	if err != nil {
		return nil, err
	}

	trader := &matching.Trader{
		AccountKey:      accountKey,
		BatchKey:        batchKey,
		NextBatchKey:    nextBatchKey,
		VenueSecret:     venueSecret,
		AccountExpiry:   sqlTrader.AccountExpiry,
		AccountOutPoint: *accountOutPoint,
		AccountBalance:  btcutil.Amount(sqlTrader.AccountBalance),
	}

	return trader, nil
}

// toBatchSnapshot converts this SQLBatchSnapshot to a BatchSnapshot.
func (s *SQLBatchSnapshot) toBatchSnapshot() (*BatchSnapshot, error) {
	batchTx, err := msgTxFromString(s.BatchTx)
	if err != nil {
		return nil, err
	}

	batch := &BatchSnapshot{
		BatchTx:    batchTx,
		BatchTxFee: btcutil.Amount(s.BatchTxFee),
	}

	batch.OrderBatch = &matching.OrderBatch{
		Version: orderT.BatchVersion(s.SQLOrderBatch.Version),
		Orders: make(
			[]matching.MatchedOrder, 0, len(s.SQLOrderBatch.Orders),
		),
		SubBatches: make(map[uint32][]matching.MatchedOrder),
		FeeReport: matching.TradingFeeReport{
			AccountDiffs: make(
				map[matching.AccountID]*matching.AccountDiff,
			),
			AuctioneerFeesAccrued: btcutil.Amount(
				s.SQLOrderBatch.SQLTradingFeeReport.AuctioneerFeesAccrued,
			),
		},
		CreationTimestamp: s.SQLOrderBatch.CreationTimestamp,
		ClearingPrices:    make(map[uint32]orderT.FixedRatePremium),
	}

	for _, sqlMatchedOrder := range s.Orders {
		priceQuote := &sqlMatchedOrder.SQLPriceQuote

		asker, err := traderFromSQLTrader(&sqlMatchedOrder.Asker)
		if err != nil {
			return nil, err
		}

		bidder, err := traderFromSQLTrader(&sqlMatchedOrder.Bidder)
		if err != nil {
			return nil, err
		}

		ask, err := sqlMatchedOrder.Ask.toAsk()
		if err != nil {
			return nil, err
		}

		bid, err := sqlMatchedOrder.Bid.toBid()
		if err != nil {
			return nil, err
		}

		matchedOrder := matching.MatchedOrder{
			Asker:  *asker,
			Bidder: *bidder,
			Details: matching.OrderPair{
				Ask: ask,
				Bid: bid,
				Quote: matching.PriceQuote{
					MatchingRate: orderT.FixedRatePremium(
						priceQuote.MatchingRate,
					),
					TotalSatsCleared: btcutil.Amount(
						priceQuote.TotalSatsCleared,
					),
					UnitsMatched: orderT.SupplyUnit(
						priceQuote.UnitsMatched,
					),
					UnitsUnmatched: orderT.SupplyUnit(
						priceQuote.UnitsUnmatched,
					),
					Type: matching.FulfillType(
						priceQuote.Type,
					),
				},
			},
		}

		batch.OrderBatch.Orders = append(
			batch.OrderBatch.Orders, matchedOrder,
		)

		duration := sqlMatchedOrder.LeaseDuration
		batch.OrderBatch.SubBatches[duration] = append(
			batch.OrderBatch.SubBatches[duration], matchedOrder,
		)

		batch.OrderBatch.ClearingPrices[duration] = orderT.FixedRatePremium(
			sqlMatchedOrder.ClearingPrice,
		)
	}

	feeReport := &batch.OrderBatch.FeeReport
	for _, sqlAccoundDiff := range s.SQLTradingFeeReport.AccountDiffs {
		recreatedOutput, err := sqlAccoundDiff.RecreatedOutput.TxOut()
		if err != nil {
			return nil, err
		}

		startingState, err := traderFromSQLTrader(
			&sqlAccoundDiff.StartingState,
		)
		if err != nil {
			return nil, err
		}

		tally := &sqlAccoundDiff.Tally
		accountDiff := &matching.AccountDiff{
			AccountTally: &orderT.AccountTally{
				EndingBalance: btcutil.Amount(
					tally.EndingBalance,
				),
				TotalExecutionFeesPaid: btcutil.Amount(
					tally.TotalExecutionFeesPaid,
				),
				TotalTakerFeesPaid: btcutil.Amount(
					tally.TotalTakerFeesPaid,
				),
				TotalMakerFeesAccrued: btcutil.Amount(
					tally.TotalMakerFeesAccrued,
				),
				NumChansCreated: tally.NumChansCreated,
			},
			StartingState:   startingState,
			RecreatedOutput: recreatedOutput,
		}

		feeReport.AccountDiffs[startingState.AccountKey] = accountDiff
	}

	return batch, nil
}

// UpdateBatch stores a batch snapshot with the given batch ID to SQL.
func (s *SQLTransaction) UpdateBatch(batchID orderT.BatchID,
	batchSnapshot *BatchSnapshot) error {

	sqlBatchID := hex.EncodeToString(batchID[:])
	orderBatch := batchSnapshot.OrderBatch
	feeReport := &batchSnapshot.OrderBatch.FeeReport

	batchTx, err := msgTxToString(batchSnapshot.BatchTx)
	if err != nil {
		return err
	}

	sqlBatch := &SQLBatchSnapshot{
		BatchID:    hex.EncodeToString(batchID[:]),
		BatchTx:    batchTx,
		BatchTxFee: int64(batchSnapshot.BatchTxFee),
		SQLOrderBatch: SQLOrderBatch{
			Version: uint32(orderBatch.Version),
			SQLTradingFeeReport: SQLTradingFeeReport{
				AuctioneerFeesAccrued: int64(
					feeReport.AuctioneerFeesAccrued,
				),
			},
			CreationTimestamp: orderBatch.CreationTimestamp,
		},
	}

	if err := s.tx.Clauses(
		clause.OnConflict{UpdateAll: true},
	).Create(sqlBatch).Error; err != nil {
		return err
	}

	var sqlOrders []SQLMatchedOrder
	for leaseDuration, subBatch := range orderBatch.SubBatches {
		for _, matchedOrder := range subBatch {
			ask, err := newSQLAskOrder(matchedOrder.Details.Ask)
			if err != nil {
				return err
			}

			bid, err := newSQLBidOrder(matchedOrder.Details.Bid)
			if err != nil {
				return err
			}

			quote := &matchedOrder.Details.Quote

			sqlMatchedOrder := SQLMatchedOrder{
				BatchID: sqlBatchID,
				Asker: traderToSQLTrader(
					&matchedOrder.Asker,
				),
				Bidder: traderToSQLTrader(
					&matchedOrder.Bidder,
				),
				LeaseDuration: leaseDuration,
				ClearingPrice: uint32(
					orderBatch.ClearingPrices[leaseDuration],
				),
				SQLOrderPair: SQLOrderPair{
					Ask: *ask,
					Bid: *bid,
					SQLPriceQuote: SQLPriceQuote{
						MatchingRate: uint32(
							quote.MatchingRate,
						),
						TotalSatsCleared: int64(
							quote.TotalSatsCleared,
						),
						UnitsMatched: uint64(
							quote.UnitsMatched,
						),
						UnitsUnmatched: uint64(
							quote.UnitsUnmatched,
						),
						Type: uint8(quote.Type),
					},
				},
			}

			sqlOrders = append(sqlOrders, sqlMatchedOrder)
		}
	}

	if err := s.tx.Clauses(
		clause.OnConflict{UpdateAll: true},
	).Create(sqlOrders).Error; err != nil {
		return err
	}

	sqlAccountDiffs := make(
		[]SQLAccountDiff, 0, len(orderBatch.FeeReport.AccountDiffs),
	)
	for _, accountDiff := range orderBatch.FeeReport.AccountDiffs {
		tally := accountDiff.AccountTally

		sqlAccountDiff := SQLAccountDiff{
			BatchID: sqlBatchID,
			StartingState: traderToSQLTrader(
				accountDiff.StartingState,
			),
			Tally: SQLAccountTally{
				EndingBalance: int64(
					tally.EndingBalance,
				),
				TotalExecutionFeesPaid: int64(
					tally.TotalExecutionFeesPaid,
				),
				TotalTakerFeesPaid: int64(
					tally.TotalTakerFeesPaid,
				),
				TotalMakerFeesAccrued: int64(
					tally.TotalMakerFeesAccrued,
				),
				NumChansCreated: tally.NumChansCreated,
			},
			RecreatedOutput: *NewSQLTxOut(
				accountDiff.RecreatedOutput,
			),
		}

		sqlAccountDiffs = append(sqlAccountDiffs, sqlAccountDiff)
	}

	return s.tx.Clauses(
		clause.OnConflict{UpdateAll: true},
	).Create(sqlAccountDiffs).Error
}

func (s *SQLTransaction) GetBatch(batchID orderT.BatchID) ( // nolint: interfacer
	*BatchSnapshot, error) {

	var batches []SQLBatchSnapshot
	result := s.tx.Where(
		"batch_id  = ?", hex.EncodeToString(batchID[:]),
	).Preload("Orders").Preload("AccountDiffs").Find(&batches)

	if result.Error != nil {
		return nil, result.Error
	}

	if len(batches) == 0 {
		return nil, nil
	}

	return batches[0].toBatchSnapshot()
}

// UpdateBatchesSQL upserts more batches in one transaction to SQL.
func UpdateBatchesSQL(ctx context.Context, store *SQLStore,
	batches map[orderT.BatchID]*BatchSnapshot) {

	if store == nil || len(batches) == 0 {
		return
	}

	err := store.Transaction(ctx, func(tx *SQLTransaction) error {
		for batchID, snapshot := range batches {
			if err := tx.UpdateBatch(batchID, snapshot); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		log.Errorf("Unable to store batches to SQL: %v", err)
	}

}
