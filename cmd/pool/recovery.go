package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"sync"

	"github.com/btcsuite/btcd/rpcclient"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/urfave/cli"
)

func serverRecovery(ctx *cli.Context) error {
	client, cleanup, err := getClient(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	resp, err := client.RecoverAccounts(
		context.Background(), &poolrpc.RecoverAccountsRequest{},
	)
	if err != nil {
		return err
	}

	printRespJSON(resp)
	return nil
}

// BitcoinConfig defines exported config options for the connection to the
// btcd/bitcoind backend.
type BitcoinConfig struct {
	Host         string `long:"host" description:"bitcoind/btcd instance address"`
	User         string `long:"user" description:"bitcoind/btcd user name"`
	Password     string `long:"password" description:"bitcoind/btcd password"`
	HTTPPostMode bool   `long:"httppostmode" description:"Use HTTP POST mode? bitcoind only supports this mode"`
	UseTLS       bool   `long:"usetls" description:"Use TLS to connect? bitcoind only supports non-TLS connections"`
	TLSPath      string `long:"tlspath" description:"Path to btcd's TLS certificate, if TLS is enabled"`
}

// BitcoinClient is a wrapper around the RPC connection to the chain backend
// and allows transactions to be queried.
type BitcoinClient struct {
	sync.Mutex

	rpcClient *rpcclient.Client
}

// NewClient opens a new RPC connection to the chain backend.
func NewClient(cfg *BitcoinConfig) (*BitcoinClient, error) {
	var err error
	client := &BitcoinClient{}
	client.rpcClient, err = getBitcoinConn(cfg)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func getBitcoinConn(cfg *BitcoinConfig) (*rpcclient.Client, error) {
	// In case we use TLS and a certificate argument is provided, we need to
	// read that file and provide it to the RPC connection as byte slice.
	var rpcCert []byte
	if cfg.UseTLS && cfg.TLSPath != "" {
		certFile, err := os.Open(cfg.TLSPath)
		if err != nil {
			return nil, err
		}
		rpcCert, err = ioutil.ReadAll(certFile)
		if err != nil {
			return nil, err
		}
		if err := certFile.Close(); err != nil {
			return nil, err
		}
	}

	// Connect to the backend with the certs we just loaded.
	connCfg := &rpcclient.ConnConfig{
		Host:         cfg.Host,
		User:         cfg.User,
		Pass:         cfg.Password,
		HTTPPostMode: cfg.HTTPPostMode,
		DisableTLS:   !cfg.UseTLS,
		Certificates: rpcCert,
	}

	// Notice the notification parameter is nil since notifications are
	// not supported in HTTP POST mode.
	return rpcclient.New(connCfg, nil)
}

func clientRecovery(ctx *cli.Context) error {
	cfg := &BitcoinConfig{
		Host:         "localhost:18443",
		User:         "lightning",
		Password:     "lightning",
		HTTPPostMode: true,
		UseTLS:       false,
		TLSPath:      "",
	}
	client, err := NewClient(cfg)
	if err != nil {
		return err
	}

	c, err := client.rpcClient.GetBlockCount()
	fmt.Println(err)
	fmt.Println(c)

	//pkScripts(cltv, yourKey, auctioneerKey, batchCounter)

	for i := int64(100); i < 1000; i++ {

		bHash, err := client.rpcClient.GetBlockHash(i)
		if err != nil {
			//fmt.Println(err)
			continue
		}
		block, err := client.rpcClient.GetBlock(bHash)
		if err != nil {
			fmt.Println(err)
			continue
		}

		for _, tx := range block.Transactions {
			fmt.Printf("TxIn: %v \n", tx.TxIn)
			fmt.Printf("TxOut: %v \n", tx.TxOut)
		}
	}
	return nil
}
