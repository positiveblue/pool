//go:build regtest
// +build regtest

package main

import "github.com/urfave/cli"

var auctionCommands = []cli.Command{{
	Name:      "auction",
	ShortName: "a",
	Usage:     "Interact with the auction.",
	Category:  "Auction",
	Subcommands: []cli.Command{
		batchTickCommand,
		pauseBatchTickerCommand,
		resumeBatchTickerCommand,
		statusCommand,
	},
}}

var masterAccountCommands = []cli.Command{{
	Name:      "masteraccount",
	ShortName: "m",
	Usage:     "Interact with the master account.",
	Category:  "Master Account",
	Subcommands: []cli.Command{
		showMasterAccountCommand,
	},
}}

// addCommands adds all available commands to the given CLI app.
func addCommands(app *cli.App) {
	app.Commands = append(app.Commands, masterAccountCommands...)
	app.Commands = append(app.Commands, auctionCommands...)
}
