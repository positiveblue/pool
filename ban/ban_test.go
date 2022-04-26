package ban

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	gomock "github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

var (
	traderKeyStr = "036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0af" +
		"58e0c9395446ba09"
	traderKeyRaw, _ = hex.DecodeString(traderKeyStr)
	traderKey, _    = btcec.ParsePubKey(traderKeyRaw)

	// Reuse the same key for trader/node instead of creating a new one.
	nodeKey, _ = btcec.ParsePubKey(traderKeyRaw)

	errExpected = errors.New("random error")
)

func TestCalculateNewInfo(t *testing.T) {
	banManager := NewManager(nil)

	currentHeight := uint32(500)

	// If there is no active ban we use the default value.
	info := banManager.CalculateNewInfo(500, nil)
	require.Equal(t, currentHeight+initialBanDuration, info.Expiration())

	// If the account is already banned, we restart the timer + double the
	// ban duration.
	currentBan := &Info{Duration: 288}
	info = banManager.CalculateNewInfo(currentHeight, currentBan)
	require.Equal(
		t, currentHeight+currentBan.Duration*2, info.Expiration(),
	)
}

var banAccountTestCases = []struct {
	name              string
	currentInfo       *Info
	expectedBanExpiry uint32
	expectedError     error
}{{
	name: "we are able to ban an account successfully for first time",

	currentInfo:       nil,
	expectedBanExpiry: 644,
	expectedError:     nil,
}, {
	name: "accounts that are already banned get an extension",

	currentInfo: &Info{
		Duration: 200,
	},
	expectedError: nil,
}, {
	name:          "the manager bubbles up the storage errors",
	expectedError: errExpected,
}}

func TestBanManagerBanAccount(t *testing.T) { // nolint:dupl
	for _, tc := range banAccountTestCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockCtrl := gomock.NewController(t)
			defer mockCtrl.Finish()

			currentHeight := uint32(500)

			store := NewMockStore(mockCtrl)
			cfg := &ManagerConfig{
				Store: store,
			}

			banManager := NewManager(cfg)
			info := banManager.CalculateNewInfo(
				currentHeight, tc.currentInfo,
			)

			store.EXPECT().
				GetAccountBan(gomock.Any(), traderKey).
				Return(tc.currentInfo, nil)

			// We will assert that the ban Info has the correct
			// expiration later.
			store.EXPECT().
				BanAccount(gomock.Any(), traderKey, info).
				Return(tc.expectedError)

			expireHeight, err := banManager.BanAccount(
				traderKey, currentHeight,
			)

			if tc.expectedError != nil {
				require.EqualError(
					t, err, tc.expectedError.Error(),
				)
				return
			}
			require.NoError(t, err)

			require.Equal(t, info.Expiration(), expireHeight)
		})
	}
}

var banNodeTestCases = []struct {
	name              string
	currentInfo       *Info
	expectedBanExpiry uint32
	expectedError     error
}{{
	name: "we are able to ban an node successfully for first time",

	currentInfo:       nil,
	expectedBanExpiry: 544,
	expectedError:     nil,
}, {
	name: "nodes that are already banned get an extension",

	currentInfo: &Info{
		Duration: 200,
	},
	expectedError: nil,
}, {
	name:          "the manager bubbles up the storage errors",
	expectedError: errExpected,
}}

func TestBanManagerBanNode(t *testing.T) { // nolint:dupl
	for _, tc := range banNodeTestCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockCtrl := gomock.NewController(t)
			defer mockCtrl.Finish()

			currentHeight := uint32(400)

			store := NewMockStore(mockCtrl)
			cfg := &ManagerConfig{
				Store: store,
			}

			banManager := NewManager(cfg)
			info := banManager.CalculateNewInfo(
				currentHeight, tc.currentInfo,
			)

			store.EXPECT().
				GetNodeBan(gomock.Any(), nodeKey).
				Return(tc.currentInfo, nil)

			// We will assert that the ban Info has the correct
			// expiration later.
			store.EXPECT().
				BanNode(gomock.Any(), nodeKey, info).
				Return(tc.expectedError)

			expireHeight, err := banManager.BanNode(
				nodeKey, currentHeight,
			)

			if tc.expectedError != nil {
				require.EqualError(
					t, err, tc.expectedError.Error(),
				)
				return
			}
			require.NoError(t, err)

			require.Equal(t, info.Expiration(), expireHeight)
		})
	}
}

var isTraderBannedTestCases = []struct {
	name                   string
	currentHeight          uint32
	curentAccountBan       *Info
	curretNodeBan          *Info
	expectedIsTraderBanned bool
}{{
	name: "traders without account/node bans are not banned",

	currentHeight:          500,
	curentAccountBan:       nil,
	curretNodeBan:          nil,
	expectedIsTraderBanned: false,
}, {
	name: "traders with an active account ban are banned",

	currentHeight:          500,
	curentAccountBan:       &Info{Height: 400, Duration: 200},
	curretNodeBan:          nil,
	expectedIsTraderBanned: true,
}, {
	name: "traders with an expired account ban are not banned",

	currentHeight:          500,
	curentAccountBan:       &Info{Height: 300, Duration: 100},
	curretNodeBan:          nil,
	expectedIsTraderBanned: false,
}, {
	name: "traders with an active node ban are banned",

	currentHeight:          500,
	curentAccountBan:       nil,
	curretNodeBan:          &Info{Height: 400, Duration: 200},
	expectedIsTraderBanned: true,
}, {
	name: "traders with an expired node ban are not banned",

	currentHeight:          500,
	curentAccountBan:       nil,
	curretNodeBan:          &Info{Height: 300, Duration: 100},
	expectedIsTraderBanned: false,
}}

func TestBanManagerIsTraderBanned(t *testing.T) {
	for _, tc := range isTraderBannedTestCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockCtrl := gomock.NewController(t)
			defer mockCtrl.Finish()

			store := NewMockStore(mockCtrl)
			store.EXPECT().
				GetAccountBan(gomock.Any(), traderKey).
				Return(tc.curentAccountBan, nil)

			if tc.curentAccountBan == nil ||
				tc.curentAccountBan.ExceedsBanExpiration(
					tc.currentHeight,
				) {

				store.EXPECT().
					GetNodeBan(gomock.Any(), nodeKey).
					Return(tc.curretNodeBan, nil)
			}

			cfg := &ManagerConfig{
				Store: store,
			}
			banManager := NewManager(cfg)

			var accKey, nodKey [33]byte
			copy(accKey[:], traderKey.SerializeCompressed())
			copy(nodKey[:], nodeKey.SerializeCompressed())
			isBanned, err := banManager.IsTraderBanned(
				accKey, nodKey, tc.currentHeight,
			)
			require.NoError(t, err)

			require.Equal(t, tc.expectedIsTraderBanned, isBanned)
		})
	}
}
