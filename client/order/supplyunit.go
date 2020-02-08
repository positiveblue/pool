package order

import "github.com/btcsuite/btcutil"

// SupplyUnit is a type that represents the smallest unit of an order that can
// be fulfilled. One unit corresponds to the smallest channel size that can be
// bought or sold in the system.
type SupplyUnit uint64

const (
	// BaseSupplyUnit is the smallest channel that can be bought or sold in
	// the system. These units are expressed in satoshis.
	BaseSupplyUnit SupplyUnit = 100_000
)

// NewSupplyFromSats calculates the number of supply units that can be bought or
// sold with a given amount in satoshis.
func NewSupplyFromSats(sats btcutil.Amount) SupplyUnit {
	// TODO(roasbeef): ensure proper rounding, etc.

	return SupplyUnit(uint64(sats) / uint64(BaseSupplyUnit))
}

// ToSatoshis maps a set number of supply units to the corresponding number of
// satoshis.
func (s SupplyUnit) ToSatoshis() btcutil.Amount {
	return btcutil.Amount(uint64(s) * uint64(BaseSupplyUnit))
}
