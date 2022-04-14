package status

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (

	// stopTimeout  is the amount of time that we'll wait before cancelling
	// the context when we stop the status http server.
	stopTimeout = 1 * time.Minute

	// Path template for the liveness endpoint.
	livePath = "%s/health"

	// Path template for the readiness endpoint.
	readyPath = "%s/ready"

	// Path template for the updating status endpoint.
	statusPath = "%s/status"

	// Default port for the status http server.
	defaultPort = 1100
)

// Config contains all of the required information and dependencies of the
// Reporter to carry out its duties.
type Config struct {
	URLPrefix     string `long:"urlprefix" description:"status instance endpoint prefix"`
	Address       string `long:"address" description:"status instance rest address"`
	DefaultStatus string `long:"defaultstatus" description:"status instance default status"`

	// IsAlive returns if the status should be considerated true in liveness
	// probes.
	IsAlive func(string) bool

	// IsReady returns if the status should be considerated true in
	// readiness probes.
	IsReady func(string) bool

	// IsValidStatus returns false if we should not consider the given
	// status.
	IsValidStatus func(string) bool
}

// DefaultConfig returns the config for a status server with the default
// values.
func DefaultConfig() *Config {
	return &Config{
		URLPrefix:     "/v1",
		Address:       fmt.Sprintf(":%d", defaultPort),
		DefaultStatus: StartingUp,
		IsAlive:       DefaultIsAlive,
		IsReady:       DefaultIsReady,
		IsValidStatus: DefaultIsValidStatus,
	}
}

// Status represents the status information of a service.
type Status struct {
	// Status is the instance state.
	Status string

	// IsAlive return true if the instance is running properly.
	IsAlive bool

	// IsReady signals that the instance is ready to process requests.
	IsReady bool
}

// Reporter is the responsible to hold and communicate the current
// status for any the service.
type Reporter struct {
	cfg *Config

	Status string

	server *http.Server

	sync.RWMutex
}

// NewReporter createes a new reporter to be used as the status service.
func NewReporter(cfg *Config) *Reporter {
	return &Reporter{
		cfg:    cfg,
		Status: cfg.DefaultStatus,
	}
}

// Start registers the handlers for the status service and starts a new service.
func (r *Reporter) Start() error {
	log.Infof("Starting status server")

	handler := http.NewServeMux()
	liveURL := fmt.Sprintf(livePath, r.cfg.URLPrefix)
	handler.HandleFunc(liveURL, r.LivenessHandler())

	readyURL := fmt.Sprintf(readyPath, r.cfg.URLPrefix)
	handler.HandleFunc(readyURL, r.ReadinessHandler())

	statusURL := fmt.Sprintf(statusPath, r.cfg.URLPrefix)
	handler.HandleFunc(statusURL, r.StatusHandler())

	r.server = &http.Server{
		Addr: r.cfg.Address,
		// Default timeouts.
		WriteTimeout: time.Second * 5,
		ReadTimeout:  time.Second * 5,
		IdleTimeout:  time.Second * 30,
		Handler:      handler,
	}

	log.Infof("Status server listening on %s", r.cfg.Address)

	// Run our server in a goroutine so that it doesn't block.
	go func() {
		if err := r.server.ListenAndServe(); err != nil {
			log.Errorf("Status server error: %v", err)
		}
	}()

	return nil
}

// Stop the status http server.
func (r *Reporter) Stop(ctx context.Context) error {
	// Create a deadline to wait for.
	ctx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()

	return r.server.Shutdown(ctx)
}

// SetStatus updates the current status.
func (r *Reporter) SetStatus(status string) error {
	if !r.cfg.IsValidStatus(status) {
		return fmt.Errorf("unable to set non valid status %v", status)
	}

	r.Lock()
	log.Infof("Updating service status: %s -> %s", r.Status, status)
	r.Status = status
	r.Unlock()

	return nil
}

// GetStatus returns the information about the current status.
func (r *Reporter) GetStatus() *Status {
	r.RLock()
	status := r.Status
	r.RUnlock()

	return &Status{
		Status:  status,
		IsAlive: r.cfg.IsAlive(status),
		IsReady: r.cfg.IsReady(status),
	}
}
