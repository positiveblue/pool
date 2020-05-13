package main

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/urfave/cli"
)

var accountsCommands = []cli.Command{
	{
		Name:      "accounts",
		ShortName: "a",
		Usage:     "Interact with trader accounts.",
		Category:  "Accounts",
		Subcommands: []cli.Command{
			newAccountCommand,
			listAccountsCommand,
			withdrawAccountCommand,
			closeAccountCommand,
		},
	},
}

type Account struct {
	TraderKey        string `json:"trader_key"`
	OutPoint         string `json:"outpoint"`
	Value            uint32 `json:"value"`
	ExpirationHeight uint32 `json:"expiration_height"`
	State            string `json:"state"`
	CloseTxid        string `json:"close_txid"`
}

// NewAccountFromProto creates a display Account from its proto.
func NewAccountFromProto(a *clmrpc.Account) *Account {
	var opHash, closeTxHash chainhash.Hash
	copy(opHash[:], a.Outpoint.Txid)
	copy(closeTxHash[:], a.CloseTxid)

	return &Account{
		TraderKey:        hex.EncodeToString(a.TraderKey),
		OutPoint:         fmt.Sprintf("%v:%d", opHash, a.Outpoint.OutputIndex),
		Value:            a.Value,
		ExpirationHeight: a.ExpirationHeight,
		State:            a.State.String(),
		CloseTxid:        closeTxHash.String(),
	}
}

var newAccountCommand = cli.Command{
	Name:      "new",
	ShortName: "n",
	Usage:     "create an account",
	ArgsUsage: "amt expiry",
	Description: `
		Send the amount in satoshis specified by the amt argument to a
		new account.`,
	Flags: []cli.Flag{
		cli.Uint64Flag{
			Name:  "amt",
			Usage: "the amount in satoshis to create account for",
		},
		cli.Uint64Flag{
			Name: "expiry",
			Usage: "the block height at which this account should " +
				"expire at",
		},
	},
	Action: newAccount,
}

func newAccount(ctx *cli.Context) error {
	cmd := "new"

	amt, err := parseUint64(ctx, 0, "amt", cmd)
	if err != nil {
		return err
	}
	expiry, err := parseUint64(ctx, 1, "expiry", cmd)
	if err != nil {
		return err
	}

	client, cleanup, err := getClient(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	resp, err := client.InitAccount(context.Background(),
		&clmrpc.InitAccountRequest{
			AccountValue:  uint32(amt),
			AccountExpiry: uint32(expiry),
		},
	)
	if err != nil {
		return err
	}

	printJSON(NewAccountFromProto(resp))

	return nil
}

var listAccountsCommand = cli.Command{
	Name:        "list",
	ShortName:   "l",
	Usage:       "list all existing accounts",
	Description: `List all existing accounts.`,
	Action:      listAccounts,
}

func listAccounts(ctx *cli.Context) error {
	client, cleanup, err := getClient(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	resp, err := client.ListAccounts(
		context.Background(), &clmrpc.ListAccountsRequest{},
	)
	if err != nil {
		return err
	}

	var listAccountsResp = struct {
		Accounts []*Account `json:"accounts"`
	}{
		Accounts: make([]*Account, 0, len(resp.Accounts)),
	}
	for _, protoAccount := range resp.Accounts {
		a := NewAccountFromProto(protoAccount)
		listAccountsResp.Accounts = append(listAccountsResp.Accounts, a)
	}

	printJSON(listAccountsResp)

	return nil
}

var withdrawAccountCommand = cli.Command{
	Name:      "withdraw",
	ShortName: "w",
	Usage:     "withdraw funds from an existing account",
	Description: `
	Withdraw funds from an existing account to a supported address.
	`,
	ArgsUsage: "addr sat_per_byte",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name: "trader_key",
			Usage: "the hex-encoded trader key of the account to " +
				"withdraw funds from",
		},
		cli.StringFlag{
			Name:  "addr",
			Usage: "the address the withdrawn funds should go to",
		},
		cli.Uint64Flag{
			Name: "amt",
			Usage: "the amount that will be sent to the address " +
				"and withdrawn from the account",
		},
		cli.Uint64Flag{
			Name: "sat_per_byte",
			Usage: "the fee rate expressed in sat/byte that " +
				"should be used for the withdrawal",
		},
	},
	Action: withdrawAccount,
}

func withdrawAccount(ctx *cli.Context) error {
	cmd := "withdraw"
	traderKey, err := parseHexStr(ctx, 0, "trader_key", cmd)
	if err != nil {
		return err
	}
	addr, err := parseStr(ctx, 1, "addr", cmd)
	if err != nil {
		return err
	}
	amt, err := parseUint64(ctx, 2, "amt", cmd)
	if err != nil {
		return err
	}
	satPerByte, err := parseUint64(ctx, 3, "sat_per_byte", cmd)
	if err != nil {
		return err
	}

	client, cleanup, err := getClient(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	resp, err := client.WithdrawAccount(
		context.Background(), &clmrpc.WithdrawAccountRequest{
			TraderKey: traderKey,
			Outputs: []*clmrpc.Output{
				{
					Value:   uint32(amt),
					Address: addr,
				},
			},
			SatPerByte: uint32(satPerByte),
		},
	)
	if err != nil {
		return err
	}

	var withdrawTxid chainhash.Hash
	copy(withdrawTxid[:], resp.WithdrawTxid)

	var withdrawAccountResp = struct {
		Account      *Account `json:"account"`
		WithdrawTxid string   `json:"withdraw_txid"`
	}{
		Account:      NewAccountFromProto(resp.Account),
		WithdrawTxid: withdrawTxid.String(),
	}

	printJSON(withdrawAccountResp)

	return nil
}

var closeAccountCommand = cli.Command{
	Name:        "close",
	ShortName:   "c",
	Usage:       "close an existing account",
	Description: `Close an existing accounts`,
	ArgsUsage:   "trader_key",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "trader_key",
			Usage: "the trader key associated with the account",
		},
	},
	Action: closeAccount,
}

func closeAccount(ctx *cli.Context) error {
	cmd := "close"
	traderKey, err := parseHexStr(ctx, 0, "trader_key", cmd)
	if err != nil {
		return err
	}

	client, cleanup, err := getClient(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	resp, err := client.CloseAccount(
		context.Background(), &clmrpc.CloseAccountRequest{
			TraderKey: traderKey,
		},
	)
	if err != nil {
		return err
	}

	var closeTxid chainhash.Hash
	copy(closeTxid[:], resp.CloseTxid)

	closeAccountResp := struct {
		CloseTxid string `json:"close_txid"`
	}{
		CloseTxid: closeTxid.String(),
	}

	printJSON(closeAccountResp)

	return nil
}
