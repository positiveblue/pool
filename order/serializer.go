package order

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightninglabs/pool/auctioneerrpc"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/pool/sidecar"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/tor"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// parseOnionAddr parses an onion address specified in host:port format.
func parseOnionAddr(onionAddr string) (net.Addr, error) {
	addrHost, addrPort, err := net.SplitHostPort(onionAddr)
	if err != nil {
		// If the port wasn't specified, then we'll assume the
		// default p2p port.
		addrHost = onionAddr
		addrPort = "9735" // TODO(roasbeef): constant somewhere?
	}

	portNum, err := strconv.Atoi(addrPort)
	if err != nil {
		return nil, err
	}

	return &tor.OnionAddr{
		OnionService: addrHost,
		Port:         portNum,
	}, nil
}

// unmarshalNodeTier maps the RPC node tier enum to the node tier used in
// memory.
func unmarshalNodeTier(nodeTier auctioneerrpc.NodeTier) (orderT.NodeTier,
	error) {

	switch nodeTier {
	// This is the boundary where we enforce our interpretation of the min
	// node tier: clients that specify the default on the RPC layer will be
	// mapped to our current in-memory default.
	case auctioneerrpc.NodeTier_TIER_DEFAULT:
		// TODO(roasbeef): base off order version?
		return orderT.DefaultMinNodeTier, nil

	case auctioneerrpc.NodeTier_TIER_1:
		return orderT.NodeTier1, nil

	case auctioneerrpc.NodeTier_TIER_0:
		return orderT.NodeTier0, nil

	default:
		return 0, fmt.Errorf("unknown node tier: %v", nodeTier)
	}
}

// MarshalAnnouncementConstraints maps the channel announcement constraints
// used in memory to their RPC counterparts.
func MarshalAnnouncementConstraints(
	constraint orderT.ChannelAnnouncementConstraints) (auctioneerrpc.ChannelAnnouncementConstraints,
	error) {

	switch constraint {
	case orderT.AnnouncementNoPreference:
		return auctioneerrpc.ChannelAnnouncementConstraints_ANNOUNCEMENT_NO_PREFERENCE,
			nil

	case orderT.OnlyAnnounced:
		return auctioneerrpc.ChannelAnnouncementConstraints_ONLY_ANNOUNCED,
			nil

	case orderT.OnlyUnannounced:
		return auctioneerrpc.ChannelAnnouncementConstraints_ONLY_UNANNOUNCED,
			nil
	}

	return 0, fmt.Errorf("unable to marshal unknown announcement "+
		"constraint type: %v", constraint)
}

// UnmarshallAnnouncementConstraints maps the RPC announcement constraints enum
// to the announcement constraints used in memory.
func UnmarshallAnnouncementConstraints(
	constraint auctioneerrpc.ChannelAnnouncementConstraints) (orderT.ChannelAnnouncementConstraints,
	error) {

	switch constraint {
	case auctioneerrpc.ChannelAnnouncementConstraints_ANNOUNCEMENT_NO_PREFERENCE:
		return orderT.AnnouncementNoPreference, nil

	case auctioneerrpc.ChannelAnnouncementConstraints_ONLY_ANNOUNCED:
		return orderT.OnlyAnnounced, nil

	case auctioneerrpc.ChannelAnnouncementConstraints_ONLY_UNANNOUNCED:
		return orderT.OnlyUnannounced, nil
	}

	return 0, fmt.Errorf("unknown channel announcement constraint type: "+
		"%v", constraint)
}

// MarshalConfirmationConstraints maps the announcement constraints enum used
// in memory to the RPC counterparts.
func MarshalConfirmationConstraints(
	constraint orderT.ChannelConfirmationConstraints) (auctioneerrpc.ChannelConfirmationConstraints,
	error) {

	switch constraint {
	case orderT.ConfirmationNoPreference:
		return auctioneerrpc.ChannelConfirmationConstraints_CONFIRMATION_NO_PREFERENCE,
			nil

	case orderT.OnlyConfirmed:
		return auctioneerrpc.ChannelConfirmationConstraints_ONLY_CONFIRMED,
			nil

	case orderT.OnlyZeroConf:
		return auctioneerrpc.ChannelConfirmationConstraints_ONLY_ZEROCONF,
			nil
	}

	return 0, fmt.Errorf("unable to marshal unknown announcement "+
		"constraint type: %v", constraint)
}

func UnmarshallConfirmationConstraints(
	constraint auctioneerrpc.ChannelConfirmationConstraints) (orderT.ChannelConfirmationConstraints,
	error) {

	switch constraint {
	case auctioneerrpc.ChannelConfirmationConstraints_CONFIRMATION_NO_PREFERENCE:
		return orderT.ConfirmationNoPreference, nil

	case auctioneerrpc.ChannelConfirmationConstraints_ONLY_CONFIRMED:
		return orderT.OnlyConfirmed, nil

	case auctioneerrpc.ChannelConfirmationConstraints_ONLY_ZEROCONF:
		return orderT.OnlyZeroConf, nil
	}

	return 0, fmt.Errorf("unknown channel announcement constraint type: "+
		"%v", constraint)
}

// parseRPCKits parses the incoming raw RPC order into the go native data
// types used in the order struct.
func parseRPCKits(version uint32,
	details *auctioneerrpc.ServerOrder) (*orderT.Kit, *Kit, error) {

	// Parse the RPC fields into the common client struct.
	clientKit, nodeKey, addrs, multiSigKey, err := parseRPCServerOrder(
		version, details,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse server order: %v",
			err)
	}

	// Parse the rest of the parameters.
	serverKit := &Kit{}
	serverKit.Sig, err = lnwire.NewSigFromRawSignature(details.OrderSig)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse order signature: "+
			"%v", err)
	}
	copy(serverKit.NodeKey[:], nodeKey[:])
	serverKit.NodeAddrs = addrs
	copy(serverKit.MultiSigKey[:], multiSigKey[:])

	return clientKit, serverKit, nil
}

// parseRPCServerOrder parses the incoming raw RPC server order into the go
// native data types used in the order struct.
func parseRPCServerOrder(version uint32,
	details *auctioneerrpc.ServerOrder) (*orderT.Kit, [33]byte, []net.Addr,
	[33]byte, error) {

	var (
		nonce       orderT.Nonce
		nodeKey     [33]byte
		nodeAddrs   = make([]net.Addr, 0, len(details.NodeAddr))
		multiSigKey [33]byte
	)

	copy(nonce[:], details.OrderNonce)
	kit := orderT.NewKit(nonce)
	kit.Version = orderT.Version(version)
	kit.FixedRate = details.RateFixed
	kit.Amt = btcutil.Amount(details.Amt)
	kit.Units = orderT.NewSupplyFromSats(kit.Amt)
	kit.UnitsUnfulfilled = kit.Units
	kit.MaxBatchFeeRate = chainfee.SatPerKWeight(
		details.MaxBatchFeeRateSatPerKw,
	)
	kit.MinUnitsMatch = orderT.NewSupplyFromSats(
		btcutil.Amount(details.MinChanAmt),
	)

	switch details.ChannelType {
	case auctioneerrpc.OrderChannelType_ORDER_CHANNEL_TYPE_UNKNOWN:
		fallthrough

	case auctioneerrpc.OrderChannelType_ORDER_CHANNEL_TYPE_PEER_DEPENDENT:
		kit.ChannelType = orderT.ChannelTypePeerDependent

	case auctioneerrpc.OrderChannelType_ORDER_CHANNEL_TYPE_SCRIPT_ENFORCED:
		kit.ChannelType = orderT.ChannelTypeScriptEnforced

	default:
		return nil, [33]byte{}, nil, [33]byte{},
			fmt.Errorf("unhandled channel type %v",
				details.ChannelType)
	}

	kit.AllowedNodeIDs = make([][33]byte, len(details.AllowedNodeIds))
	for idx, nodeID := range details.AllowedNodeIds {
		if _, err := btcec.ParsePubKey(nodeID); err != nil {
			return nil, [33]byte{}, nil, [33]byte{},
				fmt.Errorf("invalid allowed_node_id: %x",
					nodeID)
		}
		copy(kit.AllowedNodeIDs[idx][:], nodeID)
	}

	kit.NotAllowedNodeIDs = make([][33]byte, len(details.NotAllowedNodeIds))
	for idx, nodeID := range details.NotAllowedNodeIds {
		if _, err := btcec.ParsePubKey(nodeID); err != nil {
			return nil, [33]byte{}, nil, [33]byte{},
				fmt.Errorf("invalid not_allowed_node_id: %x",
					nodeID)
		}
		copy(kit.NotAllowedNodeIDs[idx][:], nodeID)
	}

	copy(kit.AcctKey[:], details.TraderKey)

	nodePubKey, err := btcec.ParsePubKey(details.NodePub)
	if err != nil {
		return nil, nodeKey, nodeAddrs, multiSigKey,
			fmt.Errorf("unable to parse node pub key: %v",
				err)
	}
	copy(nodeKey[:], nodePubKey.SerializeCompressed())

	for _, rpcAddr := range details.NodeAddr {
		// Obtain the host to determine if this is a Tor address.
		host, _, err := net.SplitHostPort(rpcAddr.Addr)
		if err != nil {
			host = rpcAddr.Addr
		}

		var addr net.Addr
		switch {
		case tor.IsOnionHost(host):
			addr, err = parseOnionAddr(rpcAddr.Addr)
			if err != nil {
				return nil, nodeKey, nodeAddrs, multiSigKey,
					fmt.Errorf("unable to parse node "+
						"addr: %v", err)
			}

		default:
			addr, err = net.ResolveTCPAddr(
				rpcAddr.Network, rpcAddr.Addr,
			)
			if err != nil {
				return nil, nodeKey, nodeAddrs, multiSigKey,
					fmt.Errorf("unable to parse node "+
						"addr: %v", err)
			}
		}

		nodeAddrs = append(nodeAddrs, addr)
	}

	multiSigPubkey, err := btcec.ParsePubKey(details.MultiSigKey)
	if err != nil {
		return nil, nodeKey, nodeAddrs, multiSigKey,
			fmt.Errorf("unable to parse multi sig pub key: %v", err)
	}
	copy(multiSigKey[:], multiSigPubkey.SerializeCompressed())

	return kit, nodeKey, nodeAddrs, multiSigKey, nil
}

// ParseRPCOrder parses the incoming raw RPC server order request into the go
// native data types used in the order struct.
// NOTE: The parser does not perform a complete validiation, it only unmarshals
// the data.
func ParseRPCOrder(req *auctioneerrpc.ServerSubmitOrderRequest) (ServerOrder,
	error) {

	var o ServerOrder
	switch requestOrder := req.Details.(type) {
	case *auctioneerrpc.ServerSubmitOrderRequest_Ask:
		a := requestOrder.Ask

		clientKit, serverKit, err := parseRPCKits(a.Version, a.Details)
		if err != nil {
			return nil, err
		}
		clientKit.LeaseDuration = a.LeaseDurationBlocks

		announcement, err := UnmarshallAnnouncementConstraints(
			a.AnnouncementConstraints,
		)
		if err != nil {
			return nil, err
		}

		confirmation, err := UnmarshallConfirmationConstraints(
			a.ConfirmationConstraints,
		)
		if err != nil {
			return nil, err
		}

		o = &Ask{
			Ask: orderT.Ask{
				Kit:                     *clientKit,
				AnnouncementConstraints: announcement,
				ConfirmationConstraints: confirmation,
			},
			Kit: *serverKit,
		}

	case *auctioneerrpc.ServerSubmitOrderRequest_Bid:
		b := requestOrder.Bid
		clientKit, serverKit, err := parseRPCKits(b.Version, b.Details)
		if err != nil {
			return nil, err
		}
		clientKit.LeaseDuration = b.LeaseDurationBlocks

		nodeTier, err := unmarshalNodeTier(b.MinNodeTier)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument,
				err.Error())
		}
		clientBid := &orderT.Bid{
			Kit:                *clientKit,
			MinNodeTier:        nodeTier,
			SelfChanBalance:    btcutil.Amount(b.SelfChanBalance),
			UnannouncedChannel: b.UnannouncedChannel,
			ZeroConfChannel:    b.ZeroConfChannel,
		}

		// The order signature digest includes the IsSidecar flag but
		// it's calculated based on whether the sidecar ticket in the
		// client struct is nil or not. So we need to add an empty
		// ticket if the flag is true, otherwise we'd get a different
		// digest.
		if b.IsSidecarChannel {
			clientBid.SidecarTicket = &sidecar.Ticket{}
		}

		o = &Bid{
			Bid:       *clientBid,
			Kit:       *serverKit,
			IsSidecar: b.IsSidecarChannel,
		}

	default:
		return nil, status.Error(codes.InvalidArgument, "invalid "+
			"order details type")
	}

	// New clients optionally send their user agent string.
	o.ServerDetails().UserAgent = strings.TrimSpace(req.UserAgent)

	return o, nil
}
