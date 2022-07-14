//go:build !regtest
// +build !regtest

package main

import "github.com/urfave/cli"

var adminCommands = []cli.Command{{
	Name:      "admin",
	ShortName: "adm",
	Usage:     "Server maintenance and administration.",
	Category:  "Maintenance",
	Subcommands: []cli.Command{
		financialReportCommand,
		shutdownCommand,
		setStatusCommand,
		setLogLevelCommand,
	},
}}

var auctionCommands = []cli.Command{{
	Name:      "auction",
	ShortName: "a",
	Usage:     "Interact with the auction.",
	Category:  "Auction",
	Subcommands: []cli.Command{
		listConnectedTradersCommand,
		listBatchesCommand,
		listOrdersCommand,
		listArchivedOrdersCommand,
		accountDetailsCommand,
		listAccountsCommand,
		editAccountCommand,
		deleteAccountDiffCommand,
		listBansCommand,
		batchTickCommand,
		pauseBatchTickerCommand,
		resumeBatchTickerCommand,
		bumpBatchFeeRateCommand,
		statusCommand,
		batchSnapshotCommand,
		removeBanCommand,
		addBanCommand,
		removeReservationCommand,
		listConflictsCommand,
		clearConflictsCommand,
		modifyNodeRatingsCommand,
		listNodeRatingsCommand,
		storeLeaseDuration,
		removeLeaseDuration,
		listTraderTermsCommand,
		storeTraderTermsCommand,
		removeTraderTermsCommand,
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
	app.Commands = append(app.Commands, adminCommands...)
}
