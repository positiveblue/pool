package subasta

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/cert"
	"github.com/lightningnetwork/lnd/lnrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	// defaultAuctioneerRPCPort is the default port that the auctioneer
	// server listens on.
	defaultAuctioneerRPCPort = 12009

	// defaultAuctioneerRESTPort is the default port that the auctioneer
	// server listens on for REST requests.
	defaultAuctioneerRESTPort = 12080

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
	defaultMsgTimeout = 30 * time.Second

	// defaultBatchTickInterval is the default amount of time we'll wait
	// between attempts to create a new batch.
	defaultBatchTickInterval = time.Minute * 10

	// defaultMaxAcctValue is the default maximum account value we enforce
	// if no other configuration value is set.
	defaultMaxAcctValue int64 = btcutil.SatoshiPerBitcoin

	// defaultAccountExpiryExtension is a value expressed in blocks used
	// to automatically extend the expiry height of account close to expiry
	// after participating in a batch. The default value is 3024 (~3 weeks).
	// That means that if an account participating in  a batch is set to
	// expire in less than 3024 blocks it will be extended to 3024 blocks from
	// the current bestHeight.
	defaultAccountExpiryExtension = 3024

	// defaultAccountExpiryOffset is a value expressed in blocks that
	// subtracted from the expiry of an account in order to determine if it
	// expires "too soon". This value should essentially represent an upper
	// bound on worst-case block confirmation.
	//
	// NOTE: If a batch takes longer than this to confirm, then a race
	// condition can happen if the account was involved in a prior batch.
	// On top of that, users with matched zero conf orders which have not
	// been confirmed yet could potentially lose routed funds.
	defaultAccountExpiryOffset = 432

	// defaultNodeRatingsRefreshInterval is the interval that we'll use to
	// continually refresh the node ratings to cache locally so we don't
	// hammer the end point.
	defaultNodeRatingsRefreshInterval = time.Minute * 20

	// defaultBosScoreURL is the default mainnet bos score URL. It exists
	// on testnet as well, but isn't as reliable since that's essentially a
	// zombie network.
	defaultBosScoreURL = "https://nodes.lightning.computer/availability/v1/btc.json"

	// defaultMetricsRefreshCacheInterval is the time interval at which
	// we re-populate batches and orders within the metrics manager class
	// to calculate orders.
	defaultMetricsRefreshCacheInterval = time.Minute * 10

	// defaultFundingConflictResetInterval is the default interval after
	// which we automatically reset the funding conflict map. By default we
	// don't reset the funding conflicts automatically.
	defaultFundingConflictResetInterval = time.Duration(0)

	// defaultTraderRejectResetInterval is the default interval after which
	// we automatically reset the trader reject conflict map.
	defaultTraderRejectResetInterval = time.Hour * 24

	// DefaultAutogenValidity is the default validity of a self-signed
	// certificate. The value corresponds to 14 months
	// (14 months * 30 days * 24 hours).
	DefaultAutogenValidity = 14 * 30 * 24 * time.Hour

	// defaultMaxSQLConnections is the default number of connections we
	// allow the SQL client to open simultaneously against the server.
	defaultMaxSQLConnections = 10

	networkRegtest = "regtest"
	userEmbedded   = "embedded"
)

var (
	// DefaultBaseDir is the default root data directory where auctionserver
	// will store all its data. On UNIX like systems this will resolve to
	// ~/.auctionserver. Below this directory the logs and network directory
	// will be created.
	DefaultBaseDir = btcutil.AppDataDir("auctionserver", false)

	defaultAuctioneerAddr = fmt.Sprintf(":%d", defaultAuctioneerRPCPort)
	defaultRestAddr       = fmt.Sprintf(":%d", defaultAuctioneerRESTPort)
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

// getTLSConfig examines the main configuration to create a *tls.Config and
// grpc.DialOption instance which encodes our TLS parameters for both the gRPC
// server and the REST proxy client.
func getTLSConfig(cfg *Config) (*tls.Config, grpc.DialOption, error) {
	// Ensure we create TLS key and certificate if they don't exist
	if !lnrpc.FileExists(cfg.TLSCertPath) &&
		!lnrpc.FileExists(cfg.TLSKeyPath) {

		err := cert.GenCertPair(
			"auctionserver autogenerated cert", cfg.TLSCertPath,
			cfg.TLSKeyPath, nil, nil, false,
			DefaultAutogenValidity,
		)
		if err != nil {
			return nil, nil, err
		}
	}
	certData, x509Cert, err := cert.LoadCert(
		cfg.TLSCertPath, cfg.TLSKeyPath,
	)
	if err != nil {
		return nil, nil, err
	}

	// The REST proxy is a client that connects to the gRPC server so it
	// needs to add our server cert as a trusted CA.
	certPool := x509.NewCertPool()
	certPool.AddCert(x509Cert)
	clientTLS := credentials.NewClientTLSFromCert(certPool, "")

	return cert.TLSConfFromCert(certData),
		grpc.WithTransportCredentials(clientTLS), nil
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
