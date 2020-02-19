package main

import (
	"fmt"
	"os"

	"github.com/lightninglabs/agora"
	"github.com/lightningnetwork/lnd/signal"
)

type daemonCommand struct {
	cfg *agora.Config
}

func (x *daemonCommand) Execute(_ []string) error {
	// Special show command to list supported subsystems and exit.
	if x.cfg.DebugLevel == "show" {
		fmt.Printf("Supported subsystems: %v\n",
			agora.SupportedSubsystems())
		os.Exit(0)
	}

	signal.Intercept()

	server, err := agora.NewServer(x.cfg)
	if err != nil {
		return fmt.Errorf("unable to start server: %v", err)
	}

	// Wait for any external interrupt signal.
	<-signal.ShutdownChannel()

	return server.Stop()
}
