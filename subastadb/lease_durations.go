package subastadb

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/lightninglabs/subasta/order"
	conc "go.etcd.io/etcd/client/v3/concurrency"
)

const (
	// leaseDurationsDir is the directory name under which we'll store all
	// lease duration buckets. The key used for each duration consists of
	// the absolute duration in blocks. This needs be prefixed with
	// topLevelDir to obtain the full path.
	leaseDurationsDir = "lease-durations"
)

// leaseDurationPathPrefix returns the key path prefix under which we store
// _all_ lease duration buckets.
//
// The key path prefix is represented as follows:
//	bitcoin/clm/subasta/lease-durations
func (s *EtcdStore) leaseDurationPathPrefix() string {
	parts := []string{leaseDurationsDir}
	return s.getKeyPrefix(strings.Join(parts, keyDelimiter))
}

// leaseDurationKeyPath returns the full path under which we store a lease
// duration bucket.
//
// The key path is represented as follows:
//	bitcoin/clm/subasta/lease-durations/{duration_blocks}
func (s *EtcdStore) leaseDurationKeyPath(duration uint32) string {
	parts := []string{
		s.leaseDurationPathPrefix(), fmt.Sprintf("%d", duration),
	}
	return strings.Join(parts, keyDelimiter)
}

// StoreLeaseDuration persists to disk the given lease duration bucket.
func (s *EtcdStore) StoreLeaseDuration(ctx context.Context, duration uint32,
	marketState order.DurationBucketState) error {

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		// If a given duration already exists, we just overwrite it.
		k := s.leaseDurationKeyPath(duration)

		var buf bytes.Buffer
		if err := WriteElement(&buf, marketState); err != nil {
			return err
		}

		stm.Put(k, buf.String())
		return nil
	})
	return err
}

// LeaseDurations retrieves all lease duration buckets.
func (s *EtcdStore) LeaseDurations(ctx context.Context) (
	map[uint32]order.DurationBucketState, error) {

	resp, err := s.getAllValuesByPrefix(ctx, s.leaseDurationPathPrefix())
	if err != nil {
		return nil, err
	}

	r := bytes.NewReader(nil)
	durations := make(map[uint32]order.DurationBucketState)
	for k, v := range resp {
		// First, parse the duration from the key.
		parts := strings.Split(k, keyDelimiter)
		lastPart := parts[len(parts)-1]
		duration, err := strconv.ParseUint(lastPart, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("error parsing lease duration "+
				"key as uint: %v", err)
		}

		// Read the actual value stored as the market state.
		r.Reset(v)
		var marketState order.DurationBucketState
		if err := ReadElement(r, &marketState); err != nil {
			return nil, err
		}

		durations[uint32(duration)] = marketState
	}

	return durations, nil
}

// RemoveLeaseDuration removes a single lease duration bucket from the database.
func (s *EtcdStore) RemoveLeaseDuration(ctx context.Context,
	duration uint32) error {

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		stm.Del(s.leaseDurationKeyPath(duration))
		return nil
	})
	return err
}
