package subastadb

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strings"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/aperture/lsat"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/lnwire"
	"gorm.io/gorm/clause"
)

// SQLOrderKit stores the common order fields in SQLAskOrder and SQLBidOrder
// matching the pool order.Kit type.
type SQLOrderKit struct {
	// Nonce is the hash of the preimage and acts as the unique identifier
	// of an order.
	Nonce string `gorm:"primaryKey"`

	// Preimage is the randomly generated preimage to the nonce hash. It is
	// only known to the trader client.
	Preimage string

	// Version is the feature version of this order. Can be used to
	// distinguish between certain feature sets or to signal feature flags.
	Version uint32

	// State is the current state the order is in as it was last seen by the
	// client. The real state is tracked on the auction server, so this can
	// be out of sync if there was no connection for a while.
	State uint8

	// FixedRate is the fixed order rate expressed in parts per million.
	FixedRate uint32

	// Amt is the order amount in satoshis.
	Amt int64

	// Units the total amount of units that the target amount maps to.
	Units uint64

	// UnitsUnfulfilled is the number of units that have not been filled yet
	// and are still available for matching against other orders.
	UnitsUnfulfilled uint64

	// MultiSigKeyLocatorFamily is the key family used to obtain the multi
	// sig key.
	MultiSigKeyLocatorFamily uint32

	// MultiSigKeyLocatorIndex is the key index used to obtain the multi sig
	// key.
	MultiSigKeyLocatorIndex uint32

	// MaxBatchFeeRate is is the maximum fee rate the trader is willing to
	// pay for the batch transaction, in sat/kW.
	MaxBatchFeeRate int64

	// AcctKey is key of the account the order belongs to.
	AcctKey string

	// LeaseDuration identifies how long this order wishes to acquire or
	// lease out capital in the Lightning Network for.
	LeaseDuration uint32

	// MinUnitsMatch signals the minimum number of units that must be
	// matched against an order.
	MinUnitsMatch uint64
}

// SQLOrderServerDetailsKit stores all the common order details matching the
// subasta order.Kit type.
type SQLOrderServerDetailsKit struct {
	// Sig is the order signature over the order nonce, signed with the
	// user's account sub key.
	Sig []byte

	// MultiSigKey is a key of the node creating the order that will be used
	// to craft the channel funding TX's 2-of-2 multi signature output.
	MultiSigKey string

	// NodeKey is the identity public key of the node creating the order.
	NodeKey string

	// NodeAddrs is the list of network addresses of the node creating the
	// order. Stored as a comma separated list.
	NodeAddrs string

	// ChanType is the type of the channel that should be opened.
	ChanType uint8

	// Lsat is the LSAT token that was used to submit the order.
	Lsat string

	// UserAgent is the string that identifies the software running on the
	// user's side that was used to initially submit this order.
	UserAgent string
}

// SQLBidOrder is the SQL model server side ask orders matching
// subasta.ServerOrder where the encapsulated order is a pool order.Ask.
type SQLAskOrder struct {
	SQLOrderKit
	SQLOrderServerDetailsKit
}

// TableName overrides the default table name.
func (SQLAskOrder) TableName() string {
	return "asks"
}

// SQLBidOrder is the SQL model server side bid orders matching
// subasta.ServerOrder where the encapsulated order is a pool order.Bid.
type SQLBidOrder struct {
	SQLOrderKit
	SQLOrderServerDetailsKit

	// MinNodeTier is the minimum node tier that this order should be
	// matched with. Only Asks backed by nodes on this tier or above will
	// be matched with this bid.
	MinNodeTier uint32

	// SelfChanBalance is the initial outbound balance that should be added
	// to the channel resulting from matching this bid by moving additional
	// funds from the taker's account into the channel.
	SelfChanBalance int64

	// IsSidecar denotes whether this bid was submitted as a sidecar order.
	IsSidecar bool
}

// TableName overrides the default table name.
func (SQLBidOrder) TableName() string {
	return "bids"
}

// toClientKit converts an SQLOrderKit to orderT.Kit.
func toClientKit(sqlKit *SQLOrderKit) (*orderT.Kit, error) {
	preimage, err := lntypes.MakePreimageFromStr(sqlKit.Preimage)
	if err != nil {
		return nil, err
	}

	clientKit := orderT.NewKitWithPreimage(preimage)
	if clientKit.Nonce().String() != sqlKit.Nonce {
		return nil, fmt.Errorf("nonce mismatch")
	}

	clientKit.Version = orderT.Version(sqlKit.Version)
	clientKit.State = orderT.State(sqlKit.State)
	clientKit.FixedRate = sqlKit.FixedRate
	clientKit.Amt = btcutil.Amount(sqlKit.Amt)
	clientKit.Units = orderT.SupplyUnit(sqlKit.Units)
	clientKit.UnitsUnfulfilled = orderT.SupplyUnit(sqlKit.UnitsUnfulfilled)
	clientKit.LeaseDuration = sqlKit.LeaseDuration
	clientKit.MinUnitsMatch = orderT.SupplyUnit(sqlKit.MinUnitsMatch)
	clientKit.MultiSigKeyLocator = keychain.KeyLocator{
		Index:  sqlKit.MultiSigKeyLocatorIndex,
		Family: keychain.KeyFamily(sqlKit.MultiSigKeyLocatorFamily),
	}
	clientKit.MaxBatchFeeRate = chainfee.SatPerKWeight(
		sqlKit.MaxBatchFeeRate,
	)

	clientKit.AcctKey, err = keyFromHexString(sqlKit.AcctKey)
	if err != nil {
		return nil, err
	}

	return clientKit, nil
}

// toServerKit converts an SQLOrderServerDetailsKit to order.Kit.
func toServerKit(sqlKit *SQLOrderServerDetailsKit) (*order.Kit, error) {
	sig, err := lnwire.NewSigFromRawSignature(sqlKit.Sig)
	if err != nil {
		return nil, err
	}

	nodeKey, err := keyFromHexString(sqlKit.NodeKey)
	if err != nil {
		return nil, err
	}

	multisigKey, err := keyFromHexString(sqlKit.MultiSigKey)
	if err != nil {
		return nil, err
	}

	addrStrs := strings.Split(sqlKit.NodeAddrs, ",")
	addrs := make([]net.Addr, 0, len(addrStrs))
	for _, addrStr := range addrStrs {
		addr, err := addrFromString(addrStr)
		if err != nil {
			return nil, err
		}

		addrs = append(addrs, addr)
	}

	tokenID, err := lsat.MakeIDFromString(sqlKit.Lsat)
	if err != nil {
		return nil, err
	}

	return &order.Kit{
		Sig:         sig,
		MultiSigKey: multisigKey,
		NodeKey:     nodeKey,
		NodeAddrs:   addrs,
		ChanType:    order.ChanType(sqlKit.ChanType),
		Lsat:        tokenID,
		UserAgent:   sqlKit.UserAgent,
	}, nil
}

// toAsk converts this SQLAskOrder to an order.Ask.
func (s *SQLAskOrder) toAsk() (*order.Ask, error) {
	clientKit, err := toClientKit(&s.SQLOrderKit)
	if err != nil {
		return nil, err
	}

	serverKit, err := toServerKit(&s.SQLOrderServerDetailsKit)
	if err != nil {
		return nil, err
	}

	return &order.Ask{
		Ask: orderT.Ask{
			Kit: *clientKit,
		},
		Kit: *serverKit,
	}, nil
}

// toBid converts this SQLBidOrder to an order.Bid.
func (s *SQLBidOrder) toBid() (*order.Bid, error) {
	clientKit, err := toClientKit(&s.SQLOrderKit)
	if err != nil {
		return nil, err
	}

	serverKit, err := toServerKit(&s.SQLOrderServerDetailsKit)
	if err != nil {
		return nil, err
	}

	return &order.Bid{
		Bid: orderT.Bid{
			Kit:             *clientKit,
			MinNodeTier:     orderT.NodeTier(s.MinNodeTier),
			SelfChanBalance: btcutil.Amount(s.SelfChanBalance),
		},
		Kit:       *serverKit,
		IsSidecar: s.IsSidecar,
	}, nil
}

func serverDetailsKit(kit *order.Kit) (*SQLOrderServerDetailsKit, error) {
	nodeAddrsStr, err := addressesToString(kit.NodeAddrs)
	if err != nil {
		return nil, err
	}
	return &SQLOrderServerDetailsKit{
		Sig:         kit.Sig.ToSignatureBytes(),
		MultiSigKey: hex.EncodeToString(kit.MultiSigKey[:]),
		NodeKey:     hex.EncodeToString(kit.NodeKey[:]),
		NodeAddrs:   nodeAddrsStr,
		ChanType:    uint8(kit.ChanType),
		Lsat:        kit.Lsat.String(),
		UserAgent:   kit.UserAgent,
	}, nil
}

func clientOrderKit(kit *orderT.Kit) SQLOrderKit {
	return SQLOrderKit{
		Nonce:                    kit.Nonce().String(),
		Preimage:                 kit.Preimage.String(),
		Version:                  uint32(kit.Version),
		State:                    uint8(kit.State),
		FixedRate:                kit.FixedRate,
		Amt:                      int64(kit.Amt),
		Units:                    uint64(kit.Units),
		UnitsUnfulfilled:         uint64(kit.UnitsUnfulfilled),
		MultiSigKeyLocatorFamily: uint32(kit.MultiSigKeyLocator.Family),
		MultiSigKeyLocatorIndex:  kit.MultiSigKeyLocator.Index,
		MaxBatchFeeRate:          int64(kit.MaxBatchFeeRate),
		AcctKey:                  hex.EncodeToString(kit.AcctKey[:]),
		LeaseDuration:            kit.LeaseDuration,
		MinUnitsMatch:            uint64(kit.MinUnitsMatch),
	}
}

func (s *SQLTransaction) updateAskOrder(o *order.Ask) error {
	serverDetailsKit, err := serverDetailsKit(&o.Kit)
	if err != nil {
		return err
	}

	askOrder := &SQLAskOrder{
		SQLOrderKit:              clientOrderKit(&o.Ask.Kit),
		SQLOrderServerDetailsKit: *serverDetailsKit,
	}
	return s.tx.Clauses(
		clause.OnConflict{UpdateAll: true},
	).Create(askOrder).Error
}

func (s *SQLTransaction) updateBidOrder(o *order.Bid) error {
	serverDetailsKit, err := serverDetailsKit(&o.Kit)
	if err != nil {
		return err
	}

	bidOrder := &SQLBidOrder{
		SQLOrderKit:              clientOrderKit(&o.Bid.Kit),
		SQLOrderServerDetailsKit: *serverDetailsKit,
		MinNodeTier:              uint32(o.MinNodeTier),
		SelfChanBalance:          int64(o.SelfChanBalance),
		IsSidecar:                o.IsSidecar,
	}
	return s.tx.Clauses(
		clause.OnConflict{UpdateAll: true},
	).Create(bidOrder).Error
}

// UpdateOrderSQL attempts to insert or update a ServerOrder in the parent SQL
// transaction.
func (s *SQLTransaction) UpdateOrder(o order.ServerOrder) error {
	switch t := o.(type) {
	case *order.Ask:
		return s.updateAskOrder(t)

	case *order.Bid:
		return s.updateBidOrder(t)
	}

	return fmt.Errorf("unknown order type: %T", o)
}

// GetAsk will select the ask order corresponding to the passed nonce or nil if
// the ask order doesn't exist.
func (s *SQLTransaction) GetAsk(nonce orderT.Nonce) ( // nolint: interfacer
	*order.Ask, error) {

	var asks []SQLAskOrder

	result := s.tx.Where(
		"nonce  = ?", nonce.String(),
	).Find(&asks)
	if result.Error != nil {
		return nil, result.Error
	}

	if len(asks) == 0 {
		return nil, nil
	}

	return asks[0].toAsk()
}

// GetBid will select the bid order corresponding to the passed nonce or nil if
// the bid order doesn't exist.
func (s *SQLTransaction) GetBid(nonce orderT.Nonce) ( // nolint: interfacer
	*order.Bid, error) {

	var bids []SQLBidOrder

	result := s.tx.Where(
		"nonce  = ?", nonce.String(),
	).Find(&bids)
	if result.Error != nil {
		return nil, result.Error
	}

	if len(bids) == 0 {
		return nil, nil
	}

	return bids[0].toBid()
}

// UpdateOrdersSQL is a helper to insert or update orders into our SQL database.
// It does not return any errors to not block normal processing as we only
// intend to mirror into SQL and our main source of truth is etcd.
func UpdateOrdersSQL(ctx context.Context, store *SQLStore,
	orders ...order.ServerOrder) {

	if store == nil || len(orders) == 0 {
		return
	}

	err := store.Transaction(ctx, func(tx *SQLTransaction) error {
		for _, order := range orders {
			if err := tx.UpdateOrder(order); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		log.Errorf("Unable to store orders to SQL db: %v", err)
	}
}
