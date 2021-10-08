package main

import (
	"context"

	"github.com/lightninglabs/protobuf-hex-display/proto"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/urfave/cli"
)

var adminCommands = []cli.Command{
	{
		Name:      "admin",
		ShortName: "adm",
		Usage:     "Server maintenance and administration.",
		Category:  "Maintenance",
		Subcommands: []cli.Command{
			mirrorDatabaseCommand,
		},
	},
}

var mirrorDatabaseCommand = cli.Command{
	Name:        "mirror",
	ShortName:   "m",
	Usage:       "Mirror database to SQL",
	Description: `Mirror database to SQL.`,
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.MirrorDatabase(ctx, &adminrpc.EmptyRequest{})
	}),
}
