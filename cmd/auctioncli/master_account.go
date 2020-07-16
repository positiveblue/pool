package main

import (
	"context"

	"github.com/lightninglabs/protobuf-hex-display/proto"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/urfave/cli"
)

var masterAccountCommands = []cli.Command{
	{
		Name:      "masteraccount",
		ShortName: "m",
		Usage:     "Interact with the master account.",
		Category:  "Master Account",
		Subcommands: []cli.Command{
			showMasterAccountCommand,
		},
	},
}

var showMasterAccountCommand = cli.Command{
	Name:        "show",
	ShortName:   "s",
	Usage:       "show current status of the master account",
	Description: `Show the current status of the auction master account.`,
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.MasterAccount(ctx, &adminrpc.EmptyRequest{})
	}),
}
