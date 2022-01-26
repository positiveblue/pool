package accounting

import (
	"context"
	"fmt"
	"time"

	"github.com/lightninglabs/faraday/fiat"
)

// PriceFunc is a signature for a function that provides usd bitcoin prices
// at a given time.
type PriceFunc func(ts time.Time) (*fiat.Price, error)

// GetPriceFunc queries coin cap's fiat api for prices with hourly granularity
// over the period needed and returns a price function which can be used to
// provide USD prices at specific times within the range.
func GetPriceFunc(startTime, endTime time.Time) (PriceFunc, error) {
	// Sanity check the range we were given.
	if startTime.After(endTime) {
		return nil, fmt.Errorf("start: %v after	end: %v", startTime,
			endTime)
	}

	priceSource, err := fiat.NewPriceSource(&fiat.PriceSourceConfig{
		Backend:     fiat.CoinGeckoPriceBackend,
		Granularity: &fiat.GranularityHour,
	})
	if err != nil {
		return nil, err
	}

	prices, err := priceSource.GetPrices(
		context.Background(), startTime, endTime,
	)
	if err != nil {
		return nil, err
	}

	return func(ts time.Time) (*fiat.Price, error) {
		return fiat.GetPrice(prices, ts)
	}, nil
}
