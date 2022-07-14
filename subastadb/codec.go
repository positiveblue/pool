package subastadb

import (
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
)

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
