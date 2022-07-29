//go:build !regtest
// +build !regtest

package monitoring

import (
	"context"
	"encoding/hex"
	"strconv"
	"sync"

	"github.com/lightninglabs/subasta/account"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// accountCollectorName is the name of the MetricGroup for the
	// accountCollector.
	accountCollectorName = "account"

	// accountCount is a gauge that keeps track of the total number of all
	// accounts there are.
	accountCount = "account_count"

	// accountBalance is a gauge that keeps track of the total balance of
	// all accounts there are.
	accountBalance = "account_balance"

	// accountTxFeePaidBlock is a gauge of the on-chain fees that were paid
	// for account open/modify transactions in each block. This will also
	// count batch transactions if an account was used. So the values have
	// to be interpreted carefully.
	accountTxFeePaidBlock = "account_tx_fee_paid_block"

	labelAccountState   = "acct_state"
	labelAccountVersion = "acct_version"
	labelLsat           = "lsat"
	labelUserAgent      = "user_agent"
	labelAccountKey     = "acct_key"
	labelBlock          = "block_height"
)

// accountCollector is a collector that keeps track of our accounts.
type accountCollector struct {
	collectMx sync.Mutex

	cfg *PrometheusConfig

	g gauges
}

// newAccountCollector makes a new accountCollector instance.
func newAccountCollector(cfg *PrometheusConfig) *accountCollector {
	baseLabels := []string{
		labelAccountState, labelAccountVersion, labelLsat,
		labelUserAgent,
	}
	g := make(gauges)
	g.addGauge(
		accountCount, "total number of accounts", baseLabels,
	)
	g.addGauge(
		accountBalance, "total number of satoshis in accounts",
		baseLabels,
	)
	g.addGauge(
		accountTxFeePaidBlock, "total number of fees paid on chain "+
			"for account modifications, including batch "+
			"transactions",
		append(baseLabels, labelAccountKey, labelBlock),
	)
	return &accountCollector{
		cfg: cfg,
		g:   g,
	}
}

// Name is the name of the metric group. When exported to prometheus, it's
// expected that all metric under this group have the same prefix.
//
// NOTE: Part of the MetricGroup interface.
func (c *accountCollector) Name() string {
	return accountCollectorName
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector to the provided channel and returns once the
// last descriptor has been sent.
//
// NOTE: Part of the prometheus.Collector interface.
func (c *accountCollector) Describe(ch chan<- *prometheus.Desc) {
	c.collectMx.Lock()
	defer c.collectMx.Unlock()

	c.g.describe(ch)
}

// Collect is called by the Prometheus registry when collecting metrics.
//
// NOTE: Part of the prometheus.Collector interface.
func (c *accountCollector) Collect(ch chan<- prometheus.Metric) {
	c.collectMx.Lock()
	defer c.collectMx.Unlock()

	// We must reset our metrics that we collect from the DB here, otherwise
	// they would increment with each run.
	c.resetGauges()

	// Next, we'll fetch all our accounts so we can export our series of
	// gauges.
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	accounts, err := c.cfg.Store.Accounts(ctx)
	if err != nil {
		log.Errorf("unable to get accounts: %v", err)
		return
	}

	// Record all metrics for each account.
	for _, acct := range accounts {
		c.observeAccount(acct)
	}

	// Finally, collect the metrics into the prometheus collect channel.
	c.g.collect(ch)
}

// observeAccount fetches the details of a chain TX from the chain backend
// and adds them to the account metrics.
func (c *accountCollector) observeAccount(acct *account.Account) {
	userAgent := "<none>"
	if len(acct.UserAgent) > 0 {
		userAgent = acct.UserAgent
	}

	l := prometheus.Labels{
		labelAccountState:   acct.State.String(),
		labelAccountVersion: acct.Version.String(),
		labelLsat:           acct.TokenID.String(),
		labelUserAgent:      userAgent,
	}
	c.g[accountCount].With(l).Inc()
	c.g[accountBalance].With(l).Add(float64(acct.Value))

	// Query the latest account outpoint on chain.
	details, err := c.cfg.BitcoinClient.GetFullTxDetails(acct.OutPoint.Hash)
	if err != nil {
		log.Errorf("chain tx details: %v", err)
		return
	}

	// Don't track unconfirmed transactions.
	if details.BlockHeight == nil {
		return
	}
	c.g[accountTxFeePaidBlock].With(prometheus.Labels{
		labelAccountState:   acct.State.String(),
		labelAccountVersion: acct.Version.String(),
		labelLsat:           acct.TokenID.String(),
		labelUserAgent:      userAgent,
		labelAccountKey:     hex.EncodeToString(acct.TraderKeyRaw[:]),
		labelBlock:          strconv.Itoa(int(*details.BlockHeight)),
	}).Add(float64(details.TxFee))
}

// resetGauges resets all gauges and adds some default values for nicer graphs.
func (c *accountCollector) resetGauges() {
	c.g.reset()

	// To make prometheus reset values nicely, we can't just remove them.
	// We need to add a baseline for all account states.
	for i := account.StatePendingOpen; i <= account.StateExpiredPendingUpdate; i++ {
		l := prometheus.Labels{
			labelAccountState:   i.String(),
			labelAccountVersion: "",
			labelLsat:           "",
			labelUserAgent:      "<none>",
		}
		c.g[accountCount].With(l).Set(0)
		c.g[accountBalance].With(l).Set(0)
	}
}

// RegisterMetricFuncs signals to the underlying hybrid collector that it
// should register all metrics that it aims to export with the global
// Prometheus registry. Rather than using the series of "MustRegister"
// directives, implementers of this interface should instead propagate back any
// errors related to metric registration.
//
// NOTE: Part of the MetricGroup interface.
func (c *accountCollector) RegisterMetricFuncs() error {
	return prometheus.Register(c)
}

// A compile time flag to ensure the accountCollector satisfies the MetricGroup
// interface.
var _ MetricGroup = (*accountCollector)(nil)

func init() {
	metricsMtx.Lock()
	metricGroups[accountCollectorName] = func(cfg *PrometheusConfig) (
		MetricGroup, error) {

		return newAccountCollector(cfg), nil
	}
	metricsMtx.Unlock()
}
