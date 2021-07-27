package order_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/lndclient"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightninglabs/subasta/account"
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
	testAuctioneerKey, _  = btcec.ParsePubKey(testRawAuctioneerKey, btcec.S256())
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
	testTraderKey, _ = btcec.ParsePubKey(testRawTraderKey, btcec.S256())

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
)

type mockSigner struct {
	shouldVerify bool
}

func (s *mockSigner) SignOutputRaw(context.Context, *wire.MsgTx,
	[]*lndclient.SignDescriptor) ([][]byte, error) {

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

func TestBookPrepareOrder(t *testing.T) {
	const bestHeight = 100
	store := subastadb.NewStoreMock(t)
	ctxb := context.Background()
	signer := &mockSigner{}

	store.Accs[testAccount.TraderKeyRaw] = &testAccount
	store.Accs[testAccount2.TraderKeyRaw] = &testAccount2

	feeSchedule := terms.NewLinearFeeSchedule(1, 100)
	durations := order.NewDurationBuckets()

	durations.PutMarket(
		orderT.LegacyLeaseDurationBucket,
		order.BucketStateAcceptingOrders,
	)
	durations.PutMarket(145, order.BucketStateNoMarket)
	durations.PutMarket(4032, order.BucketStateClearingMarket)

	book := order.NewBook(&order.BookConfig{
		Store:           store,
		Signer:          signer,
		DurationBuckets: durations,
	})
	err := book.Start()
	if err != nil {
		t.Fatalf("Could not start order book: %v", err)
	}
	defer book.Stop()

	testCases := []struct {
		name        string
		expectedErr string
		run         func() error
	}{{
		name:        "invalid signature",
		expectedErr: "signature not valid for public key",
		run: func() error {
			signer.shouldVerify = false
			o := ask(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "ask duration 0",
		expectedErr: "cannot submit order outside of default 2016",
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    0,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "ask invalid duration",
		expectedErr: "cannot submit order outside of default 2016",
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    143,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "bid duration 0",
		expectedErr: "cannot submit order outside of default 2016",
		run: func() error {
			o := bid(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    0,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "bid invalid duration",
		expectedErr: "cannot submit order outside of default 2016",
		run: func() error {
			o := bid(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    143,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "zero amount",
		expectedErr: "order amount must be multiple of",
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              0,
				Units:            orderT.NewSupplyFromSats(0),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(0),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "zero max batch feerate",
		expectedErr: "invalid max batch feerate",
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  0,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "low max batch feerate",
		expectedErr: "invalid max batch feerate",
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor - 1,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "account balance insufficient",
		expectedErr: order.ErrInvalidAmt.Error(),
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              500_000,
				Units:            orderT.NewSupplyFromSats(500_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(500_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "invalid duration for order",
		expectedErr: "bucket for duration 145 is in state: BucketStateNoMarket",
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    145,
				MinUnitsMatch:    1,
				Version:          orderT.VersionLeaseDurationBuckets,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "invalid version for duration",
		expectedErr: "cannot submit order outside of default 2016 duration bucket",
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    4032,
				MinUnitsMatch:    1,
				Version:          orderT.VersionNodeTierMinMatch,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "maker cannot pay fees",
		expectedErr: order.ErrInvalidAmt.Error(),
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              200_000,
				Units:            orderT.NewSupplyFromSats(200_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(200_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "taker cannot pay fees",
		expectedErr: order.ErrInvalidAmt.Error(),
		run: func() error {
			o := bid(orderT.Kit{
				Amt:              2_000_000,
				Units:            orderT.NewSupplyFromSats(2_000_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(2_000_000),
				FixedRate:        100_000,
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name: "invalid version for self chan balance",
		expectedErr: "cannot use self chan balance with old order " +
			"version",
		run: func() error {
			o := bid(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
			})
			o.SelfChanBalance = 500000
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name: "invalid self chan balance",
		expectedErr: "invalid self chan balance: self channel balance " +
			"must be smaller than or equal to capacity",
		run: func() error {
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
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "banned account",
		expectedErr: account.NewErrBannedAccount(bestHeight + 144).Error(),
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
			})
			err := store.BanAccount(ctxb, testTraderKey, bestHeight)
			if err != nil {
				return fmt.Errorf("unable to ban account: %v",
					err)
			}

			err = book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)

			// Before returning the error, unban the account to not
			// affect any following tests.
			delete(store.BannedAccs, toRawKey(testTraderKey))

			return err
		},
	}, {
		name:        "invalid version for sidecar",
		expectedErr: "invalid order version 0 for order with sidecar",
		run: func() error {
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
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name: "invalid min units match for sidecar",
		expectedErr: "to use self chan balance the min units match " +
			"must be equal to the order amount in units",
		run: func() error {
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
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "invalid version for channel type",
		expectedErr: "cannot submit channel type preference",
		run: func() error {
			o := bid(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
				ChannelType:      orderT.ChannelTypeScriptEnforced,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}, {
		name:        "successful order submission",
		expectedErr: "",
		run: func() error {
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
			err := book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
			if err != nil {
				return err
			}

			storedOrder, err := store.GetOrder(ctxb, orderT.Nonce{})
			if err != nil {
				return err
			}
			if o != storedOrder {
				return fmt.Errorf("stored order doesn't match")
			}

			return nil
		},
	}, {
		name: "good order but account is expired",
		expectedErr: "account must be open or pending open to submit " +
			"orders, instead state=StateExpired",
		run: func() error {
			o := ask(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testAuctioneerKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				LeaseDuration:    orderT.LegacyLeaseDurationBucket,
				MinUnitsMatch:    1,
			})
			return book.PrepareOrder(ctxb, o, feeSchedule, bestHeight)
		},
	}}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			signer.shouldVerify = true

			err := tc.run()

			// Make sure the error is what we expected.
			if tc.expectedErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErr)
			}
		})
	}
}

// TestCancelOrder ensures that we can cancel an order through its preimage.
func TestCancelOrderWithPreimage(t *testing.T) {
	t.Parallel()

	store := subastadb.NewStoreMock(t)
	store.Accs[testAccount.TraderKeyRaw] = &testAccount

	signer := &mockSigner{shouldVerify: true}

	durations := order.NewDurationBuckets()
	durations.PutMarket(1024, order.BucketStateAcceptingOrders)

	book := order.NewBook(&order.BookConfig{
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
	}
	feeSchedule := terms.NewLinearFeeSchedule(1, 100)
	require.NoError(t, book.PrepareOrder(ctx, o, feeSchedule, 100))

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
	return &order.Ask{
		Ask: orderT.Ask{
			Kit: kit,
		},
	}
}

func bid(kit orderT.Kit) *order.Bid {
	return &order.Bid{
		Bid: orderT.Bid{
			Kit: kit,
		},
	}
}
