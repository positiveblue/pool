package metrics

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"

	"github.com/lightninglabs/aperture/lsat"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/poolscript"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/stretchr/testify/require"
)

var (
	testRawTraderKey, _ = hex.DecodeString(
		"036b51e0cc2d9e5988ee4967e0ba67ef3727bb633fea21a0af58e0c93954" +
			"46ba09",
	)
	testTraderKey, _        = btcec.ParsePubKey(testRawTraderKey)
	testRawAuctioneerKey, _ = hex.DecodeString("02187d1a0e30f4e5016fc1137363ee9e7ed5dde1e6c50f367422336df7a108b716")
	testAuctioneerKey, _    = btcec.ParsePubKey(testRawAuctioneerKey)

	testNonce = [33]byte{5, 6, 7}

	addr, _       = net.ResolveTCPAddr("tcp", "127.0.0.1:9735")
	testNodeAddrs = []net.Addr{addr}
)

// TestManagerWithEmptyOrdersAndBatches expects 0 values for the order
// and batch metrics.
func TestManagerWithEmptyOrdersAndBatches(t *testing.T) {
	t.Parallel()

	cfg := &ManagerConfig{
		Batches:     nil,
		Orders:      nil,
		LastUpdated: time.Now(),
		RefreshRate: time.Second,
		TimeDurationBuckets: []time.Duration{
			time.Duration(86400) * time.Second,
		},
	}

	metricsManager, _ := NewManager(cfg)

	initialBatches := metricsManager.GetBatches()
	if initialBatches != nil {
		t.Fatalf("batches should not be empty")
	}

	initialOrders := metricsManager.GetOrders()
	if initialOrders != nil {
		t.Fatalf("batches should not be empty")
	}

	expectedOrderMetrics := OrderMetric{
		NumAsks:   int64(0),
		NumBids:   int64(0),
		AskVolume: int64(0),
		BidVolume: int64(0),
	}

	emptyOrderMetrics, err := metricsManager.GenerateOrderMetric(nil)
	if err != nil {
		t.Fatalf("generate order metrics should not throw an err")
	}
	require.Equal(t, emptyOrderMetrics.AskVolume, expectedOrderMetrics.AskVolume)
	require.Equal(t, emptyOrderMetrics.BidVolume, expectedOrderMetrics.BidVolume)
	require.Equal(t, emptyOrderMetrics.NumAsks, expectedOrderMetrics.NumAsks)
	require.Equal(t, emptyOrderMetrics.NumBids, expectedOrderMetrics.NumBids)
}

// TestMetricsManagerWithOrders expects updated NumBids, NumAsks
// AskVolume, and BidVolume.
func TestMetricsManagerWithOrders(t *testing.T) {
	t.Parallel()

	cfg := &ManagerConfig{
		Batches:     nil,
		Orders:      nil,
		LastUpdated: time.Now(),
		RefreshRate: time.Second,
		TimeDurationBuckets: []time.Duration{
			86400 * time.Second,
		},
	}

	metricsManager, _ := NewManager(cfg)

	getAsk :=
		func() order.ServerOrder {
			return ask(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				MinUnitsMatch:    1,
			})
		}
	askOrder := getAsk()
	orders := make([]*order.ServerOrder, 1)
	orders[0] = &askOrder

	expectedMetric := &OrderMetric{
		NumBids:   0,
		NumAsks:   1,
		BidVolume: 0,
		AskVolume: 100000,
	}

	orderMetrics, err := metricsManager.GenerateOrderMetric(orders)
	require.NoError(t, err)
	require.Equal(t, orderMetrics.AskVolume, expectedMetric.AskVolume)
	require.Equal(t, orderMetrics.BidVolume, expectedMetric.BidVolume)
	require.Equal(t, orderMetrics.NumAsks, expectedMetric.NumAsks)
	require.Equal(t, orderMetrics.NumBids, expectedMetric.NumBids)

	getBid :=
		func() order.ServerOrder {
			return bid(orderT.Kit{
				Amt:              100_000,
				Units:            orderT.NewSupplyFromSats(100_000),
				UnitsUnfulfilled: orderT.NewSupplyFromSats(100_000),
				AcctKey:          toRawKey(testTraderKey),
				MaxBatchFeeRate:  chainfee.FeePerKwFloor,
				MinUnitsMatch:    1,
			})
		}
	bidOrder := getBid()
	orders = make([]*order.ServerOrder, 2)
	orders[0] = &bidOrder
	orders[1] = &askOrder

	expectedMetric = &OrderMetric{
		NumBids:   1,
		NumAsks:   1,
		BidVolume: 100000,
		AskVolume: 100000,
	}
	orderMetrics, err = metricsManager.GenerateOrderMetric(orders)
	require.NoError(t, err)
	require.Equal(t, orderMetrics.AskVolume, expectedMetric.AskVolume)
	require.Equal(t, orderMetrics.BidVolume, expectedMetric.BidVolume)
	require.Equal(t, orderMetrics.NumAsks, expectedMetric.NumAsks)
	require.Equal(t, orderMetrics.NumBids, expectedMetric.NumBids)
}

// TestManagerWithBatches expects values for MedianAPR, MedianOrderSize,
// TotalSatsLeased, and TotalFeesAccrued.
func TestMetricsManagerBatches(t *testing.T) {
	t.Parallel()

	cfg := &ManagerConfig{
		Batches:     nil,
		Orders:      nil,
		LastUpdated: time.Now(),
		RefreshRate: time.Second,
		TimeDurationBuckets: []time.Duration{
			86400 * time.Second,
		},
	}

	metricsManager, _ := NewManager(cfg)

	matchedOrders := makeTestOrderBatches(t)

	expectedMetric := &BatchMetric{
		MedianOrderSize:  8,
		MedianAPR:        .2628,
		TotalFeesAccrued: 10009,
		TotalSatsLeased:  24,
	}
	batchMetrics, err := metricsManager.GenerateBatchMetrics(matchedOrders)
	if err != nil {
		t.Fatalf("generate batch metrics should not throw an err")
	}
	require.Equal(t, expectedMetric.TotalSatsLeased, batchMetrics.TotalSatsLeased)
	require.Equal(t, expectedMetric.TotalFeesAccrued, batchMetrics.TotalFeesAccrued)
	require.Equal(t, expectedMetric.MedianOrderSize, batchMetrics.MedianOrderSize)
	require.Equal(t, expectedMetric.MedianAPR, batchMetrics.MedianAPR)
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

func makeTestOrderBatches(t *testing.T) []*matching.MatchedOrder {
	// Create an order batch that contains dummy data.
	askLegacyClientKit := dummyClientOrder(
		t, orderT.LegacyLeaseDurationBucket,
	)
	bidLegacyClientKit := dummyClientOrder(
		t, orderT.LegacyLeaseDurationBucket,
	)
	// _, batchKeyV0Raw := btcec.PrivKeyFromBytes([]byte{0x01})
	// batchKeyV0 := orderT.NewBatchID(batchKeyV0Raw)
	// _, batchKeyV1Raw := btcec.PrivKeyFromBytes([]byte{0x02})
	// batchKeyV1 := orderT.NewBatchID(batchKeyV1Raw)
	askNewClientKit := dummyClientOrder(t, 12345)
	bidNewClientKit := dummyClientOrder(t, 12345)
	serverKit := dummyOrder(t)
	trader1 := matching.Trader{
		AccountKey: matching.AccountID{
			1, 2, 3, 4, 5,
		},
		BatchKey: toRawKey(testAuctioneerKey),
		NextBatchKey: toRawKey(
			poolscript.IncrementKey(testAuctioneerKey),
		),
		VenueSecret:   [32]byte{88, 99},
		AccountExpiry: 10,
		AccountOutPoint: wire.OutPoint{
			Hash: chainhash.Hash{
				11, 12, 13, 14, 15,
			},
			Index: 16,
		},
		AccountBalance: 17,
	}
	trader2 := matching.Trader{
		AccountKey: matching.AccountID{
			2, 3, 4, 5, 6,
		},
		BatchKey: toRawKey(testTraderKey),
		NextBatchKey: toRawKey(
			poolscript.IncrementKey(testTraderKey),
		),
		VenueSecret:   [32]byte{99, 10},
		AccountExpiry: 11,
		AccountOutPoint: wire.OutPoint{
			Hash: chainhash.Hash{
				12, 13, 14, 15, 16,
			},
			Index: 17,
		},
		AccountBalance: 18,
	}
	askLegacyClientKit.AcctKey = trader1.AccountKey
	bidLegacyClientKit.AcctKey = trader2.AccountKey
	askNewClientKit.AcctKey = trader1.AccountKey
	bidNewClientKit.AcctKey = trader2.AccountKey

	allClientOrders := []*orderT.Kit{
		askLegacyClientKit, bidLegacyClientKit,
		askNewClientKit, bidNewClientKit,
	}
	for _, o := range allClientOrders {
		o.Preimage = lntypes.Preimage{}
		o.MultiSigKeyLocator = keychain.KeyLocator{}
	}
	legacyOrders := []matching.MatchedOrder{{
		Asker:  trader1,
		Bidder: trader2,
		Details: matching.OrderPair{
			Ask: &order.Ask{
				Ask: orderT.Ask{
					Kit: *askLegacyClientKit,
				},
				Kit: *serverKit,
			},
			Bid: &order.Bid{
				Bid: orderT.Bid{
					Kit:         *bidLegacyClientKit,
					MinNodeTier: 10,
				},
				Kit: *serverKit,
			},
			Quote: matching.PriceQuote{
				MatchingRate:     5000,
				TotalSatsCleared: 8,
				UnitsMatched:     7,
				UnitsUnmatched:   6,
				Type:             5,
			},
		},
	}}
	newOrders := []matching.MatchedOrder{{
		Asker:  trader1,
		Bidder: trader2,
		Details: matching.OrderPair{
			Ask: &order.Ask{
				Ask: orderT.Ask{
					Kit: *askNewClientKit,
				},
				Kit: *serverKit,
			},
			Bid: &order.Bid{
				Bid: orderT.Bid{
					Kit:         *bidNewClientKit,
					MinNodeTier: 10,
				},
				Kit: *serverKit,
			},
			Quote: matching.PriceQuote{
				MatchingRate:     9,
				TotalSatsCleared: 8,
				UnitsMatched:     7,
				UnitsUnmatched:   6,
				Type:             5,
			},
		},
	}}
	feeReport := matching.TradingFeeReport{
		AccountDiffs: map[matching.AccountID]*matching.AccountDiff{
			trader2.AccountKey: {
				AccountTally: &orderT.AccountTally{
					EndingBalance:          123,
					TotalExecutionFeesPaid: 234,
					TotalTakerFeesPaid:     345,
					TotalMakerFeesAccrued:  456,
					NumChansCreated:        567,
				},
				StartingState:   &trader2,
				RecreatedOutput: nil,
			},
			trader1.AccountKey: {
				AccountTally: &orderT.AccountTally{
					EndingBalance:          99,
					TotalExecutionFeesPaid: 88,
					TotalTakerFeesPaid:     77,
					TotalMakerFeesAccrued:  66,
					NumChansCreated:        55,
				},
				StartingState: &trader1,
				RecreatedOutput: &wire.TxOut{
					Value:    987654,
					PkScript: []byte{77, 88, 99},
				},
			},
		},
		AuctioneerFeesAccrued: 1337,
	}

	batchV0 := matching.NewBatch(
		map[uint32][]matching.MatchedOrder{
			orderT.LegacyLeaseDurationBucket: legacyOrders,
		}, feeReport, map[uint32]orderT.FixedRatePremium{
			orderT.LegacyLeaseDurationBucket: 123,
		},
		orderT.DefaultBatchVersion,
	)

	batchV1 := matching.NewBatch(
		map[uint32][]matching.MatchedOrder{
			orderT.LegacyLeaseDurationBucket: legacyOrders,
			12345:                            newOrders,
		}, feeReport, map[uint32]orderT.FixedRatePremium{
			orderT.LegacyLeaseDurationBucket: 123,
			12345:                            321,
		},
		orderT.DefaultBatchVersion,
	)

	batchSnapshotV0 := matching.BatchSnapshot{
		BatchTx: &wire.MsgTx{
			Version: 2,
			TxOut: []*wire.TxOut{{
				Value:    600_000,
				PkScript: []byte{77, 88, 99},
			}},
		},
		BatchTxFee: 123_456,
		OrderBatch: batchV0,
	}
	batchSnapshotV1 := matching.BatchSnapshot{
		BatchTx: &wire.MsgTx{
			Version: 2,
			TxOut: []*wire.TxOut{{
				Value:    600_000,
				PkScript: []byte{77, 88, 99},
			}},
		},
		BatchTxFee: 123_456,
		OrderBatch: batchV1,
	}
	// batches := make(map[orderT.BatchID]*matching.BatchSnapshot)
	matchedOrders := make([]*matching.MatchedOrder, len(batchSnapshotV0.OrderBatch.Orders)+len(batchSnapshotV1.OrderBatch.Orders))
	for idx := range batchSnapshotV0.OrderBatch.Orders {
		matchedOrders[idx] = &batchSnapshotV0.OrderBatch.Orders[idx]
	}
	for idx := range batchSnapshotV1.OrderBatch.Orders {
		matchedOrders[idx+len(batchSnapshotV0.OrderBatch.Orders)] = &batchSnapshotV1.OrderBatch.Orders[idx]
	}
	return matchedOrders
}

func dummyClientOrder(t *testing.T,
	leaseDuration uint32) *orderT.Kit {

	amt := btcutil.Amount(123_456)
	var testPreimage lntypes.Preimage
	if _, err := rand.Read(testPreimage[:]); err != nil {
		t.Fatalf("could not create private key: %v", err)
	}
	kit := orderT.NewKitWithPreimage(testPreimage)
	kit.State = orderT.StatePartiallyFilled
	kit.FixedRate = 21
	kit.Amt = amt
	kit.Units = orderT.NewSupplyFromSats(amt)
	kit.UnitsUnfulfilled = kit.Units
	kit.MinUnitsMatch = 1
	kit.MultiSigKeyLocator = keychain.KeyLocator{Index: 1, Family: 2}
	kit.MaxBatchFeeRate = chainfee.FeePerKwFloor
	kit.LeaseDuration = leaseDuration
	copy(kit.AcctKey[:], testTraderKey.SerializeCompressed())
	kit.ChannelType = orderT.ChannelTypeScriptEnforced
	return kit
}

func dummyOrder(t *testing.T) *order.Kit {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:9735")
	if err != nil {
		t.Fatalf("could not parse IP addr: %v", err)
	}
	kit := &order.Kit{}
	kit.Sig = lnwire.Sig{99, 99, 99}
	copy(kit.NodeKey[:], randomPubKey(t).SerializeCompressed())
	copy(kit.MultiSigKey[:], randomPubKey(t).SerializeCompressed())
	kit.NodeAddrs = []net.Addr{addr}
	kit.Lsat = lsat.TokenID{9, 8, 7, 6, 5}
	kit.UserAgent = "poold/v0.4.3-alpha/commit=test"
	return kit
}

func randomPubKey(t *testing.T) *btcec.PublicKey {
	var testPriv [32]byte
	if _, err := rand.Read(testPriv[:]); err != nil {
		t.Fatalf("could not create private key: %v", err)
	}

	_, pub := btcec.PrivKeyFromBytes(testPriv[:])
	return pub
}
