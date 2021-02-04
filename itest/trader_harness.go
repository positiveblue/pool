package itest

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/lightninglabs/pool"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightningnetwork/lnd/lntest"
)

// traderHarness is a test harness that holds everything that is needed to
// start an instance of the trader server.
type traderHarness struct {
	cfg       *traderConfig
	server    *pool.Server
	clientCfg *pool.Config

	poolrpc.TraderClient
}

// traderConfig holds all configuration items that are required to start an
// trader server.
type traderConfig struct {
	AuctionServer string
	BackendCfg    lntest.BackendConfig
	ServerTLSPath string
	LndNode       *lntest.HarnessNode
	NetParams     *chaincfg.Params
	BaseDir       string
}

// traderCfgOpt is a function type that can manipulate the trader's config
// options.
type traderCfgOpt func(*pool.Config)

func newNodesOnlyOpt() traderCfgOpt {
	return func(cfg *pool.Config) {
		cfg.NewNodesOnly = true
	}
}

// newTraderHarness creates a new trader server harness with the given
// configuration.
func newTraderHarness(cfg traderConfig, opts []traderCfgOpt) (*traderHarness,
	error) {

	if cfg.BaseDir == "" {
		var err error
		cfg.BaseDir, err = ioutil.TempDir("", "itest-poold")
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

	tlsCertPath := filepath.Join(cfg.BaseDir, pool.DefaultTLSCertFilename)
	tlsKeyPath := filepath.Join(cfg.BaseDir, pool.DefaultTLSKeyFilename)
	macaroonPath := filepath.Join(cfg.BaseDir, pool.DefaultMacaroonFilename)

	traderCfg := &pool.Config{
		LogDir:         ".",
		MaxLogFiles:    99,
		MaxLogFileSize: 999,
		Network:        cfg.NetParams.Name,
		FakeAuth:       true,
		BaseDir:        cfg.BaseDir,
		DebugLevel:     "debug",
		MinBackoff:     100 * time.Millisecond,
		MaxBackoff:     500 * time.Millisecond,
		TLSCertPath:    tlsCertPath,
		TLSKeyPath:     tlsKeyPath,
		MacaroonPath:   macaroonPath,
		AuctionServer:  cfg.AuctionServer,
		TLSPathAuctSrv: cfg.ServerTLSPath,
		RPCListen:      fmt.Sprintf("127.0.0.1:%d", nextAvailablePort()),
		NewNodesOnly:   false,
		Lnd: &pool.LndConfig{
			Host:        cfg.LndNode.Cfg.RPCAddr(),
			MacaroonDir: rpcMacaroonDir,
			TLSPath:     cfg.LndNode.Cfg.TLSCertPath,
		},
	}
	for _, opt := range opts {
		opt(traderCfg)
	}

	return &traderHarness{
		cfg:       &cfg,
		clientCfg: traderCfg,
	}, nil
}

// start spins up the trader server listening for gRPC connections.
func (hs *traderHarness) start() error {
	var err error
	hs.server = pool.NewServer(hs.clientCfg)
	err = hs.server.Start()
	if err != nil {
		return fmt.Errorf("could not start trader server: %v", err)
	}

	// Create our client to interact with the trader RPC server directly.
	rpcConn, err := dialServer(
		hs.clientCfg.RPCListen, hs.clientCfg.TLSCertPath,
		hs.clientCfg.MacaroonPath,
	)
	if err != nil {
		return fmt.Errorf("could not connect to %v: %v",
			hs.clientCfg.RPCListen, err)
	}
	hs.TraderClient = poolrpc.NewTraderClient(rpcConn)
	return nil
}

// stop shuts down the trader server and deletes its temporary data directory.
func (hs *traderHarness) stop(deleteData bool) error {
	// Don't return the error immediately if stopping goes wrong, always
	// remove the temp directory.
	err := hs.server.Stop()
	if deleteData {
		_ = os.RemoveAll(hs.cfg.BaseDir)
	}

	return err
}
