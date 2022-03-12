package test

import (
	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightninglabs/pool/terms"
)

type MockFeeScheduler struct {
	defaultFeeSchedule terms.FeeSchedule
}

func NewMockFeeSchedule(baseFee, feeRate btcutil.Amount) *MockFeeScheduler {
	return &MockFeeScheduler{
		defaultFeeSchedule: terms.NewLinearFeeSchedule(
			baseFee, feeRate,
		),
	}
}

// AccountFeeSchedule returns the fee schedule for a specific account.
func (m *MockFeeScheduler) AccountFeeSchedule(
	_ [33]byte) terms.FeeSchedule {

	return m.defaultFeeSchedule
}

// TraderFeeSchedule returns the fee schedule for a trader identified by
// their LSAT ID.
func (m *MockFeeScheduler) TraderFeeSchedule(_ [32]byte) terms.FeeSchedule {
	return m.defaultFeeSchedule
}
