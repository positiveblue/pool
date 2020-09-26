// As this file is very similar in every package, ignore the linter here.
// nolint:dupl
package subasta

import (
	"context"

	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/ratings"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/batchtx"
	"github.com/lightninglabs/subasta/venue/matching"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"
	"google.golang.org/grpc"
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
	addSubLogger(matching.Subsystem, matching.UseLogger)
	addSubLogger(auth.Subsystem, auth.UseLogger)
	addSubLogger(chanenforcement.Subsystem, chanenforcement.UseLogger)
	addSubLogger(monitoring.Subsystem, monitoring.UseLogger)
	addSubLogger(ratings.Subsystem, ratings.UseLogger)
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

// errorLogUnaryServerInterceptor is a simple UnaryServerInterceptor that will
// automatically log any errors that occur when serving a client's unary
// request.
func errorLogUnaryServerInterceptor(
	logger btclog.Logger) grpc.UnaryServerInterceptor { // nolint:interfacer

	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (interface{}, error) {

		resp, err := handler(ctx, req)
		if err != nil {
			// TODO(roasbeef): also log request details?
			logger.Errorf("[%v]: %v", info.FullMethod, err)
		}

		return resp, err
	}
}

// errorLogStreamServerInterceptor is a simple StreamServerInterceptor that
// will log any errors that occur while processing a client or server streaming
// RPC.
func errorLogStreamServerInterceptor(
	logger btclog.Logger) grpc.StreamServerInterceptor { // nolint:interfacer

	return func(srv interface{}, ss grpc.ServerStream,
		info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {

		err := handler(srv, ss)
		if err != nil {
			logger.Errorf("[%v]: %v", info.FullMethod, err)
		}

		return err
	}
}
