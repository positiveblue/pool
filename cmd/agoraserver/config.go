package main

import (
	"fmt"
	"path/filepath"

	"github.com/btcsuite/btcutil"
)

const (
	// defaultAuctioneerRPCPort is the default port that the auctioneer
	// server listens on.
	defaultAuctioneerRPCPort = 12009

	// defaultOrderSubmitFee is the default one-time fee that is charged for
	// submitting an order.
	defaultOrderSubmitFee = 1337

	// defaultTLSCertFilename is the default file name for the TLS
	// certificate.
	defaultTLSCertFilename = "tls.cert"

	// defaultTLSKeyFilename is the default file name for the TLS key.
	defaultTLSKeyFilename = "tls.key"

	// defaultConfigFilename is the default file name for the configuration
	// file for the auctioneer server.
	defaultConfigFilename = "agoraserver.conf"

	// defaultLogLevel is the default log level that is used for all loggers
	// and sub systems.
	defaultLogLevel = "info"

	// defaultLogDirname is the default directory name where the log files
	// will be stored.
	defaultLogDirname = "logs"

	// defaultLogFilename is the default file name for the auctioneer log
	// file.
	defaultLogFilename = "agoraserver.log"

	// defaultMaxLogFiles is the default number of log files to keep.
	defaultMaxLogFiles = 3

	// defaultMaxLogFileSize is the default file size of 10 MB that a log
	// file can grow to before it is rotated.
	defaultMaxLogFileSize = 10
)

var (
	defaultAuctioneerAddr = fmt.Sprintf(":%d", defaultAuctioneerRPCPort)

	agoraServerDirBase = btcutil.AppDataDir("agoraserver", false)

	defaultTLSCertPath = filepath.Join(
		agoraServerDirBase, defaultTLSCertFilename,
	)
	defaultTLSKeyPath = filepath.Join(
		agoraServerDirBase, defaultTLSKeyFilename,
	)

	defaultLogDir = filepath.Join(agoraServerDirBase, defaultLogDirname)
)

type lndConfig struct {
	Host        string `long:"host" description:"lnd instance rpc address"`
	MacaroonDir string `long:"macaroondir" description:"Path to lnd macaroons"`
	TLSPath     string `long:"tlspath" description:"Path to lnd tls certificate"`
}

type etcdConfig struct {
	Host     string `long:"host" description:"etcd instance address"`
	User     string `long:"user" description:"etcd user name"`
	Password string `long:"password" description:"etcd password"`
}

type config struct {
	Network string `long:"network" description:"network to run on" choice:"regtest" choice:"testnet" choice:"mainnet" choice:"simnet"`

	OrderSubmitFee int64 `long:"ordersubmitfee" description:"Flat one-time fee (sat) to submit an order."`

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

	Lnd  *lndConfig  `group:"lnd" namespace:"lnd"`
	Etcd *etcdConfig `group:"etcd" namespace:"etcd"`

	serverDir string
}

var defaultConfig = &config{
	Network:        "mainnet",
	OrderSubmitFee: defaultOrderSubmitFee,
	ServerName:     "auction.lightning.today",
	Insecure:       false,
	AutoCert:       false,
	Lnd: &lndConfig{
		Host: "localhost:10009",
	},
	Etcd: &etcdConfig{
		Host: "localhost:2379",
	},
	TLSCertPath:    defaultTLSCertPath,
	TLSKeyPath:     defaultTLSKeyPath,
	MaxLogFiles:    defaultMaxLogFiles,
	MaxLogFileSize: defaultMaxLogFileSize,
	DebugLevel:     defaultLogLevel,
	LogDir:         defaultLogDir,
}
