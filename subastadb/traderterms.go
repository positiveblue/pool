package subastadb

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/subasta/traderterms"
	conc "go.etcd.io/etcd/client/v3/concurrency"
)

var (
	// traderTermsPrefix is the prefix that we'll use to store all trader
	// specific terms data. From the top level directory, this path is:
	// bitcoin/clm/subasta/<network>/traderterms.
	traderTermsPrefix = "traderterms"

	// ErrNoTerms is the error that is returned if no trader specific terms
	// can be found in the database.
	ErrNoTerms = errors.New("no trader specific terms found")
)

// getTraderTermsKey returns the key path for the terms stored for the given
// LSAT ID.
func (s *EtcdStore) getTraderTermsKey(lsatID lsat.TokenID) string {
	// bitcoin/clm/subasta/<network>/traderterms/<LSAT-ID>.
	return strings.Join(
		[]string{s.getKeyPrefix(traderTermsPrefix), lsatID.String()},
		keyDelimiter,
	)
}

// AllTraderTerms returns all trader terms currently in the store.
func (s *EtcdStore) AllTraderTerms(ctx context.Context) ([]*traderterms.Custom,
	error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	prefix := s.getKeyPrefix(traderTermsPrefix)
	resultMap, err := s.getAllValuesByPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}

	allTerms := make([]*traderterms.Custom, 0, len(resultMap))
	for _, termsBytes := range resultMap {
		terms := &traderterms.Custom{}
		r := bytes.NewReader(termsBytes)
		if err := traderterms.DeserializeCustom(r, terms); err != nil {
			return nil, err
		}

		allTerms = append(allTerms, terms)
	}

	return allTerms, nil
}

// GetTraderTerms returns the trader terms for the given trader or ErrNoTerms if
// there are no terms stored for that trader.
func (s *EtcdStore) GetTraderTerms(ctx context.Context,
	traderID lsat.TokenID) (*traderterms.Custom, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	terms := &traderterms.Custom{}
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		key := s.getTraderTermsKey(traderID)
		rawTerms := stm.Get(key)
		if rawTerms == "" {
			return ErrNoTerms
		}

		r := strings.NewReader(rawTerms)
		return traderterms.DeserializeCustom(r, terms)
	})
	if err != nil {
		return nil, err
	}

	return terms, nil
}

// PutTraderTerms stores a trader terms item, replacing the previous one
// if an item with the same ID existed.
func (s *EtcdStore) PutTraderTerms(ctx context.Context,
	terms *traderterms.Custom) error {

	if !s.initialized {
		return errNotInitialized
	}

	var buf bytes.Buffer
	if err := traderterms.SerializeCustom(&buf, terms); err != nil {
		return err
	}

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		key := s.getTraderTermsKey(terms.TraderID)
		stm.Put(key, buf.String())

		return nil
	})

	return err
}

// DelTraderTerms removes the trader specific terms for the given trader ID.
func (s *EtcdStore) DelTraderTerms(ctx context.Context,
	traderID lsat.TokenID) error {

	if !s.initialized {
		return errNotInitialized
	}

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		key := s.getTraderTermsKey(traderID)
		stm.Del(key)

		return nil
	})

	return err
}
