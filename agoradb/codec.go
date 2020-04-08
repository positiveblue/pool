package agoradb

import (
	"io"

	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/client/clientdb"
	"github.com/lightninglabs/agora/order"
	"github.com/lightninglabs/agora/venue/matching"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/lnwire"
)

// WriteElements is writes each element in the elements slice to the passed
// io.Writer using WriteElement.
func WriteElements(w io.Writer, elements ...interface{}) error {
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
func WriteElement(w io.Writer, element interface{}) error {
	switch e := element.(type) {
	case lsat.TokenID:
		if _, err := w.Write(e[:]); err != nil {
			return err
		}

	case account.State:
		return lnwire.WriteElement(w, uint8(e))

	case order.ChanType:
		return lnwire.WriteElement(w, uint8(e))

	case matching.FulfillType:
		return lnwire.WriteElement(w, uint8(e))

	case matching.AccountID:
		return lnwire.WriteElement(w, e[:])

	case *wire.TxOut:
		// We allow the values of TX outputs to be nil and just write a
		// boolean value for hasValue = false.
		if e == nil {
			return lnwire.WriteElement(w, false)
		}

		// We have a non-nil value, write it.
		return lnwire.WriteElements(
			w, true, btcutil.Amount(e.Value),
			uint32(len(e.PkScript)), e.PkScript,
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

	case *order.ChanType:
		var s uint8
		if err := lnwire.ReadElement(r, &s); err != nil {
			return err
		}
		*e = order.ChanType(s)

	case *matching.FulfillType:
		var s uint8
		if err := lnwire.ReadElement(r, &s); err != nil {
			return err
		}
		*e = matching.FulfillType(s)

	case *matching.AccountID:
		if _, err := io.ReadFull(r, e[:]); err != nil {
			return err
		}

	case **wire.TxOut:
		var (
			hasValue    bool
			value       btcutil.Amount
			pkScriptLen uint32
		)
		// Was this nil when it was serialized?
		if err := lnwire.ReadElement(r, &hasValue); err != nil {
			return err
		}
		if !hasValue {
			return nil
		}

		// Non-nil value, read the rest.
		err := lnwire.ReadElements(r, &value, &pkScriptLen)
		if err != nil {
			return err
		}
		txOut := &wire.TxOut{
			Value:    int64(value),
			PkScript: make([]byte, pkScriptLen),
		}
		if _, err := io.ReadFull(r, txOut.PkScript); err != nil {
			return err
		}
		*e = txOut

	default:
		return clientdb.ReadElement(r, element)
	}

	return nil
}
