package main

import (
	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/agora"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"
)

const Subsystem = "SRVR"

var (
	logWriter = build.NewRotatingLogWriter()
	log       = build.NewSubLogger(Subsystem, logWriter.GenSubLogger)
)

func init() {
	setSubLogger(Subsystem, log, nil)
	addSubLogger(agora.Subsystem, agora.UseLogger)
	addSubLogger(agoradb.Subsystem, agoradb.UseLogger)
	addSubLogger("LNDC", lndclient.UseLogger)
	addSubLogger("SGNL", signal.UseLogger)
	addSubLogger(account.Subsystem, account.UseLogger)
	addSubLogger(auth.Subsystem, auth.UseLogger)
}

// addSubLogger is a helper method to conveniently create and register the
// logger of a sub system.
func addSubLogger(subsystem string, useLogger func(btclog.Logger)) {
	logger := build.NewSubLogger(subsystem, logWriter.GenSubLogger)
	setSubLogger(subsystem, logger, useLogger)
}

// setSubLogger is a helper method to conveniently register the logger of a sub
// system.
func setSubLogger(subsystem string, logger btclog.Logger,
	useLogger func(btclog.Logger)) {

	logWriter.RegisterSubLogger(subsystem, logger)
	if useLogger != nil {
		useLogger(logger)
	}
}
