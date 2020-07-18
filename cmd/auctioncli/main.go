package main

import (
	"context"
	"fmt"
	"os"

	"github.com/lightninglabs/protobuf-hex-display/jsonpb"
	"github.com/lightninglabs/protobuf-hex-display/proto"
	"github.com/lightninglabs/subasta"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/urfave/cli"
	"google.golang.org/grpc"
)

type simpleCmd func(ctx context.Context, cliCtx *cli.Context,
	client adminrpc.AuctionAdminClient) (proto.Message, error)

func wrapSimpleCmd(exec simpleCmd) func(ctx *cli.Context) error {
	return func(ctx *cli.Context) error {
		client, cleanup, err := getClient(ctx)
		if err != nil {
			return err
		}
		defer cleanup()

		resp, err := exec(context.Background(), ctx, client)
		if err != nil {
			return err
		}

		if _, ok := resp.(*adminrpc.EmptyResponse); !ok {
			printRespJSON(resp)
		}
		return nil
	}
}

func printRespJSON(resp proto.Message) { // nolint
	jsonMarshaler := &jsonpb.Marshaler{
		EmitDefaults: true,
		OrigName:     true,
		Indent:       "\t", // Matches indentation of printJSON.
	}

	jsonStr, err := jsonMarshaler.MarshalToString(resp)
	if err != nil {
		fmt.Println("unable to decode response: ", err)
		return
	}

	fmt.Println(jsonStr)
}

func fatal(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "[auctioncli] %v\n", err)
	os.Exit(1)
}

func main() {
	app := cli.NewApp()

	app.Version = subasta.Version()
	app.Name = "auctioncli"
	app.Usage = "control plane for the auctioneer server"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "rpcserver",
			Value: "localhost:13370",
			Usage: "auctionserver daemon admin address host:port",
		},
	}
	app.Commands = append(app.Commands, masterAccountCommands...)
	app.Commands = append(app.Commands, auctionCommands...)

	err := app.Run(os.Args)
	if err != nil {
		fatal(err)
	}
}

func getClient(ctx *cli.Context) (adminrpc.AuctionAdminClient, func(),
	error) {

	rpcServer := ctx.GlobalString("rpcserver")
	conn, err := getClientConn(rpcServer)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = conn.Close() }

	adminClient := adminrpc.NewAuctionAdminClient(conn)
	return adminClient, cleanup, nil
}

func getClientConn(address string) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{
		grpc.WithInsecure(),
	}

	conn, err := grpc.Dial(address, opts...)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to RPC server: %v",
			err)
	}

	return conn, nil
}
