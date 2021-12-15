package subastadb

import (
	"context"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/subasta/traderterms"
	"gorm.io/gorm/clause"
)

// SQLTraderTerms is the SQL model for traderterms.Custom.
type SQLTraderTerms struct {
	// TraderID is the ID that identifies the targeted trading client that
	// these settings apply to. It is the LSAT token ID encoded in the
	// macaroon ID part.
	TraderID string `gorm:"primaryKey"`

	// BaseFee is the base fee the auctioneer will charge the trader for
	// each executed order. If this is nil then the default base fee will be
	// charged.
	BaseFee *int64 // btcutil.Amount

	// FeeRate is the fee rate in parts per million the auctioneer will
	// charge the trader for each executed order. If this is nil then the
	// default fee rate will be charged.
	FeeRate *int64 // btcutil.Amount
}

// TableName overrides the default table name.
func (SQLTraderTerms) TableName() string {
	return "traderterms"
}

// toTraderTerms converts this SQLTraderTerms to a traderterms.Custom.
func (s *SQLTraderTerms) toTraderTerms() (*traderterms.Custom, error) {
	tokenID, err := lsat.MakeIDFromString(s.TraderID)
	if err != nil {
		return nil, err
	}

	terms := &traderterms.Custom{
		TraderID: tokenID,
	}
	if s.BaseFee != nil {
		tmp := btcutil.Amount(*s.BaseFee)
		terms.BaseFee = &tmp
	}
	if s.FeeRate != nil {
		tmp := btcutil.Amount(*s.FeeRate)
		terms.FeeRate = &tmp
	}

	return terms, nil
}

// UpdateTraderTerms attempts to insert or update the custom trader terms using
// the parent SQL transaction.
func (s *SQLTransaction) UpdateTraderTerms(t *traderterms.Custom) error {
	sqlTerms := &SQLTraderTerms{
		TraderID: t.TraderID.String(),
	}

	if t.BaseFee != nil {
		tmp := int64(*t.BaseFee)
		sqlTerms.BaseFee = &tmp
	}
	if t.FeeRate != nil {
		tmp := int64(*t.FeeRate)
		sqlTerms.FeeRate = &tmp
	}

	return s.tx.Clauses(
		clause.OnConflict{UpdateAll: true},
	).Create(sqlTerms).Error
}

// GetTraderTerms will select the custom trader terms corresponding to the
// passed trader ID or nil if no terms exist.
func (s *SQLTransaction) GetTraderTerms(
	traderID lsat.TokenID) (*traderterms.Custom, error) { // nolint

	var terms []SQLTraderTerms

	result := s.tx.Where(
		"trader_id = ?", traderID.String(),
	).Find(&terms)

	if result.Error != nil {
		return nil, result.Error
	}

	if len(terms) == 0 {
		return nil, nil
	}

	return terms[0].toTraderTerms()
}

// UpdateTraderTermsSQL is a helper to insert or update custom trader terms into
// our SQL database. It does not return any errors to not block normal
// processing as we only intend to mirror into SQL and our main source of truth
// is etcd.
func UpdateTraderTermsSQL(ctx context.Context, store *SQLStore,
	terms ...*traderterms.Custom) {

	if store == nil || len(terms) == 0 {
		return
	}

	err := store.Transaction(ctx, func(tx *SQLTransaction) error {
		for _, term := range terms {
			if err := tx.UpdateTraderTerms(term); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		log.Warnf("Unable to store accounts to SQL: %v", err)
	}
}
