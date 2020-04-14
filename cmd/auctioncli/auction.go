package main

import (
	"context"

	"github.com/lightninglabs/agora/adminrpc"
	"github.com/urfave/cli"
)

var auctionCommands = []cli.Command{
	{
		Name:      "auction",
		ShortName: "a",
		Usage:     "Interact with the auction.",
		Category:  "Auction",
		Subcommands: []cli.Command{
			listConnectedTradersCommand,
		},
	},
}

var listConnectedTradersCommand = cli.Command{
	Name:        "listtraders",
	ShortName:   "ls",
	Usage:       "list all currently connected traders",
	Description: `List all traders that are currently connected to the auction server.`,
	Action:      listConnectedTraders,
}

func listConnectedTraders(ctx *cli.Context) error {
	client, cleanup, err := getClient(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	resp, err := client.ConnectedTraders(
		context.Background(), &adminrpc.EmptyRequest{},
	)
	if err != nil {
		return err
	}

	printRespJSON(resp)
	return nil
}
