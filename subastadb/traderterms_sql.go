package subastadb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/jackc/pgx/v4"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/subasta/subastadb/postgres"
	"github.com/lightninglabs/subasta/traderterms"
)

// AllTraderTerms returns all trader terms currently in the store.
func (s *SQLStore) AllTraderTerms(ctx context.Context) ([]*traderterms.Custom,
	error) {

	var rows []postgres.TarderTerm
	txBody := func(txQueries *postgres.Queries) error {
		var err error
		params := postgres.GetTraderTermsParams{}
		rows, err = txQueries.GetTraderTerms(ctx, params)
		if err != nil {
			return err
		}
		return nil
	}

	err := s.ExecTx(ctx, txBody)
	if err != nil {
		return nil, fmt.Errorf("unable to get trader terms: %v", err)
	}

	terms := make([]*traderterms.Custom, 0, len(rows))
	for _, row := range rows {
		terms = append(terms, unmarshalTraderTerm(row))
	}

	return terms, nil
}

// GetTraderTerms returns the trader terms for the given trader or
// ErrNoTerms if there are no terms stored for that trader.
func (s *SQLStore) GetTraderTerms(ctx context.Context,
	traderID lsat.TokenID) (*traderterms.Custom, error) {

	errMsg := "unable to get trader terms: %w"

	terms, err := s.queries.GetTraderTermsByTokenID(ctx, traderID[:])
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf(errMsg, ErrNoTerms)

	case err != nil:
		return nil, fmt.Errorf(errMsg, err)

	default:
		return unmarshalTraderTerm(terms), nil
	}
}

// PutTraderTerms stores a trader terms item, replacing the previous one
// if an item with the same ID existed.
func (s *SQLStore) PutTraderTerms(ctx context.Context,
	terms *traderterms.Custom) error {

	errMsg := "unable to put trader terms(%x): %v"

	var baseFee, feeRate sql.NullInt64
	if terms.BaseFee != nil {
		baseFee.Int64, baseFee.Valid = int64(*terms.BaseFee), true
	}
	if terms.FeeRate != nil {
		if err := feeRate.Scan(int64(*terms.FeeRate)); err != nil {
			return fmt.Errorf(errMsg, terms.TraderID, err)
		}
	}
	params := postgres.UpsertTraderTermsParams{
		TokenID: terms.TraderID[:],
		BaseFee: baseFee,
		FeeRate: feeRate,
	}

	err := s.queries.UpsertTraderTerms(ctx, params)
	if err != nil {
		return fmt.Errorf(errMsg, terms.TraderID, err)
	}

	return nil
}

// DelTraderTerms removes the trader specific terms for the given trader
// ID.
func (s *SQLStore) DelTraderTerms(ctx context.Context,
	traderID lsat.TokenID) error {

	errMsg := "unable to delete trader terms(%x): %w"

	_, err := s.queries.DeleteTraderTerms(ctx, traderID[:])
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf(errMsg, traderID, ErrNoTerms)

	case err != nil:
		return fmt.Errorf(errMsg, traderID, err)

	default:
		return nil
	}
}

// unmarshalTraderTerm deserializes a *traderterms.Custom from its serialized
// version used in the db.
func unmarshalTraderTerm(row postgres.TarderTerm) *traderterms.Custom {
	var tokenID lsat.TokenID
	copy(tokenID[:], row.TokenID)

	traderTerms := &traderterms.Custom{
		TraderID: tokenID,
	}

	if row.BaseFee.Valid {
		baseFee := btcutil.Amount(row.BaseFee.Int64)
		traderTerms.BaseFee = &baseFee
	}

	if row.FeeRate.Valid {
		feeRate := btcutil.Amount(row.FeeRate.Int64)
		traderTerms.FeeRate = &feeRate
	}

	return traderTerms
}
