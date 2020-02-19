package agora

import (
	"fmt"
	"net"
	"path/filepath"
	"time"

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

	// defaultSubscribeTimeout is the maximum time we give a client stream
	// subscriber to send us the first subscription message.
	defaultSubscribeTimeout = 10 * time.Second
)

var (
	// DefaultBaseDir is the default root data directory where agoraserver
	// will store all its data. On UNIX like systems this will resolve to
	// ~/.agoraserver. Below this directory the logs and network directory
	// will be created.
	DefaultBaseDir = btcutil.AppDataDir("agoraserver", false)

	defaultAuctioneerAddr = fmt.Sprintf(":%d", defaultAuctioneerRPCPort)
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
	Network string `long:"network" description:"network to run on" choice:"regtest" choice:"testnet" choice:"mainnet" choice:"simnet"`
	BaseDir string `long:"basedir" description:"The base directory where agoraserver stores all its data"`

	OrderSubmitFee   int64         `long:"ordersubmitfee" description:"Flat one-time fee (sat) to submit an order."`
	SubscribeTimeout time.Duration `long:"subscribetimeout" description:"The maximum duration we wait for a client to send the first subscription when connecting to the stream."`

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

	Lnd  *LndConfig  `group:"lnd" namespace:"lnd"`
	Etcd *EtcdConfig `group:"etcd" namespace:"etcd"`

	// RPCListener is a network listener that can be set if agoraserver
	// should be used as a library and listen on the given listener instead
	// of what is configured in the --rpclisten parameter.
	RPCListener net.Listener
}

var DefaultConfig = &Config{
	Network:          "mainnet",
	BaseDir:          DefaultBaseDir,
	OrderSubmitFee:   defaultOrderSubmitFee,
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
	TLSCertPath:    defaultTLSCertPath,
	TLSKeyPath:     defaultTLSKeyPath,
	MaxLogFiles:    defaultMaxLogFiles,
	MaxLogFileSize: defaultMaxLogFileSize,
	DebugLevel:     defaultLogLevel,
	LogDir:         defaultLogDir,
}
