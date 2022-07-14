//go:build regtest
// +build regtest

package subasta

import (
	"fmt"
	"net"
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/chain"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightninglabs/subasta/status"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightningnetwork/lnd/lncfg"
)

var (
	dockerAuctioneerAddr = fmt.Sprintf("0.0.0.0:%d", defaultAuctioneerRPCPort)
	dockerRestAddr       = fmt.Sprintf("0.0.0.0:%d", defaultAuctioneerRESTPort)
)

type EtcdConfig struct {
	Host     string
	User     string
	Password string
}

type Config struct {
	Network        string
	BaseDir        string
	RPCListen      string
	RESTListen     string
	AdminRPCListen string

	SubscribeTimeout time.Duration

	ExecFeeBase int64
	ExecFeeRate int64

	BatchConfTarget int32

	MaxAcctValue int64
	MaxDuration  uint32

	TLSCertPath string
	TLSKeyPath  string

	// TLSExtraIP and TLSExtraDomain are exposed to make it easier to
	// connect to the auction server running in a dockerized environment.
	TLSExtraIP     string `long:"tlsextraip" description:"Adds an extra ip to the generated certificate"`
	TLSExtraDomain string `long:"tlsextradomain" description:"Adds an extra domain to the generated certificate"`

	LogDir         string
	MaxLogFiles    int
	MaxLogFileSize int

	DebugLevel      string
	Profile         string
	AllowFakeTokens bool

	AccountExpiryExtension uint32
	AccountExpiryOffset    uint32

	ExternalNodeRatingsActive  bool
	DefaultNodeTier            orderT.NodeTier
	NodeRatingsRefreshInterval time.Duration
	BosScoreWebURL             string

	FundingConflictResetInterval time.Duration
	TraderRejectResetInterval    time.Duration

	// Lnd config is exposed because that always needs to be configured,
	// even in a regtest environment.
	Lnd *LndConfig `group:"lnd" namespace:"lnd"`

	Cluster *lncfg.Cluster       `group:"cluster" namespace:"cluster" hidden:"true"`
	Bitcoin *chain.BitcoinConfig `group:"bitcoin" namespace:"bitcoin" hidden:"true"`
	SQL     *subastadb.SQLConfig `group:"sql" namespace:"sql" hidden:"true"`
	UseSQL  bool

	Status *status.Config `group:"status" namespace:"status" hidden:"true"`

	// Prometheus and etcd configs have a build tag so the regtest version
	// doesn't expose any CLI flags and we don't get any collisions that
	// require us to set the group and namespace tags as the config elements
	// above.
	Prometheus *monitoring.PrometheusConfig
	Etcd       *EtcdConfig

	// RPCListener is a network listener that the default auctionserver
	// should listen on.
	RPCListener net.Listener

	// AdminRPCListener is a network listener that the admin server should
	// listen on.
	AdminRPCListener net.Listener
}

// DefaultConfig returns the default config for a subasta server.
func DefaultConfig() *Config {
	return &Config{
		Network:          networkRegtest,
		BaseDir:          DefaultBaseDir,
		RPCListen:        dockerAuctioneerAddr,
		RESTListen:       dockerRestAddr,
		AdminRPCListen:   defaultAdminAddr,
		ExecFeeBase:      DefaultExecutionFeeBase,
		ExecFeeRate:      DefaultExecutionFeeRate,
		BatchConfTarget:  defaultBatchConfTarget,
		MaxAcctValue:     defaultMaxAcctValue,
		SubscribeTimeout: defaultSubscribeTimeout,
		Lnd: &LndConfig{
			Host: "localhost:10009",
		},
		Etcd: &EtcdConfig{
			// We use a non-default port here to avoid colliding
			// with a real etcd server that might be running on the
			// same (dev) machine.
			Host:     "localhost:2479",
			User:     etcdUserEmbedded,
			Password: etcdUserEmbedded,
		},
		Cluster:                      lncfg.DefaultCluster(),
		Prometheus:                   &monitoring.PrometheusConfig{},
		Bitcoin:                      &chain.BitcoinConfig{},
		SQL:                          &subastadb.SQLConfig{},
		Status:                       status.DefaultConfig(),
		TLSCertPath:                  defaultTLSCertPath,
		TLSKeyPath:                   defaultTLSKeyPath,
		MaxLogFiles:                  defaultMaxLogFiles,
		MaxLogFileSize:               defaultMaxLogFileSize,
		DebugLevel:                   "info",
		LogDir:                       defaultLogDir,
		AccountExpiryExtension:       defaultAccountExpiryExtension,
		AccountExpiryOffset:          defaultAccountExpiryOffset,
		DefaultNodeTier:              orderT.NodeTier1,
		NodeRatingsRefreshInterval:   defaultNodeRatingsRefreshInterval,
		FundingConflictResetInterval: defaultFundingConflictResetInterval,
		TraderRejectResetInterval:    defaultTraderRejectResetInterval,
		AllowFakeTokens:              true,
	}
}
