package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/lightninglabs/subasta"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"

	// Blank import to set up profiling HTTP handlers.
	_ "net/http/pprof"
)

type daemonCommand struct {
	cfg *subasta.Config
}

func (x *daemonCommand) Execute(_ []string) error {
	// Hook interceptor for os signals.
	shutdownInterceptor, err := signal.Intercept()
	if err != nil {
		return err
	}

	logWriter := build.NewRotatingLogWriter()
	subasta.SetupLoggers(logWriter, shutdownInterceptor)

	// Special show command to list supported subsystems and exit.
	if x.cfg.DebugLevel == "show" {
		fmt.Printf("Supported subsystems: %v\n",
			logWriter.SupportedSubsystems())
		os.Exit(0)
	}

	// Enable http profiling and Validate profile port number if reqeusted.
	if x.cfg.Profile != "" {
		profilePort, err := strconv.Atoi(x.cfg.Profile)
		if err != nil || profilePort < 1024 || profilePort > 65535 {
			return fmt.Errorf("the profile port must be between " +
				"1024 and 65535")
		}

		go func() {
			listenAddr := net.JoinHostPort("", x.cfg.Profile)
			profileRedirect := http.RedirectHandler("/debug/pprof",
				http.StatusSeeOther)
			http.Handle("/", profileRedirect)
			fmt.Println(http.ListenAndServe(listenAddr, nil))
		}()
	}

	server, err := subasta.NewServer(x.cfg, shutdownInterceptor)
	if err != nil {
		return fmt.Errorf("unable to create server: %v", err)
	}

	if err := server.Start(); err != nil {
		return fmt.Errorf("unable to start server: %v", err)
	}

	// Wait for any external interrupt signal.
	<-shutdownInterceptor.ShutdownChannel()

	return server.Stop()
}
