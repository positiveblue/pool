package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/btcsuite/btcutil"
	"github.com/gogo/protobuf/jsonpb"
	"github.com/gogo/protobuf/proto"
	"github.com/lightninglabs/agora/client"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/urfave/cli"
	"google.golang.org/grpc"
)

func printRespJSON(resp proto.Message) {
	jsonMarshaler := &jsonpb.Marshaler{
		EmitDefaults: true,
		Indent:       "    ",
	}

	jsonStr, err := jsonMarshaler.MarshalToString(resp)
	if err != nil {
		fmt.Println("unable to decode response: ", err)
		return
	}

	fmt.Println(jsonStr)
}

func fatal(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "[agora] %v\n", err)
	os.Exit(1)
}

func main() {
	app := cli.NewApp()

	app.Version = client.Version()
	app.Name = "agora"
	app.Usage = "control plane for your agorad"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "rpcserver",
			Value: "localhost:12010",
			Usage: "agorad daemon address host:port",
		},
	}
	app.Commands = []cli.Command{
		initAccountCommand,
	}

	err := app.Run(os.Args)
	if err != nil {
		fatal(err)
	}
}

func getClient(ctx *cli.Context) (clmrpc.ChannelAuctioneerClientClient, func(),
	error) {

	rpcServer := ctx.GlobalString("rpcserver")
	conn, err := getClientConn(rpcServer)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { conn.Close() }

	traderClient := clmrpc.NewChannelAuctioneerClientClient(conn)
	return traderClient, cleanup, nil
}

func parseAmt(text string) (btcutil.Amount, error) {
	amtInt64, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amt value")
	}
	return btcutil.Amount(amtInt64), nil
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
