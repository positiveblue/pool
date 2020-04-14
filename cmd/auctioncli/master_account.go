package main

import (
	"context"

	"github.com/lightninglabs/agora/adminrpc"
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
	Action:      showMasterAccount,
}

func showMasterAccount(ctx *cli.Context) error {
	client, cleanup, err := getClient(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	resp, err := client.MasterAccount(
		context.Background(), &adminrpc.EmptyRequest{},
	)
	if err != nil {
		return err
	}

	printRespJSON(resp)
	return nil
}
