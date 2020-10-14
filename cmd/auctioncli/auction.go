package main

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/protobuf-hex-display/proto"
	"github.com/lightninglabs/subasta/adminrpc"
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
			listBatchesCommand,
			listOrdersCommand,
			listArchivedOrdersCommand,
			listAccountsCommand,
			listBansCommand,
			batchTickCommand,
			pauseBatchTickerCommand,
			resumeBatchTickerCommand,
			bumpBatchFeeRateCommand,
			statusCommand,
			batchSnapshotCommand,
			removeBanCommand,
			removeReservationCommand,
			listConflictsCommand,
			clearConflictsCommand,
			modifyNodeRatingsCommand,
			listNodeRatingsCommand,
		},
	},
}

var listConnectedTradersCommand = cli.Command{
	Name:      "listtraders",
	ShortName: "lt",
	Usage:     "list all currently connected traders",
	Description: `
	List all traders that are currently connected to the auction server.
	`,
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.ConnectedTraders(ctx, &adminrpc.EmptyRequest{})
	}),
}

var listBatchesCommand = cli.Command{
	Name:      "listbatches",
	ShortName: "lb",
	Usage:     "list all executed batches",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.ListBatches(ctx, &adminrpc.EmptyRequest{})
	}),
}

var listOrdersCommand = cli.Command{
	Name:      "listorders",
	ShortName: "lo",
	Usage:     "list all active orders",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.ListOrders(ctx, &adminrpc.ListOrdersRequest{
			Archived: false,
		})
	}),
}

var listArchivedOrdersCommand = cli.Command{
	Name:      "listarchivedorders",
	ShortName: "lao",
	Usage:     "list all archived orders",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.ListOrders(ctx, &adminrpc.ListOrdersRequest{
			Archived: true,
		})
	}),
}

var listAccountsCommand = cli.Command{
	Name:      "listaccounts",
	ShortName: "la",
	Usage:     "list all accounts",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.ListAccounts(ctx, &adminrpc.EmptyRequest{})
	}),
}

var listBansCommand = cli.Command{
	Name:      "listbans",
	ShortName: "lba",
	Usage:     "list all banned accounts and nodes",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.ListBans(ctx, &adminrpc.EmptyRequest{})
	}),
}

var batchTickCommand = cli.Command{
	Name:      "batchtick",
	ShortName: "t",
	Usage:     "manually force a new batch tick event",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.BatchTick(ctx, &adminrpc.EmptyRequest{})
	}),
}

var pauseBatchTickerCommand = cli.Command{
	Name:      "pausebatch",
	ShortName: "p",
	Usage:     "manually pause all batch tick events",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.PauseBatchTicker(ctx, &adminrpc.EmptyRequest{})
	}),
}

var resumeBatchTickerCommand = cli.Command{
	Name:      "resumebatch",
	ShortName: "r",
	Usage:     "manually resume all batch tick events",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.ResumeBatchTicker(ctx, &adminrpc.EmptyRequest{})
	}),
}

var bumpBatchFeeRateCommand = cli.Command{
	Name:      "bumpbatchfeerate",
	ShortName: "bb",
	Usage: "manually set the effective fee rate of the next batch in " +
		"order to bump the fee of any unconfirmed batches. Note that " +
		"this might end up creating an empty batch.",
	Flags: []cli.Flag{
		cli.Uint64Flag{
			Name: "conf_target",
			Usage: "set the fee estimation for next batch to " +
				"target this number",
		},
		cli.Uint64Flag{
			Name: "fee_rate",
			Usage: "set the fee rate for next batch to this " +
				"sat/kw (overrides conf_target)",
		},
	},

	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.BumpBatchFeeRate(
			ctx, &adminrpc.BumpBatchFeeRateRequest{
				ConfTarget:      uint32(cliCtx.Uint64("conf_target")),
				FeeRateSatPerKw: uint32(cliCtx.Uint64("fee_rate")),
			},
		)
	}),
}

var statusCommand = cli.Command{
	Name:      "status",
	ShortName: "s",
	Usage:     "query the current status of the auction",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.AuctionStatus(ctx, &adminrpc.EmptyRequest{})
	}),
}

var batchSnapshotCommand = cli.Command{
	Name:      "snapshot",
	ShortName: "sn",
	Usage:     "query the snapshot of a specific batch",
	ArgsUsage: "batch_id",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name: "batch_id",
			Usage: "the hex encoded batch ID, if left blank, " +
				"information about the latest batch is shown",
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		var (
			batchIDHex string
			args       = cliCtx.Args()
		)
		switch {
		case cliCtx.IsSet("batch_id"):
			batchIDHex = cliCtx.String("batch_id")
		case args.Present():
			batchIDHex = args.First()
		}

		batchID, err := hex.DecodeString(batchIDHex)
		if err != nil {
			return nil, err
		}

		return client.BatchSnapshot(ctx, &poolrpc.BatchSnapshotRequest{
			BatchId: batchID,
		})
	}),
}

var removeBanCommand = cli.Command{ // nolint:dupl
	Name:      "removeban",
	ShortName: "rm",
	Usage:     "remove a banned trader, either by account key or node key",
	ArgsUsage: "--account_key | --node_key",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "account_key",
			Usage: "the hex encoded trader account key",
		},
		cli.StringFlag{
			Name:  "node_key",
			Usage: "the hex encoded trader node key",
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		if cliCtx.NArg() != 0 || cliCtx.NumFlags() != 1 {
			return nil, cli.ShowCommandHelp(cliCtx, "removeban")
		}

		req := &adminrpc.RemoveBanRequest{}
		switch {
		case cliCtx.IsSet("account_key"):
			acctKey, err := hex.DecodeString(
				cliCtx.String("account_key"),
			)
			if err != nil {
				return nil, err
			}
			req.Ban = &adminrpc.RemoveBanRequest_Account{
				Account: acctKey,
			}

		case cliCtx.IsSet("node_key"):
			nodeKey, err := hex.DecodeString(
				cliCtx.String("node_key"),
			)
			if err != nil {
				return nil, err
			}
			req.Ban = &adminrpc.RemoveBanRequest_Node{
				Node: nodeKey,
			}
		}

		return client.RemoveBan(ctx, req)
	}),
}

var removeReservationCommand = cli.Command{ // nolint:dupl
	Name:      "removereservation",
	ShortName: "rr",
	Usage:     "remove a reserved account, either by account key or LSAT ID",
	ArgsUsage: "--account_key | --lsat_id",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "account_key",
			Usage: "the hex encoded trader account key",
		},
		cli.StringFlag{
			Name:  "lsat_id",
			Usage: "the hex encoded trader LSAT ID",
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		if cliCtx.NArg() != 0 || cliCtx.NumFlags() != 1 {
			return nil, cli.ShowCommandHelp(
				cliCtx, "removereservation",
			)
		}

		req := &adminrpc.RemoveReservationRequest{}
		switch {
		case cliCtx.IsSet("account_key"):
			acctKey, err := hex.DecodeString(
				cliCtx.String("account_key"),
			)
			if err != nil {
				return nil, err
			}
			req.Reservation = &adminrpc.RemoveReservationRequest_TraderKey{
				TraderKey: acctKey,
			}

		case cliCtx.IsSet("lsat_id"):
			lsatID, err := hex.DecodeString(
				cliCtx.String("lsat_id"),
			)
			if err != nil {
				return nil, err
			}
			req.Reservation = &adminrpc.RemoveReservationRequest_Lsat{
				Lsat: lsatID,
			}
		}

		return client.RemoveReservation(ctx, req)
	}),
}

var listConflictsCommand = cli.Command{
	Name:      "listconflicts",
	ShortName: "lc",
	Usage: "list all funding conflicts that are currently being " +
		"tracked",
	Description: `
	The auctioneer tracks all funding conflicts (problems when opening
	channels) between nodes and tries to not match nodes with conflicts
	again. This command lists all currently recorded conflicts.
	`,
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.FundingConflicts(ctx, &adminrpc.EmptyRequest{})
	}),
}

var clearConflictsCommand = cli.Command{
	Name:      "clearconflicts",
	ShortName: "cc",
	Usage:     "manually clear all recorded conflicts",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.ClearConflicts(ctx, &adminrpc.EmptyRequest{})
	}),
}

var modifyNodeRatingsCommand = cli.Command{
	Name:      "modifyrating",
	ShortName: "mr",
	Usage:     "manually modify an existing node rating",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "node_key",
			Usage: "the node key to modify the rating for",
		},
		cli.Uint64Flag{
			Name:  "new_rating",
			Usage: "the new rating to set: 1 is t0, 2 is t1",
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		nodeRating := cliCtx.Uint64("new_rating")
		if nodeRating == 0 {
			return nil, fmt.Errorf("rating must be 1 or 2 (t1)")
		}

		nodeKey, err := hex.DecodeString(cliCtx.String("node_key"))
		if err != nil {
			return nil, fmt.Errorf("unable to parse node "+
				"key: %v", err)
		}

		return client.ModifyNodeRatings(ctx, &adminrpc.ModifyRatingRequest{
			NodeKey:     nodeKey,
			NewNodeTier: uint32(nodeRating),
		})
	}),
}

var listNodeRatingsCommand = cli.Command{
	Name:      "listnoderatings",
	ShortName: "lnr",
	Usage:     "list the current set of node ratings",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.ListNodeRatings(ctx, &adminrpc.EmptyRequest{})
	}),
}
