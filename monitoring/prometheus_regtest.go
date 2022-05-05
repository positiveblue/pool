//go:build regtest
// +build regtest

package monitoring

import (
	"context"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/subasta/chain"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue/matching"
	"google.golang.org/grpc"
)

// PrometheusConfig is the set of configuration data that specifies if
// Prometheus metric exporting is activated, and if so the listening address of
// the Prometheus server.
type PrometheusConfig struct {
	// Active, if true, then Prometheus metrics will be expired.
	Active bool

	// ListenAddr is the listening address that we should use to allow the
	// main Prometheus server to scrape our metrics.
	ListenAddr string

	// Store is the active database where all data is stored.
	Store subastadb.Store

	// BitcoinClient holds the connection to the bitcoin chain backend.
	BitcoinClient *chain.BitcoinClient

	// LightningNode is the connection to the backing Lightning node
	// for the subasta server. Only read-only permissions are required.
	Lnd lndclient.LndServices

	// FundingConflicts is a conflict tracker that keeps track of funding
	// conflicts (problems during channel funding) between nodes.
	FundingConflicts matching.NodeConflictTracker

	// AdminRPCServer is a pointer to the admin RPC server. We use this to
	// ensure that we'll export metrics for the admin RPC.
	AdminRPCServer *grpc.Server

	// PublicRPCServer is a pointer to the main RPC server. We use this to
	// export generic RPC metrics to monitor the health of the service.
	PublicRPCServer *grpc.Server

	// NumActiveTraders should return the number of active traders in the
	// venue in a thread-safe manner.
	NumActiveTraders func() int

	// BatchConfTarget should mimic the conf target that is set in the main
	// config. We use it to monitor the estimated batch feerates.
	BatchConfTarget int32

	// SnapshotSource is a function that returns the batch snapshot for a
	// batch with the given batch ID.
	SnapshotSource func(context.Context,
		*btcec.PublicKey) (*subastadb.BatchSnapshot, error)
}

// PrometheusExporter is a metric exporter that uses Prometheus directly. The
// internal subasta server will interact with this struct in order to export
// relevant metrics such as the total number of fees earned from swaps over
// time.
type PrometheusExporter struct {
}

// NewPrometheusExporter makes a new instance of the PrometheusExporter given
// the config.
func NewPrometheusExporter(cfg *PrometheusConfig) *PrometheusExporter {
	return &PrometheusExporter{}
}

// Start registers all relevant metrics with the Prometheus library, then
// launches the HTTP server that Prometheus will hit to scrape our metrics.
func (p *PrometheusExporter) Start() error {
	return nil
}

// CollectAll will call the Collect method on each of the registered collectors
// and block until all of them finish executing. This can be used to prime any
// caches that might be used by the collectors.
func (p *PrometheusExporter) CollectAll() {

}

// ObserveBatchMatchAttempt increments the counter that tracks how often we
// attempt to perform match making for a given batch.
func ObserveBatchMatchAttempt(batchID []byte, marketMade bool) {

}

// ObserveBatchExecutionAttempt increments a counter that tracks how often we
// attempt to execute a batch given a set of valid matches.
func ObserveBatchExecutionAttempt(batchID []byte) {

}

// ObserveMatchingLatency exports a new observation of how long it took to
// perform match making for a given batch.
func ObserveMatchingLatency(batchID []byte, t time.Duration) {

}

// ObserveBatchExecutionLatency exports a new observation of how long it took
// to fully execute a given batch.
func ObserveBatchExecutionLatency(batchID []byte, t time.Duration) {

}

// ObserveBatchExeFailure observes an instance wherein we attempt to execute a
// batch but failed.
func ObserveBatchExeFailure(batchID []byte, reason string, reporter []byte) {

}

// ObserveNewConnection is called to observe an instance of a new trader
// subscribing to batch events.
func ObserveNewConnection(acctKey [33]byte) {

}

// ObserveFailedConnection is called to observe an instance of a trader being
// disconnected from their subscription.
func ObserveFailedConnection(acctKey [33]byte) {

}
