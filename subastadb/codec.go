package subastadb

import (
	"bytes"
	"io"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/pool/clientdb"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/chanbackup"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/tlv"
)

const (
	// userAgentType is the tlv record type for the order's user agent
	// string.
	userAgentType tlv.Type = 1

	// bidSelfChanBalanceType is the tlv type we use to store the self
	// channel balance on bid orders.
	bidSelfChanBalanceType tlv.Type = 2

	// bidIsSidecarType is the tlv record type for the sidecar flag of a bid
	// order.
	bidIsSidecarType tlv.Type = 3
)

// WriteElements is writes each element in the elements slice to the passed
// io.Writer using WriteElement.
func WriteElements(w *bytes.Buffer, elements ...interface{}) error {
	for _, element := range elements {
		err := WriteElement(w, element)
		if err != nil {
			return err
		}
	}
	return nil
}

// WriteElement is a one-stop shop to write the big endian representation of
// any element which is to be serialized. The passed io.Writer should be backed
// by an appropriately sized byte slice, or be able to dynamically expand to
// accommodate additional data.
func WriteElement(w *bytes.Buffer, element interface{}) error {
	switch e := element.(type) {
	case lsat.TokenID:
		if _, err := w.Write(e[:]); err != nil {
			return err
		}

	case account.State:
		return lnwire.WriteElement(w, uint8(e))

	case orderT.ChannelType:
		return lnwire.WriteElement(w, uint8(e))

	case order.DurationBucketState:
		return lnwire.WriteElement(w, uint8(e))

	case matching.FulfillType:
		return lnwire.WriteElement(w, uint8(e))

	case matching.AccountID:
		return lnwire.WriteElement(w, e[:])

	case chanbackup.SingleBackupVersion:
		return lnwire.WriteElement(w, uint8(e))

	case []byte:
		return lnwire.WriteElements(w, uint32(len(e)), e)

	case *wire.TxOut:
		// We allow the values of TX outputs to be nil and just write a
		// boolean value for hasValue = false.
		if e == nil {
			return WriteElement(w, false)
		}

		// We have a non-nil value, write it.
		return WriteElements(
			w, true, btcutil.Amount(e.Value), e.PkScript,
		)

	default:
		return clientdb.WriteElement(w, element)
	}

	return nil
}

// ReadElements deserializes a variable number of elements into the passed
// io.Reader, with each element being deserialized according to the ReadElement
// function.
func ReadElements(r io.Reader, elements ...interface{}) error {
	for _, element := range elements {
		err := ReadElement(r, element)
		if err != nil {
			return err
		}
	}
	return nil
}

// ReadElement is a one-stop utility function to deserialize any datastructure
// encoded using the serialization format of lnwire.
func ReadElement(r io.Reader, element interface{}) error {
	switch e := element.(type) {
	case *lsat.TokenID:
		if _, err := io.ReadFull(r, e[:]); err != nil {
			return err
		}

	case *account.State:
		var s uint8
		if err := lnwire.ReadElement(r, &s); err != nil {
			return err
		}
		*e = account.State(s)

	case *orderT.ChannelType:
		var s uint8
		if err := lnwire.ReadElement(r, &s); err != nil {
			return err
		}
		*e = orderT.ChannelType(s)

	case *order.DurationBucketState:
		var s uint8
		if err := lnwire.ReadElement(r, &s); err != nil {
			return err
		}
		*e = order.DurationBucketState(s)

	case *matching.FulfillType:
		var s uint8
		if err := lnwire.ReadElement(r, &s); err != nil {
			return err
		}
		*e = matching.FulfillType(s)

	case *chanbackup.SingleBackupVersion:
		var s uint8
		if err := lnwire.ReadElement(r, &s); err != nil {
			return err
		}
		*e = chanbackup.SingleBackupVersion(s)

	case *matching.AccountID:
		if _, err := io.ReadFull(r, e[:]); err != nil {
			return err
		}

	case *[]byte:
		var size uint32
		if err := ReadElement(r, &size); err != nil {
			return err
		}
		*e = make([]byte, size)
		if _, err := io.ReadFull(r, *e); err != nil {
			return err
		}

	case **wire.TxOut:
		var (
			hasValue bool
			value    btcutil.Amount
			pkScript []byte
		)
		// Was this nil when it was serialized?
		if err := ReadElement(r, &hasValue); err != nil {
			return err
		}
		if !hasValue {
			return nil
		}

		// Non-nil value, read the rest.
		if err := ReadElements(r, &value, &pkScript); err != nil {
			return err
		}
		*e = &wire.TxOut{
			Value:    int64(value),
			PkScript: pkScript,
		}

	default:
		return clientdb.ReadElement(r, element)
	}

	return nil
}
