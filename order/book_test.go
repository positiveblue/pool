package order_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
	"github.com/golang/mock/gomock"
	"github.com/lightninglabs/lndclient"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/ban"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
)

var (
	testRawAuctioneerKey, _ = hex.DecodeString(
		"02187d1a0e30f4e5016fc1137363ee9e7ed5dde1e6c50f367422336df7a1" +
			"08b716",
	)
	testAuctioneerKey, _  = btcec.ParsePubKey(testRawAuctioneerKey)
	testAuctioneerKeyDesc = &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: account.AuctioneerKeyFamily,
		},
		PubKey: testAuctioneerKey,
	}

	testRawTraderKey, _ = hex.DecodeString(
		"036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0af58e0c93954" +
			"46ba09",
	)
	testTraderKey, _ = btcec.ParsePubKey(testRawTraderKey)

	testAccount = account.Account{
		TraderKeyRaw:  toRawKey(testTraderKey),
		Value:         200_000,
		Expiry:        100,
		AuctioneerKey: testAuctioneerKeyDesc,
		State:         account.StateOpen,
		BatchKey:      testTraderKey,
		Secret:        [32]byte{0x73, 0x65, 0x63, 0x72, 0x65, 0x74},
		HeightHint:    100,
		OutPoint:      wire.OutPoint{Index: 1},
	}

	testAccount2 = account.Account{
		TraderKeyRaw:  toRawKey(testAuctioneerKey),
		Value:         200_000,
		Expiry:        100,
		AuctioneerKey: testAuctioneerKeyDesc,
		State:         account.StateExpired,
		BatchKey:      testTraderKey,
		Secret:        [32]byte{0x73, 0x65, 0x63, 0x72, 0x65, 0x73},
		HeightHint:    100,
		OutPoint:      wire.OutPoint{Index: 12},
	}

	testNonce = [33]byte{5, 6, 7}

	addr, _       = net.ResolveTCPAddr("tcp", "127.0.0.1:9735")
	testNodeAddrs = []net.Addr{addr}
)

type mockSigner struct {
	shouldVerify bool
}

func (s *mockSigner) SignOutputRaw(context.Context, *wire.MsgTx,
	[]*lndclient.SignDescriptor, []*wire.TxOut) ([][]byte, error) {

	return [][]byte{{1, 2, 3}}, nil
}

func (s *mockSigner) ComputeInputScript(context.Context, *wire.MsgTx,
	[]*lndclient.SignDescriptor) ([]*input.Script, error) {

	return nil, fmt.Errorf("unimplemented")
}

func (s *mockSigner) SignMessage(context.Context, []byte,
	keychain.KeyLocator) ([]byte, error) {

	return []byte("signature"), nil
}

func (s *mockSigner) VerifyMessage(context.Context, []byte, []byte,
	[33]byte) (bool, error) {

	return s.shouldVerify, nil
}

func (s *mockSigner) DeriveSharedKey(context.Context, *btcec.PublicKey,
	*keychain.KeyLocator) ([32]byte, error) {

	return [32]byte{4, 5, 6}, nil
}

// MuSig2CreateSession creates a new MuSig2 signing session using the local
// key identified by the key locator. The complete list of all public keys of
// all signing parties must be provided, including the public key of the local
// signing key. If nonces of other parties are already known, they can be
// submitted as well to reduce the number of method calls necessary later on.
func (s *mockSigner) MuSig2CreateSession(context.Context, *keychain.KeyLocator,
	[][32]byte, ...lndclient.MuSig2SessionOpts) (*input.MuSig2SessionInfo,
	error) {

	return nil, nil
}

// MuSig2RegisterNonces registers one or more public nonces of other signing
// participants for a session identified by its ID. This method returns true
// once we have all nonces for all other signing participants.
func (s *mockSigner) MuSig2RegisterNonces(context.Context, [32]byte,
	[][66]byte) (bool, error) {

	return false, nil
}

// MuSig2Sign creates a partial signature using the local signing key
// that was specified when the session was created. This can only be
// called when all public nonces of all participants are known and have
// been registered with the session. If this node isn't responsible for
// combining all the partial signatures, then the cleanup parameter
// should be set, indicating that the session can be removed from memory
// once the signature was produced.
func (s *mockSigner) MuSig2Sign(context.Context, [32]byte, [32]byte,
	bool) ([]byte, error) {

	return nil, nil
}

// MuSig2CombineSig combines the given partial signature(s) with the
// local one, if it already exists. Once a partial signature of all
// participants is registered, the final signature will be combined and
// returned.
func (s *mockSigner) MuSig2CombineSig(context.Context, [32]byte,
	[][]byte) (bool, []byte, error) {

	return false, nil, nil
}

// MuSig2Cleanup removes a session from memory to free up resources.
func (s *mockSigner) MuSig2Cleanup(context.Context, [32]byte) error {
	return nil
}

var validateAccountTestCases = []struct {
	name        string
	acctKeyRaw  []byte
	mockSetter  func(*subastadb.MockStore, *ban.MockManager)
	expectedErr string
}{{
	name:        "invalid account, bad acct key",
	acctKeyRaw:  []byte{1, 2, 3},
	mockSetter:  func(_ *subastadb.MockStore, _ *ban.MockManager) {},
	expectedErr: "invalid",
}, {
	name:       "account not found",
	acctKeyRaw: testRawTraderKey,
	mockSetter: func(s *subastadb.MockStore, _ *ban.MockManager) {
		s.EXPECT().
			Account(gomock.Any(), testTraderKey, true).
			Return(nil, fmt.Errorf("account not found"))
	},
	expectedErr: "account not found",
}, {
	name:       "invalid state",
	acctKeyRaw: testRawAuctioneerKey,
	mockSetter: func(s *subastadb.MockStore, _ *ban.MockManager) {
		s.EXPECT().
			Account(gomock.Any(), testAuctioneerKey, true).
			Return(&testAccount2, nil)
	},
	expectedErr: testAccount2.State.String(),
}, {
	name:       "invalid state",
	acctKeyRaw: testRawTraderKey,
	mockSetter: func(s *subastadb.MockStore, b *ban.MockManager) {
		s.EXPECT().
			Account(gomock.Any(), testTraderKey, true).
			Return(&testAccount, nil)
		b.EXPECT().
			IsAccountBanned(testTraderKey, gomock.Any()).
			Return(true, uint32(10), nil)
	},
	expectedErr: "10",
}, {
	name:       "happy path",
	acctKeyRaw: testRawTraderKey,
	mockSetter: func(s *subastadb.MockStore, b *ban.MockManager) {
		s.EXPECT().
			Account(gomock.Any(), testTraderKey, true).
			Return(&testAccount, nil)
		b.EXPECT().
			IsAccountBanned(testTraderKey, gomock.Any()).
			Return(false, uint32(0), nil)
	},
}}

func TestValidateAccount(t *testing.T) {
	for _, tc := range validateAccountTestCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockCtrl := gomock.NewController(t)
			defer mockCtrl.Finish()

			store := subastadb.NewMockStore(mockCtrl)
			banManager := ban.NewMockManager(mockCtrl)
			tc.mockSetter(store, banManager)

			book := order.NewBook(&order.BookConfig{
				BanManager: banManager,
				Store:      store,
			})

			bestHeight := uint32(100)
			ctxb := context.Background()

			acct, err := book.ValidateAccount(
				ctxb, tc.acctKeyRaw, bestHeight,
			)
			if tc.expectedErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, acct.TraderKeyRaw[:], tc.acctKeyRaw)
		})
	}
}

var validateOrderTestCases = []struct {
	name             string
	failVerification bool
	getOrder         func() order.ServerOrder
	expectedErr      string
}{{
	name:             "invalid signature",
	failVerification: true,
	getOrder: func() order.ServerOrder {
		return ask(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			MinUnitsMatch:    1,
		})
	},
	expectedErr: "signature not valid for public key",
}, {
	name: "ask duration 0",
	getOrder: func() order.ServerOrder {
		return ask(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    0,
			MinUnitsMatch:    1,
		})
	},
	expectedErr: "cannot submit order outside of default 2016",
}, {
	name: "ask invalid duration",
	getOrder: func() order.ServerOrder {
		return ask(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    143,
			MinUnitsMatch:    1,
		})
	},
	expectedErr: "cannot submit order outside of default 2016",
}, {
	name: "bid duration 0",
	getOrder: func() order.ServerOrder {
		return bid(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    0,
			MinUnitsMatch:    1,
		})
	},
	expectedErr: "cannot submit order outside of default 2016",
}, {
	name: "bid invalid duration",
	getOrder: func() order.ServerOrder {
		return bid(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    143,
			MinUnitsMatch:    1,
		})
	},
	expectedErr: "cannot submit order outside of default 2016",
}, {
	name: "zero amount",
	getOrder: func() order.ServerOrder {
		return ask(orderT.Kit{
			Amt:              0,
			Units:            orderT.NewSupplyFromSats(0),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(0),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
		})
	},
	expectedErr: "order amount must be multiple of",
}, {
	name: "zero max batch feerate",
	getOrder: func() order.ServerOrder {
		return ask(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  0,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
		})
	},
	expectedErr: "invalid max batch feerate",
}, {
	name: "low max batch feerate",
	getOrder: func() order.ServerOrder {
		return ask(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor - 1,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
		})
	},
	expectedErr: "invalid max batch feerate",
}, {
	name: "invalid duration for order",
	getOrder: func() order.ServerOrder {
		return ask(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    145,
			MinUnitsMatch:    1,
			Version:          orderT.VersionLeaseDurationBuckets,
		})
	},
	expectedErr: "bucket for duration 145 is in state: " +
		"BucketStateNoMarket",
}, {
	name: "invalid version for duration",
	getOrder: func() order.ServerOrder {
		return ask(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    4032,
			MinUnitsMatch:    1,
			Version:          orderT.VersionNodeTierMinMatch,
		})
	},
	expectedErr: "cannot submit order outside of default 2016 duration " +
		"bucket",
}, {
	name: "invalid version for self chan balance",
	getOrder: func() order.ServerOrder {
		o := bid(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
		})
		o.SelfChanBalance = 500_000
		return o
	},
	expectedErr: "cannot use self chan balance with old order version",
}, {
	name: "invalid self chan balance",
	getOrder: func() order.ServerOrder {
		o := bid(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
		})
		o.Version = orderT.VersionSelfChanBalance
		o.SelfChanBalance = 500000
		return o
	},
	expectedErr: "invalid self chan balance: self channel balance " +
		"must be smaller than or equal to capacity",
}, {
	name: "invalid version for sidecar",
	getOrder: func() order.ServerOrder {
		o := bid(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
		})
		o.IsSidecar = true
		return o
	},
	expectedErr: "invalid order version 0 for order with sidecar",
}, {
	name: "invalid min units match for sidecar",
	getOrder: func() order.ServerOrder {
		o := bid(orderT.Kit{
			Amt:              500_000,
			Units:            orderT.NewSupplyFromSats(500_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(500_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
		})
		o.Version = orderT.VersionSidecarChannel
		o.IsSidecar = true
		return o
	},
	expectedErr: "to use self chan balance the min units match " +
		"must be equal to the order amount in units",
}, {
	name: "invalid version for channel type",
	getOrder: func() order.ServerOrder {
		return bid(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
			ChannelType:      orderT.ChannelTypeScriptEnforced,
		})
	},
	expectedErr: "cannot submit channel type preference",
}, {
	name: "order invalid allowed/not allowed ids fields set",
	getOrder: func() order.ServerOrder {
		return ask(orderT.Kit{
			Version:           orderT.VersionChannelType,
			Amt:               100_000,
			Units:             orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled:  orderT.NewSupplyFromSats(100_000),
			AcctKey:           toRawKey(testTraderKey),
			MaxBatchFeeRate:   chainfee.FeePerKwFloor,
			LeaseDuration:     orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:     1,
			ChannelType:       orderT.ChannelTypeScriptEnforced,
			AllowedNodeIDs:    [][33]byte{{1, 2, 3}},
			NotAllowedNodeIDs: [][33]byte{{2, 4, 5}},
		})
	},
	expectedErr: "allowed and not allowed node ids cannot be set together",
}, {
	name: "order invalid ask must have advertised node " +
		"addresses",
	getOrder: func() order.ServerOrder {
		o := ask(orderT.Kit{
			Version:          orderT.VersionChannelType,
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
			ChannelType:      orderT.ChannelTypeScriptEnforced,
		})
		o.ServerDetails().NodeAddrs = nil
		return o
	},
	expectedErr: "ask orders must have advertised node addresses",
}, {
	name: "validate ask happy path",
	getOrder: func() order.ServerOrder {
		return ask(orderT.Kit{
			Version:          orderT.VersionChannelType,
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
			ChannelType:      orderT.ChannelTypeScriptEnforced,
		})
	},
}, {
	name: "validate bid happy path",
	getOrder: func() order.ServerOrder {
		return bid(orderT.Kit{
			Amt:              100_000,
			Units:            orderT.NewSupplyFromSats(100_000),
			UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
			AcctKey:          toRawKey(testTraderKey),
			MaxBatchFeeRate:  chainfee.FeePerKwFloor,
			LeaseDuration:    orderT.LegacyLeaseDurationBucket,
			MinUnitsMatch:    1,
		})
	},
}}

func TestValidateOrder(t *testing.T) {
	for _, tc := range validateOrderTestCases {
		tc := tc

		signer := &mockSigner{}
		signer.shouldVerify = !tc.failVerification

		durations := order.NewDurationBuckets()
		durations.PutMarket(
			orderT.LegacyLeaseDurationBucket,
			order.BucketStateAcceptingOrders,
		)
		durations.PutMarket(145, order.BucketStateNoMarket)
		durations.PutMarket(4032, order.BucketStateClearingMarket)

		book := order.NewBook(&order.BookConfig{
			Signer:          signer,
			DurationBuckets: durations,
		})

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctxb := context.Background()
			err := book.ValidateOrder(ctxb, tc.getOrder())
			if tc.expectedErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

var submitOrderTestCases = []struct {
	name        string
	acct        *account.Account
	order       order.ServerOrder
	mockSetter  func(*subastadb.MockStore)
	expectedErr string
}{{
	name: "account balance insufficient",
	acct: &testAccount,
	order: ask(orderT.Kit{
		Amt:              500_000,
		Units:            orderT.NewSupplyFromSats(500_000),
		UnitsUnfulfilled: orderT.NewSupplyFromSats(500_000),
		AcctKey:          toRawKey(testTraderKey),
		MaxBatchFeeRate:  chainfee.FeePerKwFloor,
		LeaseDuration:    orderT.LegacyLeaseDurationBucket,
		MinUnitsMatch:    1,
	}),
	mockSetter: func(s *subastadb.MockStore) {
		s.EXPECT().
			GetOrders(gomock.Any()).
			Return(nil, nil)
	},
	expectedErr: order.ErrInvalidAmt.Error(),
}, {
	name: "maker cannot pay fees",
	acct: &testAccount,
	order: ask(orderT.Kit{
		Amt:              200_000,
		Units:            orderT.NewSupplyFromSats(200_000),
		UnitsUnfulfilled: orderT.NewSupplyFromSats(200_000),
		AcctKey:          toRawKey(testTraderKey),
		MaxBatchFeeRate:  chainfee.FeePerKwFloor,
		LeaseDuration:    orderT.LegacyLeaseDurationBucket,
		MinUnitsMatch:    1,
	}),
	mockSetter: func(s *subastadb.MockStore) {
		s.EXPECT().
			GetOrders(gomock.Any()).
			Return(nil, nil)
	},
	expectedErr: order.ErrInvalidAmt.Error(),
}, {
	name: "taker cannot pay fees",
	acct: &testAccount,
	order: bid(orderT.Kit{
		Amt:              2_000_000,
		Units:            orderT.NewSupplyFromSats(2_000_000),
		UnitsUnfulfilled: orderT.NewSupplyFromSats(2_000_000),
		FixedRate:        100_000,
		AcctKey:          toRawKey(testTraderKey),
		MaxBatchFeeRate:  chainfee.FeePerKwFloor,
		LeaseDuration:    orderT.LegacyLeaseDurationBucket,
		MinUnitsMatch:    1,
	}),
	mockSetter: func(s *subastadb.MockStore) {
		s.EXPECT().
			GetOrders(gomock.Any()).
			Return(nil, nil)
	},
	expectedErr: order.ErrInvalidAmt.Error(),
}, {
	name: "sumbit ask happy path",
	acct: &testAccount,
	order: ask(orderT.Kit{
		Version:          orderT.VersionChannelType,
		Amt:              100_000,
		Units:            orderT.NewSupplyFromSats(100_000),
		UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
		AcctKey:          toRawKey(testTraderKey),
		MaxBatchFeeRate:  chainfee.FeePerKwFloor,
		LeaseDuration:    orderT.LegacyLeaseDurationBucket,
		MinUnitsMatch:    1,
		ChannelType:      orderT.ChannelTypeScriptEnforced,
	}),
	mockSetter: func(s *subastadb.MockStore) {
		s.EXPECT().
			GetOrders(gomock.Any()).
			Return(nil, nil)
		s.EXPECT().
			SubmitOrder(gomock.Any(), ask(orderT.Kit{
				Version:          orderT.VersionChannelType,
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
				ChannelType:      orderT.ChannelTypeScriptEnforced,
			})).
			Return(nil)
	},
}}

func TestSubmitOrder(t *testing.T) {
	for _, tc := range submitOrderTestCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockCtrl := gomock.NewController(t)
			defer mockCtrl.Finish()

			store := subastadb.NewMockStore(mockCtrl)
			tc.mockSetter(store)

			book := order.NewBook(&order.BookConfig{
				Store: store,
			})

			err := book.Start()
			require.NoError(t, err)
			defer book.Stop()

			ctxb := context.Background()
			feeSchedule := terms.NewLinearFeeSchedule(1, 100)

			err = book.SubmitOrder(
				ctxb, tc.acct, tc.order, feeSchedule,
			)
			if tc.expectedErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TODO(positiveblue): add tests for the RPC serializer. Maybe fuzzing?

// TestCancelOrder ensures that we can cancel an order through its preimage.
func TestCancelOrderWithPreimage(t *testing.T) {
	t.Parallel()

	store := subastadb.NewStoreMock(t)
	store.Accs[testAccount.TraderKeyRaw] = &testAccount

	signer := &mockSigner{shouldVerify: true}

	banManager := ban.NewManager(
		&ban.ManagerConfig{
			Store: ban.NewStoreMock(),
		},
	)

	durations := order.NewDurationBuckets()
	durations.PutMarket(1024, order.BucketStateAcceptingOrders)

	book := order.NewBook(&order.BookConfig{
		BanManager:      banManager,
		Store:           store,
		Signer:          signer,
		DurationBuckets: durations,
	})
	require.NoError(t, book.Start())
	defer book.Stop()

	// Create a test order we'll attempt to cancel after submission.
	preimage := lntypes.Preimage{1}
	kit := orderT.NewKitWithPreimage(preimage)
	kit.AcctKey = testAccount.TraderKeyRaw
	kit.LeaseDuration = 1024
	kit.Amt = 100_000
	kit.Units = orderT.NewSupplyFromSats(kit.Amt)
	kit.UnitsUnfulfilled = orderT.NewSupplyFromSats(kit.Amt)
	kit.MinUnitsMatch = 1
	kit.MaxBatchFeeRate = chainfee.FeePerKwFloor

	ctx := context.Background()
	o := &order.Ask{
		Ask: orderT.Ask{
			Kit: *kit,
		},
		Kit: order.Kit{
			NodeAddrs: testNodeAddrs,
		},
	}
	feeSchedule := terms.NewLinearFeeSchedule(1, 100)
	require.NoError(t, book.SubmitOrder(ctx, &testAccount, o, feeSchedule))

	storedOrder, err := store.GetOrder(ctx, kit.Nonce())
	require.NoError(t, err)
	require.Equal(t, storedOrder.Details().State, orderT.StateSubmitted)

	// Using an invalid preimage should fail.
	invalidPreimage := lntypes.Preimage{1, 1}
	require.Error(t, book.CancelOrderWithPreimage(ctx, invalidPreimage))

	// After canceling the order through its preimage, its state should be
	// updated properly.
	require.NoError(t, book.CancelOrderWithPreimage(ctx, preimage))
	storedOrder, err = store.GetOrder(ctx, kit.Nonce())
	require.NoError(t, err)
	require.Equal(t, storedOrder.Details().State, orderT.StateCanceled)
}

func toRawKey(pubkey *btcec.PublicKey) [33]byte {
	var result [33]byte
	copy(result[:], pubkey.SerializeCompressed())
	return result
}

func ask(kit orderT.Kit) *order.Ask {
	var nonce orderT.Nonce
	copy(nonce[:], testNonce[:])
	kitWithNonce := orderT.NewKit(nonce)
	kitWithNonce.Version = kit.Version
	kitWithNonce.Amt = kit.Amt
	kitWithNonce.Units = kit.Units
	kitWithNonce.UnitsUnfulfilled = kit.UnitsUnfulfilled
	kitWithNonce.AcctKey = kit.AcctKey
	kitWithNonce.MaxBatchFeeRate = kit.MaxBatchFeeRate
	kitWithNonce.FixedRate = kit.FixedRate
	kitWithNonce.LeaseDuration = kit.LeaseDuration
	kitWithNonce.MinUnitsMatch = kit.MinUnitsMatch
	kitWithNonce.ChannelType = kit.ChannelType
	kitWithNonce.AllowedNodeIDs = kit.AllowedNodeIDs
	kitWithNonce.NotAllowedNodeIDs = kit.NotAllowedNodeIDs

	return &order.Ask{
		Ask: orderT.Ask{
			Kit: *kitWithNonce,
		},
		Kit: order.Kit{
			NodeAddrs: testNodeAddrs,
		},
	}
}

func bid(kit orderT.Kit) *order.Bid {
	var nonce orderT.Nonce
	copy(nonce[:], testNonce[:])
	kitWithNonce := orderT.NewKit(nonce)
	kitWithNonce.Version = kit.Version
	kitWithNonce.Amt = kit.Amt
	kitWithNonce.Units = kit.Units
	kitWithNonce.UnitsUnfulfilled = kit.UnitsUnfulfilled
	kitWithNonce.AcctKey = kit.AcctKey
	kitWithNonce.FixedRate = kit.FixedRate
	kitWithNonce.MaxBatchFeeRate = kit.MaxBatchFeeRate
	kitWithNonce.LeaseDuration = kit.LeaseDuration
	kitWithNonce.MinUnitsMatch = kit.MinUnitsMatch
	kitWithNonce.ChannelType = kit.ChannelType
	kitWithNonce.AllowedNodeIDs = kit.AllowedNodeIDs
	kitWithNonce.NotAllowedNodeIDs = kit.NotAllowedNodeIDs

	return &order.Bid{
		Bid: orderT.Bid{
			Kit: *kitWithNonce,
		},
	}
}
