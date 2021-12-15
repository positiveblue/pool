package traderterms

import (
	"bytes"
	"testing"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/stretchr/testify/require"
)

var (
	testAmount1 btcutil.Amount = 123
	testAmount2 btcutil.Amount = 34567890
)

// TestCustom makes sure encoding and decoding of custom trader terms works as
// expected.
func TestCustom(t *testing.T) {
	testValues := []*Custom{
		{},
		{
			TraderID: lsat.TokenID{11, 22, 33, 44},
		},
		{
			TraderID: lsat.TokenID{11, 22, 33, 44},
			FeeRate:  &testAmount1,
		},
		{
			TraderID: lsat.TokenID{11, 22, 33, 44},
			FeeRate:  &testAmount1,
			BaseFee:  &testAmount2,
		},
	}

	for _, testValue := range testValues {
		val := testValue

		var buf bytes.Buffer
		require.NoError(t, SerializeCustom(&buf, val))

		decoded := &Custom{}
		require.NoError(t, DeserializeCustom(&buf, decoded))

		require.Equal(t, val, decoded)
	}
}
