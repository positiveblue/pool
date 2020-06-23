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
	"github.com/lightninglabs/llm/clmrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// traderHarness is a test harness that holds everything that is needed to
// start an instance of the trader server.
type traderHarness struct {
	cfg       *traderConfig
	server    *client.Server
	clientCfg *client.Config
	listener  *bufconn.Listener

	clmrpc.TraderClient
}

// traderConfig holds all configuration items that are required to start an
// trader server.
type traderConfig struct {
	AuctioneerConn net.Conn
	BackendCfg     lntest.BackendConfig
	ServerTLSPath  string
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

	// Create new in-memory listener that we are going to use to communicate
	// with the agorad.
	listener := bufconn.Listen(100)

	if cfg.LndNode == nil || cfg.LndNode.Cfg == nil {
		return nil, fmt.Errorf("lnd node configuration cannot be nil")
	}
	rpcMacaroonDir := filepath.Join(
		cfg.LndNode.Cfg.DataDir, "chain", "bitcoin", cfg.NetParams.Name,
	)

	return &traderHarness{
		cfg:      &cfg,
		listener: listener,
		clientCfg: &client.Config{
			LogDir:         ".",
			MaxLogFiles:    99,
			MaxLogFileSize: 999,
			Network:        cfg.NetParams.Name,
			Insecure:       true,
			FakeAuth:       true,
			BaseDir:        cfg.BaseDir,
			DebugLevel:     "debug",
			MinBackoff:     100 * time.Millisecond,
			MaxBackoff:     500 * time.Millisecond,
			RPCListener:    listener,
			Lnd: &client.LndConfig{
				Host:        cfg.LndNode.Cfg.RPCAddr(),
				MacaroonDir: rpcMacaroonDir,
				TLSPath:     cfg.LndNode.Cfg.TLSCertPath,
			},
		},
	}, nil
}

// start spins up the trader server listening for gRPC connections on a bufconn.
func (hs *traderHarness) start() error {
	var err error
	hs.server, err = client.NewServer(hs.clientCfg)
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
	rpcConn, err := ConnectAuctioneerRPC(netConn, hs.cfg.ServerTLSPath)
	if err != nil {
		return err
	}
	hs.TraderClient = clmrpc.NewTraderClient(rpcConn)
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

// auctionServerDialOpts creates the dial options that are needed to connect
// over the harness' connection to the agora server.
func (hs *traderHarness) auctionServerDialOpts(serverCertPath string) (
	[]grpc.DialOption, error) {

	dialer := func(ctx context.Context, target string) (net.Conn, error) {
		return hs.cfg.AuctioneerConn, nil
	}
	defaultOpts, err := defaultDialOptions(serverCertPath)
	if err != nil {
		return nil, err
	}
	return append(defaultOpts, grpc.WithContextDialer(dialer)), nil
}
