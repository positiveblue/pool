package order_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	orderT "github.com/lightninglabs/llm/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

var (
	testRawAuctioneerKey, _ = hex.DecodeString("02187d1a0e30f4e5016fc1137363ee9e7ed5dde1e6c50f367422336df7a108b716")
	testAuctioneerKey, _    = btcec.ParsePubKey(testRawAuctioneerKey, btcec.S256())
	testAuctioneerKeyDesc   = &keychain.KeyDescriptor{
		KeyLocator: account.LongTermKeyLocator,
		PubKey:     testAuctioneerKey,
	}

	testRawTraderKey, _ = hex.DecodeString("036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0af58e0c9395446ba09")
	testTraderKey, _    = btcec.ParsePubKey(testRawTraderKey, btcec.S256())

	testAccount = account.Account{
		TraderKeyRaw:  toRawKey(testTraderKey),
		Value:         200000,
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
		Value:         200000,
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
	[]*input.SignDescriptor) ([][]byte, error) {

	return [][]byte{{1, 2, 3}}, nil
}

func (s *mockSigner) ComputeInputScript(context.Context, *wire.MsgTx,
	[]*input.SignDescriptor) ([]*input.Script, error) {

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

	book := order.NewBook(&order.BookConfig{
		MaxDuration: 1234,
		SubmitFee:   123,
		Store:       store,
		Signer:      signer,
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
			o := &order.Ask{
				Ask: orderT.Ask{
					Kit: orderT.Kit{
						Amt: 100000,
					},
				},
			}
			return book.PrepareOrder(ctxb, o, bestHeight)
		},
	}, {
		name:        "ask max duration 0",
		expectedErr: "invalid max duration",
		run: func() error {
			o := &order.Ask{
				Ask: orderT.Ask{
					Kit: orderT.Kit{
						Amt: 100000,
					},
					MaxDuration: 0,
				},
			}
			return book.PrepareOrder(ctxb, o, bestHeight)
		},
	}, {
		name:        "ask max duration too large",
		expectedErr: "maximum allowed value for max duration is 1234",
		run: func() error {
			o := &order.Ask{
				Ask: orderT.Ask{
					Kit: orderT.Kit{
						Amt: 100000,
					},
					MaxDuration: 1235,
				},
			}
			return book.PrepareOrder(ctxb, o, bestHeight)
		},
	}, {
		name:        "bid min duration 0",
		expectedErr: "invalid min duration",
		run: func() error {
			o := &order.Bid{
				Bid: orderT.Bid{
					Kit: orderT.Kit{
						Amt: 100000,
					},
					MinDuration: 0,
				},
			}
			return book.PrepareOrder(ctxb, o, bestHeight)
		},
	}, {
		name:        "zero amount",
		expectedErr: "order amount must be multiple of",
		run: func() error {
			o := &order.Ask{
				Ask: orderT.Ask{
					Kit: orderT.Kit{
						Amt:     0,
						AcctKey: toRawKey(testTraderKey),
					},
					MaxDuration: 1024,
				},
			}
			return book.PrepareOrder(ctxb, o, bestHeight)
		},
	}, {
		name:        "bid min duration too large",
		expectedErr: "maximum allowed value for min duration is 1234",
		run: func() error {
			o := &order.Bid{
				Bid: orderT.Bid{
					Kit: orderT.Kit{
						Amt: 100000,
					},
					MinDuration: 1235,
				},
			}
			return book.PrepareOrder(ctxb, o, bestHeight)
		},
	}, {
		name:        "account balance insufficient",
		expectedErr: order.ErrInvalidAmt.Error(),
		run: func() error {
			o := &order.Ask{
				Ask: orderT.Ask{
					Kit: orderT.Kit{
						Amt:     500000,
						AcctKey: toRawKey(testTraderKey),
					},
					MaxDuration: 1024,
				},
			}
			return book.PrepareOrder(ctxb, o, bestHeight)
		},
	}, {
		name:        "banned account",
		expectedErr: account.NewErrBannedAccount(bestHeight + 144).Error(),
		run: func() error {
			o := &order.Ask{
				Ask: orderT.Ask{
					Kit: orderT.Kit{
						Amt:     100000,
						AcctKey: toRawKey(testTraderKey),
					},
					MaxDuration: 1024,
				},
			}
			err := store.BanAccount(ctxb, testTraderKey, bestHeight)
			if err != nil {
				return fmt.Errorf("unable to ban account: %v",
					err)
			}

			err = book.PrepareOrder(ctxb, o, bestHeight)

			// Before returning the error, unban the account to not
			// affect any following tests.
			delete(store.BannedAccs, toRawKey(testTraderKey))

			return err
		},
	}, {
		name:        "successful order submission",
		expectedErr: "",
		run: func() error {
			o := &order.Ask{
				Ask: orderT.Ask{
					Kit: orderT.Kit{
						Amt:     100000,
						AcctKey: toRawKey(testTraderKey),
					},
					MaxDuration: 1024,
				},
			}
			err := book.PrepareOrder(ctxb, o, bestHeight)
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
	},
		{
			name:        "good order but account is expired",
			expectedErr: "account must be open or pending open to submit orders, instead state=StateExpired",
			run: func() error {
				o := &order.Ask{
					Ask: orderT.Ask{
						Kit: orderT.Kit{
							Amt:     100000,
							AcctKey: toRawKey(testAuctioneerKey),
						},
						MaxDuration: 1024,
					},
				}
				return book.PrepareOrder(ctxb, o)
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			signer.shouldVerify = true

			err := tc.run()

			// Make sure the error is what we expected.
			if err == nil && tc.expectedErr != "" {
				t.Fatalf("expected error '%s' but got nil",
					tc.expectedErr)
			}
			if err != nil && tc.expectedErr == "" {
				t.Fatalf("expected nil error but got '%v'", err)
			}
			if err != nil &&
				!strings.Contains(err.Error(), tc.expectedErr) {

				t.Fatalf("expected error '%s' but got '%v'",
					tc.expectedErr, err)
			}
		})
	}
}

func toRawKey(pubkey *btcec.PublicKey) [33]byte {
	var result [33]byte
	copy(result[:], pubkey.SerializeCompressed())
	return result
}
