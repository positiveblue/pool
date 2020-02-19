package itest

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/lightninglabs/agora/client"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/test/bufconn"
)

// traderHarness is a test harness that holds everything that is needed to
// start an instance of the trader server.
type traderHarness struct {
	cfg      *traderConfig
	server   *client.Server
	listener *bufconn.Listener

	clmrpc.TraderClient
}

// traderConfig holds all configuration items that are required to start an
// trader server.
type traderConfig struct {
	AuctioneerConn net.Conn
	BackendCfg     lntest.BackendConfig
	LndNode        *lntest.HarnessNode
	NetParams      *chaincfg.Params
	BaseDir        string
}

// newTraderHarness creates a new trader server harness with the given
// configuration.
func newTraderHarness(cfg traderConfig) (*traderHarness, error) {
	if cfg.BaseDir == "" {
		var err error
		cfg.BaseDir, err = ioutil.TempDir("", "itest-agorad")
		if err != nil {
			return nil, err
		}
	}
	return &traderHarness{
		cfg: &cfg,
	}, nil
}

// start spins up the trader server listening for gRPC connections on a bufconn.
func (hs *traderHarness) start() error {
	// Create new in-memory listener that we are going to use to communicate
	// with the agorad.
	hs.listener = bufconn.Listen(100)

	if hs.cfg.LndNode == nil || hs.cfg.LndNode.Cfg == nil {
		return fmt.Errorf("lnd node configuration cannot be nil")
	}
	rpcMacaroonDir := filepath.Join(
		hs.cfg.LndNode.Cfg.DataDir, "chain", "bitcoin",
		hs.cfg.NetParams.Name,
	)

	// Redirect output from the nodes to log files.
	cfg := &client.Config{
		LogDir:         ".",
		MaxLogFiles:    99,
		MaxLogFileSize: 999,
		Network:        hs.cfg.NetParams.Name,
		Insecure:       true,
		BaseDir:        hs.cfg.BaseDir,
		DebugLevel:     "debug",
		RPCListener:    hs.listener,
		Lnd: &client.LndConfig{
			Host:        hs.cfg.LndNode.Cfg.RPCAddr(),
			MacaroonDir: rpcMacaroonDir,
			TLSPath:     hs.cfg.LndNode.Cfg.TLSCertPath,
		},
		AuctioneerDialOpts: inMemoryDialOpts(hs.cfg.AuctioneerConn),
	}
	var err error
	hs.server, err = client.NewServer(cfg)
	if err != nil {
		return fmt.Errorf("could not create trader server %v", err)
	}
	err = hs.server.Start()
	if err != nil {
		return fmt.Errorf("could not start trader server %v", err)
	}

	// Since Stop uses the LightningClient to stop the node, if we fail to
	// get a connected client, we have to kill the process.
	netConn, err := hs.listener.Dial()
	if err != nil {
		return fmt.Errorf("could not listen on bufconn: %v", err)
	}
	rpcOpts := inMemoryDialOpts(netConn)
	rpcConn, err := grpc.Dial("", rpcOpts...)
	if err != nil {
		return err
	}
	hs.TraderClient = clmrpc.NewTraderClient(
		rpcConn,
	)
	return nil
}

// stop shuts down the trader server and deletes its temporary data directory.
func (hs *traderHarness) stop() error {
	// Don't return the error immediately if stopping goes wrong, always
	// remove the temp directory.
	err := hs.server.Stop()
	_ = os.RemoveAll(hs.cfg.BaseDir)

	return err
}

// inMemoryDialOpts creates the dial options that are needed to connect over the
// non-TLS in-memory buffer connection to the agora server.
func inMemoryDialOpts(conn net.Conn) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithInsecure(),
		grpc.WithContextDialer(func(ctx context.Context,
			target string) (net.Conn, error) {

			return conn, nil
		}),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.DefaultConfig,
			MinConnectTimeout: 10 * time.Second,
		}),
	}
}
