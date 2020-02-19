package itest

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/coreos/etcd/embed"
	"github.com/lightninglabs/agora"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/test/bufconn"
)

var (
	etcdListenAddr = "127.0.0.1:9125"
)

// auctioneerHarness is a test harness that holds everything that is needed to
// start an instance of the auctioneer server.
type auctioneerHarness struct {
	cfg    *auctioneerConfig
	server *agora.Server

	etcd *embed.Etcd

	clmrpc.ChannelAuctioneerClient
}

// auctioneerConfig holds all configuration items that are required to start an
// auctioneer server.
type auctioneerConfig struct {
	RPCListener *bufconn.Listener
	BackendCfg  lntest.BackendConfig
	LndNode     *lntest.HarnessNode
	NetParams   *chaincfg.Params
	BaseDir     string
}

// newAuctioneerHarness creates a new auctioneer server harness with the given
// configuration.
func newAuctioneerHarness(cfg auctioneerConfig) (*auctioneerHarness, error) {
	if cfg.BaseDir == "" {
		var err error
		cfg.BaseDir, err = ioutil.TempDir("", "itest-agoraserver")
		if err != nil {
			return nil, err
		}
	}
	return &auctioneerHarness{
		cfg: &cfg,
	}, nil
}

// start spins up an in-memory etcd server and the auctioneer server listening
// for gRPC connections on a bufconn.
func (hs *auctioneerHarness) start() error {
	// Start the embedded etcd server.
	err := hs.initEtcdServer()
	if err != nil {
		return fmt.Errorf("could not start embedded etcd: %v", err)
	}

	if hs.cfg.LndNode == nil || hs.cfg.LndNode.Cfg == nil {
		return fmt.Errorf("lnd node configuration cannot be nil")
	}
	rpcMacaroonDir := filepath.Join(
		hs.cfg.LndNode.Cfg.DataDir, "chain", "bitcoin",
		hs.cfg.NetParams.Name,
	)

	// Redirect output from the nodes to log files.
	cfg := &agora.Config{
		LogDir:         ".",
		MaxLogFiles:    99,
		MaxLogFileSize: 999,
		Network:        hs.cfg.NetParams.Name,
		Insecure:       true,
		BaseDir:        hs.cfg.BaseDir,
		DebugLevel:     "debug",
		RPCListener:    hs.cfg.RPCListener,
		Lnd: &agora.LndConfig{
			Host:        hs.cfg.LndNode.Cfg.RPCAddr(),
			MacaroonDir: rpcMacaroonDir,
			TLSPath:     hs.cfg.LndNode.Cfg.TLSCertPath,
		},
		Etcd: &agora.EtcdConfig{
			Host:     etcdListenAddr,
			User:     "",
			Password: "",
		},
	}
	hs.server, err = agora.NewServer(cfg)
	if err != nil {
		return fmt.Errorf("could not start agora server: %v", err)
	}

	// Since Stop uses the LightningClient to stop the node, if we fail to
	// get a connected client, we have to kill the process.
	netConn, err := hs.cfg.RPCListener.Dial()
	if err != nil {
		return fmt.Errorf("could not listen on bufconn: %v", err)
	}
	rpcConn, err := hs.ConnectRPC(netConn)
	if err != nil {
		return err
	}

	hs.ChannelAuctioneerClient = clmrpc.NewChannelAuctioneerClient(
		rpcConn,
	)
	return nil
}

// stop shuts down the auctioneer server with its associated etcd server and
// finally deletes the server's temporary data directory.
func (hs *auctioneerHarness) stop() error {
	// Don't return the error immediately if stopping goes wrong, give etcd
	// a chance to stop as well and always remove the temp directory.
	err := hs.server.Stop()
	hs.etcd.Close()

	// The etcd data dir is also below the base dir and will be removed as
	// well.
	_ = os.RemoveAll(hs.cfg.BaseDir)

	return err
}

// initEtcdServer starts and initializes an embedded etcd server.
func (hs *auctioneerHarness) initEtcdServer() error {
	var err error
	tempDir := filepath.Join(hs.cfg.BaseDir, "etcd")

	cfg := embed.NewConfig()
	cfg.Dir = tempDir
	cfg.LCUrls = []url.URL{{Host: etcdListenAddr}}
	cfg.LPUrls = []url.URL{{Host: "127.0.0.1:9126"}}

	hs.etcd, err = embed.StartEtcd(cfg)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return err
	}

	select {
	case <-hs.etcd.Server.ReadyNotify():
	case <-time.After(5 * time.Second):
		hs.etcd.Close()
		_ = os.RemoveAll(tempDir)
		return fmt.Errorf("server took too long to start")
	}

	return nil
}

// ConnectRPC uses the non-TLS in-memory buffer connection to dial to the
// agora server.
func (hs *auctioneerHarness) ConnectRPC(conn net.Conn) (*grpc.ClientConn,
	error) {

	opts := []grpc.DialOption{
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
	return grpc.DialContext(context.Background(), "", opts...)
}
