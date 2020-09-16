package itest

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/subasta"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightninglabs/subasta/chain"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/lightningnetwork/lnd/macaroons"
	"go.etcd.io/etcd/embed"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"gopkg.in/macaroon.v2"
)

var (
	etcdListenAddr = fmt.Sprintf("127.0.0.1:%d", nextAvailablePort())
)

// auctioneerHarness is a test harness that holds everything that is needed to
// start an instance of the auctioneer server.
type auctioneerHarness struct {
	cfg *auctioneerConfig

	serverCfg *subasta.Config
	server    *subasta.Server

	etcd  *embed.Etcd
	store subastadb.Store

	poolrpc.ChannelAuctioneerClient
	adminrpc.AuctionAdminClient
}

// auctioneerConfig holds all configuration items that are required to start an
// auctioneer server.
type auctioneerConfig struct {
	BackendCfg lntest.BackendConfig
	LndNode    *lntest.HarnessNode
	NetParams  *chaincfg.Params
	BaseDir    string
}

// newAuctioneerHarness creates a new auctioneer server harness with the given
// configuration.
func newAuctioneerHarness(cfg auctioneerConfig) (*auctioneerHarness, error) {
	if cfg.BaseDir == "" {
		var err error
		cfg.BaseDir, err = ioutil.TempDir("", "itest-auctionserver")
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
		serverCfg: &subasta.Config{
			Network:          cfg.NetParams.Name,
			Insecure:         true,
			FakeAuth:         true,
			BaseDir:          cfg.BaseDir,
			ExecFeeBase:      subasta.DefaultExecutionFeeBase,
			ExecFeeRate:      subasta.DefaultExecutionFeeRate,
			BatchConfTarget:  6,
			MaxAcctValue:     btcutil.SatoshiPerBitcoin,
			MaxDuration:      365 * 144,
			SubscribeTimeout: 500 * time.Millisecond,
			Lnd: &subasta.LndConfig{
				Host:        cfg.LndNode.Cfg.RPCAddr(),
				MacaroonDir: rpcMacaroonDir,
				TLSPath:     cfg.LndNode.Cfg.TLSCertPath,
			},
			Etcd: &subasta.EtcdConfig{
				Host:     etcdListenAddr,
				User:     "",
				Password: "",
			},
			Prometheus:     &monitoring.PrometheusConfig{},
			Bitcoin:        &chain.BitcoinConfig{},
			MaxLogFiles:    99,
			MaxLogFileSize: 999,
			DebugLevel:     "debug",
			LogDir:         ".",
			RPCListen: fmt.Sprintf("127.0.0.1:%d",
				nextAvailablePort()),
			AdminRPCListen: fmt.Sprintf("127.0.0.1:%d",
				nextAvailablePort()),
		},
	}, nil
}

// start spins up an in-memory etcd server and the auctioneer server listening
// for gRPC connections.
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
	hs.server, err = subasta.NewServer(hs.serverCfg)
	if err != nil {
		return fmt.Errorf("unable to create server: %v", err)
	}
	if err := hs.server.Start(); err != nil {
		return fmt.Errorf("unable to start server: %v", err)
	}

	// Connect our internal client to the main RPC server so we can interact
	// with it during the test.
	rpcConn, err := dialServer(
		hs.serverCfg.RPCListen, hs.serverCfg.TLSCertPath, "",
	)
	if err != nil {
		return err
	}
	hs.ChannelAuctioneerClient = poolrpc.NewChannelAuctioneerClient(
		rpcConn,
	)

	// Also connect our internal admin client to the main RPC server so we
	// can interact with it during the test.
	rpcConn, err = dialServer(hs.serverCfg.AdminRPCListen, "", "")
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
	cfg.Logger = "zap"
	cfg.LogLevel = "error"
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

	hs.store, err = subastadb.NewEtcdStore(
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

// dialServer creates a gRPC client connection to the given host using a default
// timeout context.
func dialServer(rpcHost, tlsCertPath, macaroonPath string) (*grpc.ClientConn,
	error) {

	defaultOpts, err := defaultDialOptions(tlsCertPath, macaroonPath)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return grpc.DialContext(ctx, rpcHost, defaultOpts...)
}

// defaultDialOptions returns the default RPC dial options.
func defaultDialOptions(serverCertPath, macaroonPath string) ([]grpc.DialOption,
	error) {

	baseOpts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.DefaultConfig,
			MinConnectTimeout: 10 * time.Second,
		}),
	}

	if serverCertPath != "" {
		err := wait.Predicate(func() bool {
			return lnrpc.FileExists(serverCertPath)
		}, defaultTimeout)
		if err != nil {
			return nil, err
		}

		creds, err := credentials.NewClientTLSFromFile(
			serverCertPath, "",
		)
		if err != nil {
			return nil, err
		}
		baseOpts = append(baseOpts, grpc.WithTransportCredentials(creds))
	} else {
		baseOpts = append(baseOpts, grpc.WithInsecure())
	}

	if macaroonPath != "" {
		macaroonOptions, err := readMacaroon(macaroonPath)
		if err != nil {
			return nil, fmt.Errorf("unable to load macaroon %s: %v",
				macaroonPath, err)
		}
		baseOpts = append(baseOpts, macaroonOptions)
	}

	return baseOpts, nil
}

// readMacaroon tries to read the macaroon file at the specified path and create
// gRPC dial options from it.
func readMacaroon(macaroonPath string) (grpc.DialOption, error) {
	// Load the specified macaroon file.
	macBytes, err := ioutil.ReadFile(macaroonPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read macaroon path : %v", err)
	}

	mac := &macaroon.Macaroon{}
	if err = mac.UnmarshalBinary(macBytes); err != nil {
		return nil, fmt.Errorf("unable to decode macaroon: %v", err)
	}

	// Now we append the macaroon credentials to the dial options.
	cred := macaroons.NewMacaroonCredential(mac)
	return grpc.WithPerRPCCredentials(cred), nil
}
