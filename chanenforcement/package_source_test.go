package chanenforcement

import (
	"testing"

	gomock "github.com/golang/mock/gomock"
	"github.com/lightninglabs/subasta/ban"
	"github.com/stretchr/testify/require"
)

// TestDefaultEnforceLifetimeViolation checks that after enforcing a violation
// the account/node gets banned and the the lifetimePackage is removed.
func TestDefaultEnforceLifetimeViolation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	banManager := ban.NewMockManager(mockCtrl)
	store := NewMockStore(mockCtrl)
	source := NewDefaultSource(banManager, store)

	currentHeight := uint32(100)
	pkg := &LifetimePackage{
		AskAccountKey: testAskAccountKey,
		AskNodeKey:    testAskNodeKey,
	}

	banManager.EXPECT().GetAccountBan(pkg.AskAccountKey, currentHeight)
	banManager.EXPECT().CalculateNewInfo(currentHeight, gomock.Any())
	banManager.EXPECT().GetNodeBan(pkg.AskNodeKey, currentHeight)
	banManager.EXPECT().CalculateNewInfo(currentHeight, gomock.Any())

	store.EXPECT().EnforceLifetimeViolation(
		gomock.Any(), pkg, pkg.AskAccountKey, pkg.AskNodeKey,
		gomock.Any(), gomock.Any(),
	)

	err := source.EnforceLifetimeViolation(pkg, currentHeight)
	require.NoError(t, err)
}
