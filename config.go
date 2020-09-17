package subasta

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/subasta/chain"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/cert"
	"github.com/lightningnetwork/lnd/lnrpc"
	"golang.org/x/crypto/acme/autocert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	// defaultAuctioneerRPCPort is the default port that the auctioneer
	// server listens on.
	defaultAuctioneerRPCPort = 12009

	// defaultAdminRPCPort is the default port that the admin server listens
	// on.
	defaultAdminRPCPort = 13370

	// DefaultExecutionFeeBase is the default base fee in satoshis that we
	// charge per matched order.
	DefaultExecutionFeeBase = 1

	// DefaultExecutionFeeRate is the default variable fee rate in parts
	// per million that we charge per matched order. This ends up being 10
	// bps, or 0.001, or 0.1%.
	DefaultExecutionFeeRate = 1000

	// defaultBatchConfTarget is the default value we'll use for the fee
	// estimation of the batch transaction.
	defaultBatchConfTarget = 6

	// defaultTLSCertFilename is the default file name for the TLS
	// certificate.
	defaultTLSCertFilename = "tls.cert"

	// defaultTLSKeyFilename is the default file name for the TLS key.
	defaultTLSKeyFilename = "tls.key"

	// defaultLogLevel is the default log level that is used for all loggers
	// and sub systems.
	defaultLogLevel = "info"

	// defaultLogDirname is the default directory name where the log files
	// will be stored.
	defaultLogDirname = "logs"

	// defaultLogFilename is the default file name for the auctioneer log
	// file.
	defaultLogFilename = "auctionserver.log"

	// defaultMaxLogFiles is the default number of log files to keep.
	defaultMaxLogFiles = 3

	// defaultMaxLogFileSize is the default file size of 10 MB that a log
	// file can grow to before it is rotated.
	defaultMaxLogFileSize = 10

	// defaultSubscribeTimeout is the maximum time we give a client stream
	// subscriber to send us the first subscription message.
	defaultSubscribeTimeout = 10 * time.Second

	// defaultMsgTimeout is the default amount of time that we'll wait for
	// a trader to send us an expected batch execution message.
	defaultMsgTimeout = time.Second * 10

	// defaultBatchTickInterval is the default amount of time we'll wait
	// between attempts to create a new batch.
	defaultBatchTickInterval = time.Minute * 10

	// defaultMaxAcctValue is the default maximum account value we enforce
	// if no other configuration value is set.
	defaultMaxAcctValue int64 = btcutil.SatoshiPerBitcoin

	// defaultMaxDuration is the default maximum value for an order's min
	// duration (bid) or max duration (ask). The default is equal to the
	// number of blocks in a year.
	defaultMaxDuration uint32 = 365 * 144

	// defaultAccountExpiryOffset is a value expressed in blocks that subtracted
	// from the expiry of an account in order to determine if it expires
	// "too soon". This value should essentially represent an upper bound
	// on worst-case block confirmation. If a batch takes longer than this
	// to confirm, then a race condition can happen if the account was
	// involved in a prior batch.
	defaultAccountExpiryOffset = 144
)

var (
	// DefaultBaseDir is the default root data directory where auctionserver
	// will store all its data. On UNIX like systems this will resolve to
	// ~/.auctionserver. Below this directory the logs and network directory
	// will be created.
	DefaultBaseDir = btcutil.AppDataDir("auctionserver", false)

	defaultAuctioneerAddr = fmt.Sprintf(":%d", defaultAuctioneerRPCPort)
	defaultAdminAddr      = fmt.Sprintf("127.0.0.1:%d", defaultAdminRPCPort)
	defaultTLSCertPath    = filepath.Join(
		DefaultBaseDir, defaultTLSCertFilename,
	)
	defaultTLSKeyPath = filepath.Join(
		DefaultBaseDir, defaultTLSKeyFilename,
	)
	defaultLogDir = filepath.Join(DefaultBaseDir, defaultLogDirname)
)

type LndConfig struct {
	Host        string `long:"host" description:"lnd instance rpc address"`
	MacaroonDir string `long:"macaroondir" description:"Path to lnd macaroons"`
	TLSPath     string `long:"tlspath" description:"Path to lnd tls certificate"`
}

type EtcdConfig struct {
	Host     string `long:"host" description:"etcd instance address"`
	User     string `long:"user" description:"etcd user name"`
	Password string `long:"password" description:"etcd password"`
}

type Config struct {
	Network        string `long:"network" description:"network to run on" choice:"regtest" choice:"testnet" choice:"mainnet" choice:"simnet"`
	BaseDir        string `long:"basedir" description:"The base directory where auctionserver stores all its data"`
	RPCListen      string `long:"rpclisten" description:"Address to listen on for gRPC clients"`
	AdminRPCListen string `long:"adminrpclisten" description:"Address to listen on for gRPC admin clients"`

	SubscribeTimeout time.Duration `long:"subscribetimeout" description:"The maximum duration we wait for a client to send the first subscription when connecting to the stream."`

	ExecFeeBase int64 `long:"execfeebase" description:"The execution base fee in satoshis that is charged per matched order."`
	ExecFeeRate int64 `long:"execfeerate" description:"The execution fee rate in parts per million that is charged per matched order."`

	BatchConfTarget int32 `long:"batchconftarget" description:"The confirmation target that will be used for fee estimation of batch transactions."`

	MaxAcctValue int64  `long:"maxacctvalue" description:"The maximum account value we enforce on the auctioneer side."`
	MaxDuration  uint32 `long:"maxduration" description:"The maximum value for the min/max order duration values."`

	ServerName string `long:"servername" description:"Server name to use for the tls certificate"`
	Insecure   bool   `long:"insecure" description:"disable tls"`
	AutoCert   bool   `long:"autocert" description:"automatically create a Let's Encrypt cert using ServerName"`

	TLSCertPath    string `long:"tlscertpath" description:"Path to write the TLS certificate for lnd's RPC and REST services"`
	TLSKeyPath     string `long:"tlskeypath" description:"Path to write the TLS private key for lnd's RPC and REST services"`
	TLSExtraIP     string `long:"tlsextraip" description:"Adds an extra ip to the generated certificate"`
	TLSExtraDomain string `long:"tlsextradomain" description:"Adds an extra domain to the generated certificate"`

	LogDir         string `long:"logdir" description:"Directory to log output."`
	MaxLogFiles    int    `long:"maxlogfiles" description:"Maximum logfiles to keep (0 for no rotation)"`
	MaxLogFileSize int    `long:"maxlogfilesize" description:"Maximum logfile size in MB"`

	DebugLevel string `short:"d" long:"debuglevel" description:"Logging level for all subsystems {trace, debug, info, warn, error, critical} -- You may also specify <subsystem>=<level>,<subsystem2>=<level>,... to set the log level for individual subsystems -- Use show to list available subsystems"`
	Profile    string `long:"profile" description:"Enable HTTP profiling on given port -- NOTE port must be between 1024 and 65535"`
	FakeAuth   bool   `long:"fakeauth" description:"Use fake LSAT authentication, allow traders to set fake LSAT ID. For testing only, cannot be set on mainnet."`

	AccountExpiryOffset uint32 `long:"accountexpiryoffset" description:"padding applied to the current block height to determine if an account expires soon"`

	Lnd        *LndConfig                   `group:"lnd" namespace:"lnd"`
	Etcd       *EtcdConfig                  `group:"etcd" namespace:"etcd"`
	Prometheus *monitoring.PrometheusConfig `group:"prometheus" namespace:"prometheus"`
	Bitcoin    *chain.BitcoinConfig         `group:"bitcoin" namespace:"bitcoin"`

	// RPCListener is a network listener that the default auctionserver
	// should listen on.
	RPCListener net.Listener

	// AdminRPCListener is a network listener that the admin server should
	// listen on.
	AdminRPCListener net.Listener
}

var DefaultConfig = &Config{
	Network:          "mainnet",
	BaseDir:          DefaultBaseDir,
	RPCListen:        defaultAuctioneerAddr,
	AdminRPCListen:   defaultAdminAddr,
	ExecFeeBase:      DefaultExecutionFeeBase,
	ExecFeeRate:      DefaultExecutionFeeRate,
	BatchConfTarget:  defaultBatchConfTarget,
	MaxAcctValue:     defaultMaxAcctValue,
	MaxDuration:      defaultMaxDuration,
	SubscribeTimeout: defaultSubscribeTimeout,
	ServerName:       "auction.lightning.today",
	Insecure:         false,
	AutoCert:         false,
	Lnd: &LndConfig{
		Host: "localhost:10009",
	},
	Etcd: &EtcdConfig{
		Host: "localhost:2379",
	},
	Prometheus: &monitoring.PrometheusConfig{
		ListenAddr: "localhost:8989",
	},
	Bitcoin: &chain.BitcoinConfig{
		Host: "localhost:8332",
	},
	TLSCertPath:         defaultTLSCertPath,
	TLSKeyPath:          defaultTLSKeyPath,
	MaxLogFiles:         defaultMaxLogFiles,
	MaxLogFileSize:      defaultMaxLogFileSize,
	DebugLevel:          defaultLogLevel,
	LogDir:              defaultLogDir,
	AccountExpiryOffset: defaultAccountExpiryOffset,
}

// extractCertOpt examines the main configuration to create a grpc.ServerOption
// instance which encodes our TLS parameters.
func extractCertOpt(cfg *Config) (grpc.ServerOption, error) {
	var certOpt grpc.ServerOption

	switch {
	// If auto cert is configured, then we'll create a cert automatically
	// using Let's Encrypt.
	case !cfg.Insecure && cfg.AutoCert:
		serverName := cfg.ServerName
		if serverName == "" {
			return nil, errors.New("servername option is required " +
				"for secure operation")
		}

		certDir := filepath.Join(cfg.BaseDir, "autocert")
		log.Infof("Configuring autocert for server %v and cache dir %v",
			serverName, certDir)

		manager := autocert.Manager{
			Cache:      autocert.DirCache(certDir),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(serverName),
		}

		go func() {
			err := http.ListenAndServe(
				":http", manager.HTTPHandler(nil),
			)
			if err != nil {
				log.Errorf("Autocert http failed: %v", err)
			}
		}()
		tlsConf := &tls.Config{
			GetCertificate: manager.GetCertificate,
		}

		sCreds := credentials.NewTLS(tlsConf)

		certOpt = grpc.Creds(sCreds)

	// Otherwise, we'll generate custom self-signed cets.
	case !cfg.Insecure:
		// Ensure we create TLS key and certificate if they don't exist
		if !lnrpc.FileExists(cfg.TLSCertPath) &&
			!lnrpc.FileExists(cfg.TLSKeyPath) {

			err := cert.GenCertPair(
				"subasta autogenerated cert", cfg.TLSCertPath,
				cfg.TLSKeyPath, nil, nil, false,
				cert.DefaultAutogenValidity,
			)
			if err != nil {
				return nil, err
			}
		}
		certData, _, err := cert.LoadCert(
			cfg.TLSCertPath, cfg.TLSKeyPath,
		)
		if err != nil {
			return nil, err
		}
		sCreds := credentials.NewTLS(cert.TLSConfFromCert(certData))

		certOpt = grpc.Creds(sCreds)
	}

	return certOpt, nil
}

func initLogging(cfg *Config) error {
	// Append the network type to the log directory so it is "namespaced"
	// per network in the same fashion as the data directory.
	logDir := filepath.Join(cfg.LogDir, cfg.Network)

	// Initialize logging at the default logging level.
	err := logWriter.InitLogRotator(
		filepath.Join(logDir, defaultLogFilename),
		cfg.MaxLogFileSize, cfg.MaxLogFiles,
	)
	if err != nil {
		return err
	}

	return build.ParseAndSetDebugLevels(cfg.DebugLevel, logWriter)
}
