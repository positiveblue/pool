//go:build !regtest
// +build !regtest

package subasta

import (
	"net"
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chain"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightninglabs/subasta/status"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightningnetwork/lnd/lncfg"
)

type EtcdConfig struct {
	Host     string `long:"host" description:"etcd instance address"`
	User     string `long:"user" description:"etcd user name"`
	Password string `long:"password" description:"etcd password"`
}

type Config struct {
	Network        string `long:"network" description:"network to run on" choice:"regtest" choice:"testnet" choice:"mainnet" choice:"simnet"`
	BaseDir        string `long:"basedir" description:"The base directory where auctionserver stores all its data"`
	RPCListen      string `long:"rpclisten" description:"Address to listen on for gRPC clients"`
	RESTListen     string `long:"restlisten" description:"Address to listen on for REST clients"`
	AdminRPCListen string `long:"adminrpclisten" description:"Address to listen on for gRPC admin clients"`

	SubscribeTimeout time.Duration `long:"subscribetimeout" description:"The maximum duration we wait for a client to send the first subscription when connecting to the stream."`

	ExecFeeBase int64 `long:"execfeebase" description:"The execution base fee in satoshis that is charged per matched order."`
	ExecFeeRate int64 `long:"execfeerate" description:"The execution fee rate in parts per million that is charged per matched order."`

	BatchConfTarget int32 `long:"batchconftarget" description:"The confirmation target that will be used for fee estimation of batch transactions."`

	MaxAcctValue int64  `long:"maxacctvalue" description:"The maximum account value we enforce on the auctioneer side."`
	MaxDuration  uint32 `long:"maxduration" description:"The maximum value for the min/max order duration values."`

	TLSCertPath    string `long:"tlscertpath" description:"Path to write the TLS certificate for lnd's RPC and REST services"`
	TLSKeyPath     string `long:"tlskeypath" description:"Path to write the TLS private key for lnd's RPC and REST services"`
	TLSExtraIP     string `long:"tlsextraip" description:"Adds an extra ip to the generated certificate"`
	TLSExtraDomain string `long:"tlsextradomain" description:"Adds an extra domain to the generated certificate"`

	LogDir         string `long:"logdir" description:"Directory to log output."`
	MaxLogFiles    int    `long:"maxlogfiles" description:"Maximum logfiles to keep (0 for no rotation)"`
	MaxLogFileSize int    `long:"maxlogfilesize" description:"Maximum logfile size in MB"`

	DebugLevel      string `short:"d" long:"debuglevel" description:"Logging level for all subsystems {trace, debug, info, warn, error, critical} -- You may also specify <subsystem>=<level>,<subsystem2>=<level>,... to set the log level for individual subsystems -- Use show to list available subsystems"`
	Profile         string `long:"profile" description:"Enable HTTP profiling on given port -- NOTE port must be between 1024 and 65535"`
	AllowFakeTokens bool   `long:"allowfaketokens" description:"Also allow traders to set fake LSAT IDs if needed. Normal LSATs still work. For testing only, cannot be set on mainnet."`

	AccountExpiryExtension uint32 `long:"accountexpiryextension" description:"block height threshold to determine if an account needs to be extended and for how long"`
	AccountExpiryOffset    uint32 `long:"accountexpiryoffset" description:"padding applied to the current block height to determine if an account expires soon"`

	ExternalNodeRatingsActive  bool            `long:"noderatingsactive" description:"if true external node ratings will be used in order matching, if false, the ratings aren't updated from external sources"`
	DefaultNodeTier            orderT.NodeTier `long:"defaultnodetier" description:"the default node tier to return if node isn't found in node ratings database; 1=tier_0, 2=tier_1"`
	NodeRatingsRefreshInterval time.Duration   `long:"ratingsrefreshinterval" description:"the refresh interval of the node ratings: 5s, 5m, etc"`
	BosScoreWebURL             string          `long:"bosscoreurl" description:"should point to the current bos score JSON endpoint"`

	MetricsRefreshCacheInterval time.Duration `long:"metricsrefreshcacheinterval" description:"the refresh interval of the order and batch metrics: 5s, 5m, etc"`

	FundingConflictResetInterval time.Duration `long:"fundingconflictresetinterval" description:"the reset interval for funding conflicts (errors during channel opens), set to 0 for no automatic reset"`
	TraderRejectResetInterval    time.Duration `long:"traderrejectresetinterval" description:"the reset interval for trader rejects (partial rejects because of --newnodesonly flag)"`

	DefaultAuctioneerVersion account.AuctioneerVersion `long:"defaultauctioneerversion" description:"the version to use when creating new auctioneer account outputs"`

	Lnd        *LndConfig                   `group:"lnd" namespace:"lnd"`
	Etcd       *EtcdConfig                  `group:"etcd" namespace:"etcd"`
	Cluster    *lncfg.Cluster               `group:"cluster" namespace:"cluster"`
	Prometheus *monitoring.PrometheusConfig `group:"prometheus" namespace:"prometheus"`
	Bitcoin    *chain.BitcoinConfig         `group:"bitcoin" namespace:"bitcoin"`
	SQL        *subastadb.SQLConfig         `group:"sql" namespace:"sql"`
	Status     *status.Config               `group:"status" namespace:"status"`

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
		Network:          "mainnet",
		BaseDir:          DefaultBaseDir,
		RPCListen:        defaultAuctioneerAddr,
		RESTListen:       defaultRestAddr,
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
			Host: "localhost:2379",
		},
		Cluster: lncfg.DefaultCluster(),
		Prometheus: &monitoring.PrometheusConfig{
			ListenAddr: "localhost:8989",
		},
		Bitcoin: &chain.BitcoinConfig{
			Host: "localhost:8332",
		},
		SQL: &subastadb.SQLConfig{
			Host:               "localhost",
			Port:               5432,
			User:               "pool",
			Password:           "pool",
			DBName:             "pool",
			MaxOpenConnections: defaultMaxSQLConnections,
		},
		Status:                       status.DefaultConfig(),
		TLSCertPath:                  defaultTLSCertPath,
		TLSKeyPath:                   defaultTLSKeyPath,
		MaxLogFiles:                  defaultMaxLogFiles,
		MaxLogFileSize:               defaultMaxLogFileSize,
		DebugLevel:                   defaultLogLevel,
		LogDir:                       defaultLogDir,
		AccountExpiryExtension:       defaultAccountExpiryExtension,
		AccountExpiryOffset:          defaultAccountExpiryOffset,
		DefaultNodeTier:              orderT.NodeTier0,
		NodeRatingsRefreshInterval:   defaultNodeRatingsRefreshInterval,
		MetricsRefreshCacheInterval:  defaultMetricsRefreshCacheInterval,
		BosScoreWebURL:               defaultBosScoreURL,
		FundingConflictResetInterval: defaultFundingConflictResetInterval,
		TraderRejectResetInterval:    defaultTraderRejectResetInterval,
		DefaultAuctioneerVersion:     account.VersionTaprootEnabled,
	}
}
