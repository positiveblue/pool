package account

import (
	"reflect"
	"testing"

	"github.com/btcsuite/btcd/wire"
)

// TestAccountCopy ensures that a deep copy of an account is constructed
// successfully.
func TestAccountCopy(t *testing.T) {
	t.Parallel()

	a := &Account{
		TokenID:       testTokenID,
		Value:         1,
		Expiry:        2,
		TraderKeyRaw:  [33]byte{0x1, 0x3, 0x3, 0x7},
		AuctioneerKey: testAuctioneerKeyDesc,
		BatchKey:      testTraderKey,
		Secret:        [32]byte{0x1, 0x2, 0x3},
		State:         StateOpen,
		HeightHint:    1,
		OutPoint:      wire.OutPoint{Index: 1},
	}

	a.Value = 2
	aCopy := a.Copy(ValueModifier(2))

	if !reflect.DeepEqual(aCopy, a) {
		t.Fatal("deep copy with modifier does not match expected result")
	}
}
