package main

import (
	"context"

	"github.com/lightninglabs/protobuf-hex-display/proto"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/urfave/cli"
)

var showMasterAccountCommand = cli.Command{
	Name:        "show",
	ShortName:   "s",
	Usage:       "show current status of the master account",
	Description: `Show the current status of the auction master account.`,
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		resp, err := client.MasterAccount(ctx, &adminrpc.EmptyRequest{})
		if err != nil {
			return nil, err
		}

		// We want the TXID to be in the format block explorers use
		// (byte reversed serialization).
		if resp.Outpoint != nil {
			reverseBytes(resp.Outpoint.Txid)
		}

		return resp, nil
	}),
}

func reverseBytes(s []byte) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
