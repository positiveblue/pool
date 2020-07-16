package main

import (
	"context"
	"encoding/hex"

	"github.com/lightninglabs/llm/clmrpc"
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
			statusCommand,
			batchSnapshotCommand,
			removeBanCommand,
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

		return client.BatchSnapshot(ctx, &clmrpc.BatchSnapshotRequest{
			BatchId: batchID,
		})
	}),
}

var removeBanCommand = cli.Command{
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
			Usage: "the hex encoded trader account key",
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
