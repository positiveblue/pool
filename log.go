// As this file is very similar in every package, ignore the linter here.
// nolint:dupl
package subasta

import (
	"context"

	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/faraday/accounting"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/subasta/account"
	"github.com/lightninglabs/subasta/ban"
	"github.com/lightninglabs/subasta/chanenforcement"
	"github.com/lightninglabs/subasta/monitoring"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/ratings"
	"github.com/lightninglabs/subasta/rejects"
	"github.com/lightninglabs/subasta/status"
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
	log       = build.NewSubLogger(Subsystem, nil)
	rpcLog    = build.NewSubLogger("SRPCS", nil)
)

// SetupLoggers initializes all package-global logger variables.
func SetupLoggers(root *build.RotatingLogWriter, intercept signal.Interceptor) {
	genLogger := genSubLogger(root, intercept)

	logWriter = root
	log = build.NewSubLogger(Subsystem, genLogger)
	rpcLog = build.NewSubLogger("SRPCS", genLogger)

	setSubLogger(root, Subsystem, log, nil)
	setSubLogger(root, "SRPCS", rpcLog, nil)
	addSubLogger(root, subastadb.Subsystem, intercept, subastadb.UseLogger)
	addSubLogger(root, "LNDC", intercept, lndclient.UseLogger)
	addSubLogger(root, "SGNL", intercept, signal.UseLogger)
	addSubLogger(root, account.Subsystem, intercept, account.UseLogger)
	addSubLogger(root, order.Subsystem, intercept, order.UseLogger)
	addSubLogger(root, batchtx.Subsystem, intercept, batchtx.UseLogger)
	addSubLogger(root, venue.Subsystem, intercept, venue.UseLogger)
	addSubLogger(root, matching.Subsystem, intercept, matching.UseLogger)
	addSubLogger(root, auth.Subsystem, intercept, auth.UseLogger)
	addSubLogger(
		root, chanenforcement.Subsystem, intercept,
		chanenforcement.UseLogger,
	)
	addSubLogger(root, monitoring.Subsystem, intercept, monitoring.UseLogger)
	addSubLogger(root, ratings.Subsystem, intercept, ratings.UseLogger)
	addSubLogger(root, rejects.Subsystem, intercept, rejects.UseLogger)
	addSubLogger(root, accounting.Subsystem, intercept, accounting.UseLogger)
	addSubLogger(root, status.Subsystem, intercept, status.UseLogger)
	addSubLogger(root, ban.Subsystem, intercept, ban.UseLogger)
}

// genSubLogger creates a logger for a subsystem. We provide an instance of
// a signal.Interceptor to be able to shutdown in the case of a critical error.
func genSubLogger(root *build.RotatingLogWriter,
	interceptor signal.Interceptor) func(string) btclog.Logger {

	// Create a shutdown function which will request shutdown from our
	// interceptor if it is listening.
	shutdown := func() {
		if !interceptor.Listening() {
			return
		}

		interceptor.RequestShutdown()
	}

	// Return a function which will create a sublogger from our root
	// logger without shutdown fn.
	return func(tag string) btclog.Logger {
		return root.GenSubLogger(tag, shutdown)
	}
}

// addSubLogger is a helper method to conveniently create and register the
// logger of a sub system.
func addSubLogger(root *build.RotatingLogWriter, subsystem string,
	interceptor signal.Interceptor, useLogger func(btclog.Logger)) {

	logger := build.NewSubLogger(subsystem, genSubLogger(root, interceptor))
	setSubLogger(root, subsystem, logger, useLogger)
}

// setSubLogger is a helper method to conveniently register the logger of a sub
// system.
func setSubLogger(root *build.RotatingLogWriter, subsystem string,
	logger btclog.Logger, useLogger func(btclog.Logger)) {

	root.RegisterSubLogger(subsystem, logger)
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
