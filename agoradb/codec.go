package agoradb

import (
	"io"

	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/client/clientdb"
	"github.com/lightninglabs/agora/order"
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

	default:
		return clientdb.ReadElement(r, element)
	}

	return nil
}
