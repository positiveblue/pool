// As this file is very similar in every package, ignore the linter here.
// nolint:dupl
package subasta

import (
	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"
)

const Subsystem = "SRVR"

var (
	logWriter = build.NewRotatingLogWriter()
	log       = build.NewSubLogger(Subsystem, logWriter.GenSubLogger)
	rpcLog    = build.NewSubLogger("RPCS", logWriter.GenSubLogger)

	// SupportedSubsystems is a function that returns a list of all
	// supported logging sub systems.
	SupportedSubsystems = logWriter.SupportedSubsystems
)

func init() {
	setSubLogger(Subsystem, log, nil)
	setSubLogger("RPCS", log, nil)
	addSubLogger(subastadb.Subsystem, subastadb.UseLogger)
	addSubLogger("LNDC", lndclient.UseLogger)
	addSubLogger("SGNL", signal.UseLogger)
	addSubLogger(account.Subsystem, account.UseLogger)
	addSubLogger(order.Subsystem, order.UseLogger)
	addSubLogger(batchtx.Subsystem, batchtx.UseLogger)
	addSubLogger(venue.Subsystem, venue.UseLogger)
	addSubLogger(auth.Subsystem, auth.UseLogger)
	addSubLogger(chanenforcement.Subsystem, chanenforcement.UseLogger)
	addSubLogger(monitoring.Subsystem, monitoring.UseLogger)
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
