package traderterms

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightningnetwork/lnd/tlv"
)

const (
	// traderIDType is the tlv type we use to encode the trader's LSAT ID.
	traderIDType tlv.Type = 1

	// baseFeeType is the tlv type we use to encode the custom term's base
	// fee.
	baseFeeType tlv.Type = 2

	// feeRateType is the tlv type we use to encode the custom term's fee
	// rate.
	feeRateType tlv.Type = 3
)

// Custom is a struct for storing trader specific terms or settings.
type Custom struct {
	// TraderID is the ID that identifies the targeted trading client that
	// these settings apply to. It is the LSAT token ID encoded in the
	// macaroon ID part.
	TraderID lsat.TokenID

	// BaseFee is the base fee the auctioneer will charge the trader for
	// each executed order. If this is nil then the default base fee will be
	// charged.
	BaseFee *btcutil.Amount

	// FeeRate is the fee rate in parts per million the auctioneer will
	// charge the trader for each executed order. If this is nil then the
	// default fee rate will be charged.
	FeeRate *btcutil.Amount
}

// Store is the interface that a persistent store must implement to read and
// write custom trader terms.
type Store interface {
	// AllTraderTerms returns all trader terms currently in the store.
	AllTraderTerms(ctx context.Context) ([]*Custom, error)

	// GetTraderTerms returns the trader terms for the given trader or
	// ErrNoTerms if there are no terms stored for that trader.
	GetTraderTerms(ctx context.Context, traderID lsat.TokenID) (*Custom,
		error)

	// PutTraderTerms stores a trader terms item, replacing the previous one
	// if an item with the same ID existed.
	PutTraderTerms(ctx context.Context, terms *Custom) error

	// DelTraderTerms removes the trader specific terms for the given trader
	// ID.
	DelTraderTerms(ctx context.Context, traderID lsat.TokenID) error
}

// String returns a human readable string representation of the custom trader
// terms.
func (c *Custom) String() string {
	fmtPtr := func(ptr *btcutil.Amount) string {
		if ptr == nil {
			return "<nil>"
		}

		return strconv.FormatInt(int64(*ptr), 10)
	}
	return fmt.Sprintf("trader_id=%x, base_fee=%v, fee_rate=%v",
		c.TraderID[:], fmtPtr(c.BaseFee), fmtPtr(c.FeeRate))
}

// FeeSchedule returns a fee schedule that applies any set values of the custom
// trader terms or falls back to the given default schedule for nil fields.
func (c *Custom) FeeSchedule(defaultSchedule terms.FeeSchedule) terms.FeeSchedule {
	baseFee := defaultSchedule.BaseFee()
	feeRate := defaultSchedule.ExecutionFee(1_000_000)

	if c.BaseFee != nil {
		baseFee = *c.BaseFee
	}
	if c.FeeRate != nil {
		feeRate = *c.FeeRate
	}

	return terms.NewLinearFeeSchedule(baseFee, feeRate)
}

// SerializeCustom encodes all data contained in a custom trader terms struct
// as a single tlv stream.
func SerializeCustom(w io.Writer, c *Custom) error {
	traderID := [32]byte(c.TraderID)
	tlvRecords := []tlv.Record{
		tlv.MakePrimitiveRecord(traderIDType, &traderID),
	}

	if c.BaseFee != nil {
		baseFee := uint64(*c.BaseFee)
		tlvRecords = append(tlvRecords, tlv.MakePrimitiveRecord(
			baseFeeType, &baseFee,
		))
	}

	if c.FeeRate != nil {
		feeRate := uint64(*c.FeeRate)
		tlvRecords = append(tlvRecords, tlv.MakePrimitiveRecord(
			feeRateType, &feeRate,
		))
	}

	tlvStream, err := tlv.NewStream(tlvRecords...)
	if err != nil {
		return err
	}

	return tlvStream.Encode(w)
}

// DeserializeCustom decodes all data of a reader into a custom trader terms
// struct interpreting it as a single tlv stream.
func DeserializeCustom(r io.Reader, c *Custom) error {
	var (
		traderID [32]byte
		baseFee  uint64
		feeRate  uint64
	)

	tlvStream, err := tlv.NewStream(
		tlv.MakePrimitiveRecord(traderIDType, &traderID),
		tlv.MakePrimitiveRecord(baseFeeType, &baseFee),
		tlv.MakePrimitiveRecord(feeRateType, &feeRate),
	)
	if err != nil {
		return err
	}

	parsedTypes, err := tlvStream.DecodeWithParsedTypes(r)
	if err != nil {
		return err
	}
	c.TraderID = traderID

	// The map value being nil means everything could be parsed, no unknown
	// data was found.
	if t, ok := parsedTypes[baseFeeType]; ok && t == nil {
		baseFeeAmt := btcutil.Amount(baseFee)
		c.BaseFee = &baseFeeAmt
	}

	if t, ok := parsedTypes[feeRateType]; ok && t == nil {
		feeRateAmt := btcutil.Amount(feeRate)
		c.FeeRate = &feeRateAmt
	}

	return nil
}
