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
	"github.com/lightninglabs/agora/adminrpc"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/agora/client/clmrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"
)

var (
	etcdListenAddr = "127.0.0.1:9125"
)

// auctioneerHarness is a test harness that holds everything that is needed to
// start an instance of the auctioneer server.
type auctioneerHarness struct {
	cfg *auctioneerConfig

	serverCfg *agora.Config
	server    *agora.Server

	etcd  *embed.Etcd
	store agoradb.Store

	clmrpc.ChannelAuctioneerClient
	adminrpc.AuctionAdminClient
}

// auctioneerConfig holds all configuration items that are required to start an
// auctioneer server.
type auctioneerConfig struct {
	RPCListener      *bufconn.Listener
	AdminRPCListener *bufconn.Listener
	BackendCfg       lntest.BackendConfig
	LndNode          *lntest.HarnessNode
	NetParams        *chaincfg.Params
	BaseDir          string
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

	if cfg.LndNode == nil || cfg.LndNode.Cfg == nil {
		return nil, fmt.Errorf("lnd node configuration cannot be nil")
	}
	rpcMacaroonDir := filepath.Join(
		cfg.LndNode.Cfg.DataDir, "chain", "bitcoin", cfg.NetParams.Name,
	)

	return &auctioneerHarness{
		cfg: &cfg,
		serverCfg: &agora.Config{
			LogDir:           ".",
			MaxLogFiles:      99,
			MaxLogFileSize:   999,
			Network:          cfg.NetParams.Name,
			Insecure:         true,
			FakeAuth:         true,
			BaseDir:          cfg.BaseDir,
			OrderSubmitFee:   1337,
			SubscribeTimeout: 500 * time.Millisecond,
			DebugLevel:       "debug",
			RPCListener:      cfg.RPCListener,
			AdminRPCListener: cfg.AdminRPCListener,
			Lnd: &agora.LndConfig{
				Host:        cfg.LndNode.Cfg.RPCAddr(),
				MacaroonDir: rpcMacaroonDir,
				TLSPath:     cfg.LndNode.Cfg.TLSCertPath,
			},
			Etcd: &agora.EtcdConfig{
				Host:     etcdListenAddr,
				User:     "",
				Password: "",
			},
		},
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

	return hs.runServer()
}

// runServer starts the actual auctioneer server after the configuration and
// etcd server have already been created. Can be used to start the same node
// up again after it was turned off with halt().
func (hs *auctioneerHarness) runServer() error {
	var err error
	hs.server, err = agora.NewServer(hs.serverCfg)
	if err != nil {
		return fmt.Errorf("unable to create server: %v", err)
	}
	if err := hs.server.Start(); err != nil {
		return fmt.Errorf("unable to start server: %v", err)
	}

	// Connect our internal client to the main RPC server so we can interact
	// with it during the test.
	netConn, err := hs.cfg.RPCListener.Dial()
	if err != nil {
		return fmt.Errorf("could not listen on bufconn: %v", err)
	}
	rpcConn, err := ConnectAuctioneerRPC(netConn, hs.serverCfg.TLSCertPath)
	if err != nil {
		return err
	}
	hs.ChannelAuctioneerClient = clmrpc.NewChannelAuctioneerClient(
		rpcConn,
	)

	// Also connect our internal admin client to the main RPC server so we
	// can interact with it during the test.
	netConn, err = hs.cfg.AdminRPCListener.Dial()
	if err != nil {
		return fmt.Errorf("could not listen on bufconn: %v", err)
	}
	rpcConn, err = ConnectAuctioneerRPC(netConn, "")
	if err != nil {
		return err
	}
	hs.AuctionAdminClient = adminrpc.NewAuctionAdminClient(rpcConn)
	return nil
}

// halt temporarily shuts down the auctioneer server in a way that it can be
// started again later. The etcd server keeps running.
func (hs *auctioneerHarness) halt() error {
	err := hs.server.Stop()
	hs.server = nil
	return err
}

// stop shuts down the auctioneer server with its associated etcd server and
// finally deletes the server's temporary data directory.
func (hs *auctioneerHarness) stop() error {
	// Don't return the error immediately if stopping goes wrong, give etcd
	// a chance to stop as well and always remove the temp directory.
	err := hs.halt()
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

	hs.store, err = agoradb.NewEtcdStore(
		*hs.cfg.LndNode.Cfg.NetParams, etcdListenAddr, "", "",
	)
	if err != nil {
		return fmt.Errorf("unable to connect to etcd: %v", err)
	}

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, defaultWaitTimeout)
	defer cancel()
	if err := hs.store.Init(ctx); err != nil {
		return fmt.Errorf("unable to initialize etcd: %v", err)
	}

	return nil
}

// ConnectAuctioneerRPC uses the in-memory buffer connection to dial to the
// agora server.
func ConnectAuctioneerRPC(conn net.Conn, serverCertPath string) (
	*grpc.ClientConn, error) {

	dialer := func(ctx context.Context, target string) (net.Conn, error) {
		return conn, nil
	}
	defaultOpts, err := defaultDialOptions(serverCertPath)
	if err != nil {
		return nil, err
	}
	opts := append(defaultOpts, grpc.WithContextDialer(dialer))
	return grpc.DialContext(context.Background(), "", opts...)
}

// defaultDialOptions returns the default RPC dial options.
func defaultDialOptions(serverCertPath string) ([]grpc.DialOption, error) {
	transportOpts := grpc.WithInsecure()
	if serverCertPath != "" {
		creds, err := credentials.NewClientTLSFromFile(
			serverCertPath, "",
		)
		if err != nil {
			return nil, err
		}
		transportOpts = grpc.WithTransportCredentials(creds)
	}
	return []grpc.DialOption{
		grpc.WithBlock(),
		transportOpts,
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.DefaultConfig,
			MinConnectTimeout: 10 * time.Second,
		}),
	}, nil
}
