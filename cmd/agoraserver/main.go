package main

import (
	"os"
	"path/filepath"

	"github.com/jessevdk/go-flags"
)

func main() {
	// TODO: Prevent from running twice.
	err := start()
	if err != nil {
		log.Error(err)
	}
}

func start() error {
	// Pre-parse command line so that cfg.Network is set.
	cfg, err := preParse()
	if err != nil {
		return err
	}

	cfg.serverDir = filepath.Join(agoraServerDirBase, cfg.Network)
	if err := os.MkdirAll(cfg.serverDir, os.ModePerm); err != nil {
		return err
	}

	configFile := filepath.Join(cfg.serverDir, defaultConfigFilename)
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

// fileExists reports whether the named file or directory exists.
// This function is taken from https://github.com/btcsuite/btcd
func fileExists(name string) bool {
	if _, err := os.Stat(name); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// getParser returns a parser with the required options for agoraserver.
func getParser(cfg *config) *flags.Parser {
	parser := flags.NewParser(cfg, flags.Default)

	_, _ = parser.AddCommand(
		"daemon", "Run agora server", "",
		&daemonCommand{cfg: cfg},
	)

	return parser
}

// preParse parses the command line to make the network option available. This
// is required to find the correct config file path.
func preParse() (*config, error) {
	cfg := *defaultConfig
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
