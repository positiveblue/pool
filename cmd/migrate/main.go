package main

import (
	"github.com/jessevdk/go-flags"
)

// getParser returns a parser with the required options for auctionserver.
func getParser(cfg *Config) *flags.Parser {
	parser := flags.NewParser(cfg, flags.Default)

	_, err := parser.AddCommand(
		"migrate", "Run auction server", "",
		&migrateCommand{cfg: cfg},
	)
	if err != nil {
		panic(err)
	}

	_, err = parser.AddCommand(
		"diff", "Run the diff between two files", "",
		&diffCommand{cfg: cfg},
	)
	if err != nil {
		panic(err)
	}

	return parser
}

func main() {
	cfg := DefaultConfig()

	// Parse command line flags again to restore flags overwritten by ini
	// file and execute command.
	parser := getParser(cfg)
	_, err := parser.Parse()
	if err != nil {
		panic(err)
	}
}
