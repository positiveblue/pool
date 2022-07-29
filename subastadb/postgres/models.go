// Code generated by sqlc. DO NOT EDIT.
// versions:
//   sqlc v1.15.0

package postgres

import (
	"database/sql"
)

type Account struct {
	TraderKey           []byte
	TokenID             []byte
	Value               int64
	Expiry              int64
	AuctioneerKeyFamily int64
	AuctioneerKeyIndex  int64
	AuctioneerPublicKey []byte
	BatchKey            []byte
	Secret              []byte
	State               int16
	HeightHint          int64
	OutPointHash        []byte
	OutPointIndex       int64
	LatestTx            []byte
	UserAgent           string
	Version             int16
}

type AccountBan struct {
	ID           int64
	Disabled     bool
	TraderKey    []byte
	ExpiryHeight int64
	Duration     int64
}

type AccountDiff struct {
	ID                  int64
	TraderKey           []byte
	Confirmed           bool
	TokenID             []byte
	Value               int64
	Expiry              int64
	AuctioneerKeyFamily int64
	AuctioneerKeyIndex  int64
	AuctioneerPublicKey []byte
	BatchKey            []byte
	Secret              []byte
	State               int16
	HeightHint          int64
	OutPointHash        []byte
	OutPointIndex       int64
	LatestTx            []byte
	UserAgent           string
	Version             int16
}

type AccountReservation struct {
	TraderKey           []byte
	Value               int64
	AuctioneerKeyFamily int64
	AuctioneerKeyIndex  int64
	AuctioneerPublicKey []byte
	InitialBatchKey     []byte
	Expiry              int64
	HeightHint          int64
	TokenID             []byte
	Version             int16
}

type AuctioneerAccount struct {
	Balance             int64
	BatchKey            []byte
	IsPending           bool
	AuctioneerKeyFamily int64
	AuctioneerKeyIndex  int64
	AuctioneerPublicKey []byte
	OutPointHash        []byte
	OutPointIndex       int64
	Version             int16
}

type AuctioneerSnapshot struct {
	BatchKey      []byte
	Balance       int64
	OutPointHash  []byte
	OutPointIndex int64
	Version       int16
}

type Batch struct {
	BatchKey              []byte
	BatchTx               []byte
	BatchTxFee            int64
	Version               int64
	AuctioneerFeesAccrued int64
	Confirmed             bool
	CreatedAt             sql.NullTime
}

type BatchAccountDiff struct {
	BatchKey               []byte
	TraderKey              []byte
	TraderBatchKey         []byte
	TraderNextBatchKey     []byte
	Secret                 []byte
	TotalExecutionFeesPaid int64
	TotalTakerFeesPaid     int64
	TotalMakerFeesAccrued  int64
	NumChansCreated        int64
	StartingBalance        int64
	StartingAccountExpiry  int64
	StartingOutPointHash   []byte
	StartingOutPointIndex  int64
	EndingBalance          int64
	NewAccountExpiry       int64
	TxOutValue             sql.NullInt64
	TxOutPkscript          []byte
}

type BatchClearingPrice struct {
	BatchKey         []byte
	LeaseDuration    int64
	FixedRatePremium int64
}

type BatchMatchedOrder struct {
	BatchKey          []byte
	AskOrderNonce     []byte
	BidOrderNonce     []byte
	LeaseDuration     int64
	MatchingRate      int64
	TotalSatsCleared  int64
	UnitsMatched      int64
	UnitsUnmatched    int64
	FulfillType       int16
	AskUnitsUnmatched int64
	BidUnitsUnmatched int64
	AskState          int16
	BidState          int16
	AskerExpiry       int64
	BidderExpiry      int64
}

type CurrentBatchKey struct {
	ID        int64
	BatchKey  []byte
	UpdatedAt sql.NullTime
}

type LeaseDuration struct {
	Duration int64
	State    int16
}

type LifetimePackage struct {
	ChannelPointString  string
	ChannelPointHash    []byte
	ChannelPointIndex   int64
	ChannelScript       []byte
	HeightHint          int64
	MaturityHeight      int64
	Version             int16
	AskAccountKey       []byte
	BidAccountKey       []byte
	AskNodeKey          []byte
	BidNodeKey          []byte
	AskPaymentBasePoint []byte
	BidPaymentBasePoint []byte
}

type NodeBan struct {
	ID           int64
	Disabled     bool
	NodeKey      []byte
	ExpiryHeight int64
	Duration     int64
}

type NodeRating struct {
	NodeKey  []byte
	NodeTier int64
}

type Order struct {
	Nonce            []byte
	Type             int16
	TraderKey        []byte
	Version          int64
	State            int16
	FixedRate        int64
	Amount           int64
	Units            int64
	UnitsUnfulfilled int64
	MinUnitsMatch    int64
	MaxBatchFeeRate  int64
	LeaseDuration    int64
	ChannelType      int16
	Signature        []byte
	MultisigKey      []byte
	NodeKey          []byte
	TokenID          []byte
	UserAgent        string
	Archived         bool
	CreatedAt        sql.NullTime
	ArchivedAt       sql.NullTime
}

type OrderAllowedNodeID struct {
	Nonce   []byte
	NodeKey []byte
	Allowed bool
}

type OrderAsk struct {
	Nonce                          []byte
	ChannelAnnouncementConstraints int16
	ChannelConfirmationConstraints int16
}

type OrderBid struct {
	Nonce              []byte
	MinNodeTier        int64
	SelfChanBalance    int64
	IsSidecar          bool
	UnannouncedChannel bool
	ZeroConfChannel    bool
}

type OrderNodeNetworkAddress struct {
	Nonce   []byte
	Network string
	Address string
}

type TarderTerm struct {
	TokenID []byte
	BaseFee sql.NullInt64
	FeeRate sql.NullInt64
}
