package main

import (
	"github.com/lightninglabs/agora"
	"github.com/lightningnetwork/lnd/signal"
)

type daemonCommand struct {
	cfg *agora.Config
}

func (x *daemonCommand) Execute(_ []string) error {
	signal.Intercept()

	x.cfg.ShutdownChannel = signal.ShutdownChannel()
	return agora.Start(x.cfg)
}
