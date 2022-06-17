package venue

import (
	"regexp"
	"testing"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/stretchr/testify/require"
)

var logPattern = regexp.MustCompile(
	`\{"traders":\["\d+","\d+","\d+"],"nonces":\["\w+","\w+"]}`,
)

func TestErrMissingTradersFormatting(t *testing.T) {
	err := &ErrMissingTraders{
		TraderKeys: map[matching.AccountID]struct{}{
			{1, 2, 3}: {},
			{3, 4, 5}: {},
			{5, 5, 9}: {},
		},
		OrderNonces: map[orderT.Nonce]struct{}{
			{77, 88, 99}: {},
			{}:           {},
		},
	}

	require.True(t, logPattern.MatchString(err.Error()), err.Error())
}
