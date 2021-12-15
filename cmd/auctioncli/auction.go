package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightninglabs/pool/auctioneerrpc"
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

var accountDetailsCommand = cli.Command{
	Name:      "accountdetails",
	ShortName: "ad",
	Usage:     "retrieve the details of an existing account",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name: "account_key",
			Usage: "the identifying key of the account to retrieve " +
				"details of",
		},
		cli.BoolFlag{
			Name:  "include_diff",
			Usage: "retrieve the details of the account's staged diff",
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		acctKey, err := hex.DecodeString(cliCtx.String("account_key"))
		if err != nil {
			return nil, err
		}
		return client.AccountDetails(ctx, &adminrpc.AccountDetailsRequest{
			AccountKey:  acctKey,
			IncludeDiff: cliCtx.Bool("include_diff"),
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

var editAccountCommand = cli.Command{
	Name:      "editaccount",
	ShortName: "ea",
	Usage:     "edit the details of an account",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name: "account_key",
			Usage: "the identifying key of the account to edit " +
				"details of",
		},
		cli.BoolFlag{
			Name: "edit_diff",
			Usage: "whether the account diff should be edited " +
				"instead of the main account state",
		},
		cli.Uint64Flag{
			Name:  "value",
			Usage: "the new value of the account in satoshis",
		},
		cli.Int64Flag{
			Name: "rotate_batch_key",
			Usage: "the number of times to rotate the batch key " +
				"(positive integers increment, negative " +
				"integers decrement)",
		},
		cli.StringFlag{
			Name:  "outpoint",
			Usage: "the new outpoint of the account",
		},
		cli.StringFlag{
			Name:  "latest_tx",
			Usage: "the new latest tx of the account",
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		acctKey, err := hex.DecodeString(cliCtx.String("account_key"))
		if err != nil {
			return nil, err
		}

		var outpoint *adminrpc.OutPoint
		if cliCtx.IsSet("outpoint") {
			parts := strings.Split(cliCtx.String("outpoint"), ":")
			if len(parts) != 2 {
				return nil, errors.New("expected outpoint of " +
					"form hash:index")
			}

			hash, err := chainhash.NewHashFromStr(parts[0])
			if err != nil {
				return nil, err
			}
			index, err := strconv.ParseUint(parts[1], 10, 32)
			if err != nil {
				return nil, err
			}

			outpoint = &adminrpc.OutPoint{
				Txid:        hash[:],
				OutputIndex: uint32(index),
			}
		}

		var latestTx []byte
		if cliCtx.IsSet("latest_tx") {
			latestTx, err = hex.DecodeString(
				cliCtx.String("latest_tx"),
			)
			if err != nil {
				return nil, err
			}
		}

		return client.EditAccount(ctx, &adminrpc.EditAccountRequest{
			AccountKey:     acctKey,
			EditDiff:       cliCtx.Bool("edit_diff"),
			Value:          cliCtx.Uint64("value"),
			RotateBatchKey: int32(cliCtx.Int64("rotate_batch_key")),
			Outpoint:       outpoint,
			LatestTx:       latestTx,
		})
	}),
}

var deleteAccountDiffCommand = cli.Command{
	Name:      "deleteaccountdiff",
	ShortName: "dad",
	Usage:     "delete the staged diff of an account modification",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name: "account_key",
			Usage: "the identifying key of the account to delete " +
				"the staged diff of",
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		acctKey, err := hex.DecodeString(cliCtx.String("account_key"))
		if err != nil {
			return nil, err
		}
		return client.DeleteAccountDiff(ctx, &adminrpc.DeleteAccountDiffRequest{
			AccountKey: acctKey,
		})
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

		return client.BatchSnapshot(ctx, &auctioneerrpc.BatchSnapshotRequest{
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

		default:
			return nil, fmt.Errorf("must specify account or node " +
				"key")
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

		default:
			return nil, fmt.Errorf("must specify account key or " +
				"lsat ID")
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

		if len(nodeKey) != 33 {
			return nil, fmt.Errorf("proper node key must be specified")
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

var addBanCommand = cli.Command{ // nolint:dupl
	Name:      "addban",
	ShortName: "ab",
	Usage:     "add a ban for a trader, either by account key or node key",
	ArgsUsage: "--account_key | --node_key --duration",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "account_key",
			Usage: "the hex encoded trader account key",
		},
		cli.StringFlag{
			Name:  "node_key",
			Usage: "the hex encoded trader node key",
		},
		cli.Uint64Flag{
			Name:  "duration",
			Usage: "the duration in blocks to ban the trader for",
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		if cliCtx.NArg() != 0 || cliCtx.NumFlags() != 2 {
			return nil, cli.ShowCommandHelp(cliCtx, "addban")
		}

		req := &adminrpc.BanRequest{
			Duration: uint32(cliCtx.Uint64("duration")),
		}
		switch {
		case cliCtx.IsSet("account_key"):
			acctKey, err := hex.DecodeString(
				cliCtx.String("account_key"),
			)
			if err != nil {
				return nil, err
			}
			req.Ban = &adminrpc.BanRequest_Account{
				Account: acctKey,
			}

		case cliCtx.IsSet("node_key"):
			nodeKey, err := hex.DecodeString(
				cliCtx.String("node_key"),
			)
			if err != nil {
				return nil, err
			}
			req.Ban = &adminrpc.BanRequest_Node{
				Node: nodeKey,
			}

		default:
			return nil, fmt.Errorf("must specify account or node " +
				"key")
		}

		return client.AddBan(ctx, req)
	}),
}

var storeLeaseDuration = cli.Command{
	Name:      "storeleaseduration",
	ShortName: "sld",
	Usage:     "add or update a lease duration bucket",
	ArgsUsage: "duration status",
	Flags: []cli.Flag{
		cli.Uint64Flag{
			Name:  "duration",
			Usage: "the duration to add or modify",
		},
		cli.StringFlag{
			Name:  "status",
			Usage: "the status the market should be updated to",
			Value: auctioneerrpc.DurationBucketState_MARKET_OPEN.String(),
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		var (
			duration  uint32
			statusStr string
			args      = cliCtx.Args()
		)

		switch {
		case cliCtx.IsSet("duration"):
			duration = uint32(cliCtx.Uint64("duration"))
		case args.Present():
			duration64, err := strconv.ParseUint(
				args.First(), 10, 32,
			)
			if err != nil {
				return nil, err
			}
			duration = uint32(duration64)
			args = args.Tail()
		}

		if duration == 0 {
			return nil, fmt.Errorf("invalid duration")
		}

		switch {
		case cliCtx.IsSet("status"):
			statusStr = cliCtx.String("status")
		case args.Present():
			statusStr = args.First()
		}

		statusUint, ok := auctioneerrpc.DurationBucketState_value[statusStr]
		if !ok {
			validValues := make(
				[]string, len(auctioneerrpc.DurationBucketState_name),
			)
			for i, validValue := range auctioneerrpc.DurationBucketState_name {
				validValues[i] = validValue
			}
			return nil, fmt.Errorf("invalid market status, must "+
				"be one of %v", validValues)
		}

		return client.StoreLeaseDuration(ctx, &adminrpc.LeaseDuration{
			Duration:    duration,
			BucketState: auctioneerrpc.DurationBucketState(statusUint),
		})
	}),
}

var removeLeaseDuration = cli.Command{
	Name:      "removeleaseduration",
	ShortName: "rld",
	Usage:     "remove a lease duration bucket",
	ArgsUsage: "duration",
	Flags: []cli.Flag{
		cli.Uint64Flag{
			Name:  "duration",
			Usage: "the duration to add or modify",
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		var (
			duration uint32
			args     = cliCtx.Args()
		)

		switch {
		case cliCtx.IsSet("duration"):
			duration = uint32(cliCtx.Uint64("duration"))
		case args.Present():
			duration64, err := strconv.ParseUint(
				args.First(), 10, 32,
			)
			if err != nil {
				return nil, err
			}
			duration = uint32(duration64)
			args = args.Tail()
		}

		if duration == 0 {
			return nil, fmt.Errorf("invalid duration")
		}

		return client.RemoveLeaseDuration(ctx, &adminrpc.LeaseDuration{
			Duration: duration,
		})
	}),
}

var listTraderTermsCommand = cli.Command{
	Name:      "listtraderterms",
	ShortName: "ltt",
	Usage:     "list the current set of custom trader terms",
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.ListTraderTerms(ctx, &adminrpc.EmptyRequest{})
	}),
}

var storeTraderTermsCommand = cli.Command{
	Name:      "storetraderterms",
	ShortName: "stt",
	Usage:     "add or update a custom trader terms record",
	ArgsUsage: "lsat-id",
	Flags: []cli.Flag{
		cli.Int64Flag{
			Name: "base_fee",
			Usage: "the base fee in satoshis to charge, -1 means " +
				"use default base fee",
			Value: -1,
		},
		cli.Int64Flag{
			Name: "fee_rate",
			Usage: "the fee rate in parts per million to charge, " +
				"-1 means use default fee rate",
			Value: -1,
		},
	},
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		var (
			traderID []byte
			err      error
			args     = cliCtx.Args()
		)

		if args.Present() {
			traderID, err = hex.DecodeString(args.First())
			if err != nil {
				return nil, fmt.Errorf("error decoding lsat "+
					"ID: %v", err)
			}
		}

		if len(traderID) == 0 {
			return nil, fmt.Errorf("invalid lsat ID")
		}

		return client.StoreTraderTerms(ctx, &adminrpc.TraderTerms{
			LsatId:  traderID,
			BaseFee: cliCtx.Int64("base_fee"),
			FeeRate: cliCtx.Int64("fee_rate"),
		})
	}),
}

var removeTraderTermsCommand = cli.Command{
	Name:      "removetraderterms",
	ShortName: "rtt",
	Usage:     "remove a custom trader terms record",
	ArgsUsage: "lsat-id",
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		var (
			traderID []byte
			err      error
			args     = cliCtx.Args()
		)

		if args.Present() {
			traderID, err = hex.DecodeString(args.First())
			if err != nil {
				return nil, fmt.Errorf("error decoding lsat "+
					"ID: %v", err)
			}
		}

		if len(traderID) == 0 {
			return nil, fmt.Errorf("invalid lsat ID")
		}

		return client.RemoveTraderTerms(ctx, &adminrpc.TraderTerms{
			LsatId: traderID,
		})
	}),
}
