package itest

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/aperture"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/subasta"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/lightninglabs/subasta/chain"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightningnetwork/lnd/lncfg"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/lightningnetwork/lnd/signal"
	"go.etcd.io/etcd/server/v3/embed"
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

	apertureCfg *aperture.Config
	aperture    *aperture.Aperture

	auctioneerrpc.ChannelAuctioneerClient
	adminrpc.AuctionAdminClient
}

// auctioneerConfig holds all configuration items that are required to start an
// auctioneer server.
type auctioneerConfig struct {
	BackendCfg  lntest.BackendConfig
	LndNode     *lntest.HarnessNode
	NetParams   *chaincfg.Params
	ClusterCfg  *lncfg.Cluster
	Interceptor signal.Interceptor
	BaseDir     string
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

	subastaTLSPath := path.Join(cfg.BaseDir, "tls.cert")
	subastaListenAddr := fmt.Sprintf("127.0.0.1:%d", nextAvailablePort())
	return &auctioneerHarness{
		cfg: &cfg,
		serverCfg: &subasta.Config{
			Network: cfg.NetParams.Name,
			// We'll turn on node ratings, but we don't set a bos
			// score URL. As a result, all nodes will be seen as
			// being in the lowest tier unless we manually set
			// their scores.
			NodeRatingsActive: true,
			AllowFakeTokens:   true,
			BaseDir:           cfg.BaseDir,
			TLSCertPath:       subastaTLSPath,
			TLSKeyPath:        path.Join(cfg.BaseDir, "tls.key"),
			ExecFeeBase:       subasta.DefaultExecutionFeeBase,
			ExecFeeRate:       subasta.DefaultExecutionFeeRate,
			BatchConfTarget:   6,
			MaxAcctValue:      10 * btcutil.SatoshiPerBitcoin,
			SubscribeTimeout:  500 * time.Millisecond,
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
			Cluster:        lncfg.DefaultCluster(),
			Prometheus:     &monitoring.PrometheusConfig{},
			Bitcoin:        &chain.BitcoinConfig{},
			MaxLogFiles:    99,
			MaxLogFileSize: 999,
			DebugLevel:     "debug,PRXY=info,AUTH=info,LSAT=info",
			LogDir:         ".",
			RPCListen:      subastaListenAddr,
			AdminRPCListen: fmt.Sprintf("127.0.0.1:%d",
				nextAvailablePort()),
			AccountExpiryExtension: 3024,
		},
		apertureCfg: &aperture.Config{
			ListenAddr: fmt.Sprintf("127.0.0.1:%d",
				nextAvailablePort()),
			Etcd: &aperture.EtcdConfig{
				Host:     etcdListenAddr,
				User:     "",
				Password: "",
			},
			Authenticator: &aperture.AuthConfig{
				LndHost: cfg.LndNode.Cfg.RPCAddr(),
				TLSPath: cfg.LndNode.Cfg.TLSCertPath,
				MacDir:  rpcMacaroonDir,
				Network: cfg.NetParams.Name,
			},
			Services: []*proxy.Service{{
				Name:        "pool",
				HostRegexp:  "^.*$",
				PathRegexp:  "/poolrpc.*$",
				Address:     subastaListenAddr,
				TLSCertPath: subastaTLSPath,
				Protocol:    "https",
				Price:       1,
				Auth:        "off",
				// We turn off authentication by default so the
				// whitelist will be ignored. But we still add
				// the same rules we have in production so when
				// we turn on authentication on a per-test basis
				// they are already pre-configured.
				AuthWhitelistPaths: []string{
					"^/poolrpc.ChannelAuctioneer/Terms.*$",
					"^/poolrpc.ChannelAuctioneer/NodeRating.*$",
					"^/poolrpc.ChannelAuctioneer/BatchSnapshots.*$",
					"^/poolrpc.ChannelAuctioneer/SubscribeSidecar.*$",
					"^/poolrpc.HashMail/NewCipherBox.*$",
					"^/poolrpc.HashMail/DelCipherBox.*$",
					"^/poolrpc.HashMail/SendStream.*$",
					"^/poolrpc.HashMail/RecvStream.*$",
				},
			}},
			DebugLevel: "debug",
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

	if err := hs.runServer(); err != nil {
		return fmt.Errorf("could not start subasta server: %v", err)
	}

	// We need to start subasta before aperture so the cert already exists.
	if err := hs.initAperture(); err != nil {
		return fmt.Errorf("could not start aperture: %v", err)
	}

	return nil
}

// runServer starts the actual auctioneer server after the configuration and
// etcd server have already been created. Can be used to start the same node
// up again after it was turned off with halt().
func (hs *auctioneerHarness) runServer() error {
	var err error
	hs.server, err = subasta.NewServer(hs.serverCfg, hs.cfg.Interceptor)
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
	hs.ChannelAuctioneerClient = auctioneerrpc.NewChannelAuctioneerClient(
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
	returnErr := hs.halt()
	hs.etcd.Close()

	if err := hs.aperture.Stop(); err != nil {
		returnErr = err
	}

	// The etcd data dir is also below the base dir and will be removed as
	// well.
	_ = os.RemoveAll(hs.cfg.BaseDir)

	return returnErr
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
		*hs.cfg.LndNode.Cfg.NetParams, etcdListenAddr, "", "", nil,
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

// initAperture starts the aperture proxy.
func (hs *auctioneerHarness) initAperture() error {
	hs.aperture = aperture.NewAperture(hs.apertureCfg)
	errChan := make(chan error)

	if err := hs.aperture.Start(errChan); err != nil {
		return fmt.Errorf("unable to start aperture: %v", err)
	}

	// Any error while starting?
	select {
	case err := <-errChan:
		return fmt.Errorf("error starting aperture: %v", err)
	default:
	}

	return nil
}

// getLogFileContent returns the complete content of the auctioneer's log file.
func (hs *auctioneerHarness) getLogFileContent() (string, error) {
	content, err := ioutil.ReadFile(path.Join(
		".", hs.cfg.NetParams.Name, "auctionserver.log",
	))
	return string(content), err
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
	cred, err := macaroons.NewMacaroonCredential(mac)
	if err != nil {
		return nil, fmt.Errorf("error creating mac cred: %v", err)
	}
	return grpc.WithPerRPCCredentials(cred), nil
}
