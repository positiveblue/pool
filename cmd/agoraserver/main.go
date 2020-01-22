package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jessevdk/go-flags"
	"github.com/lightninglabs/agora"
)

var (
	// defaultConfigFilename is the default file name for the configuration
	// file for the auctioneer server.
	defaultConfigFilename = "agoraserver.conf"
)

func main() {
	// TODO: Prevent from running twice.
	err := start()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func start() error {
	// Pre-parse command line so that cfg.Network is set.
	cfg, err := preParse()
	if err != nil {
		return err
	}

	networkDir := filepath.Join(cfg.BaseDir, cfg.Network)
	if err := os.MkdirAll(networkDir, os.ModePerm); err != nil {
		return err
	}

	configFile := filepath.Join(networkDir, defaultConfigFilename)
	if err := flags.IniParse(configFile, cfg); err != nil {
		// If it's a parsing related error, then we'll return
		// immediately, otherwise we can proceed as possibly the cfg
		// file doesn't exist which is OK.
		if _, ok := err.(*flags.IniError); ok {
			return err
		}
	}

	// Parse command line flags again to restore flags overwritten by ini
	// file and execute command.
	parser := getParser(cfg)
	_, err = parser.Parse()

	return err
}

// getParser returns a parser with the required options for agoraserver.
func getParser(cfg *agora.Config) *flags.Parser {
	parser := flags.NewParser(cfg, flags.Default)

	_, _ = parser.AddCommand(
		"daemon", "Run agora server", "",
		&daemonCommand{cfg: cfg},
	)

	return parser
}

// preParse parses the command line to make the network option available. This
// is required to find the correct config file path.
func preParse() (*agora.Config, error) {
	cfg := *agora.DefaultConfig
	preParser := getParser(&cfg)

	// Don't execute commands during pre-parsing.
	preParser.CommandHandler = func(command flags.Commander,
		args []string) error {

		return nil
	}
	_, err := preParser.Parse()
	if e, ok := err.(*flags.Error); ok && e.Type == flags.ErrHelp {
		return &cfg, nil
	}
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}
