package accounting

import (
	"context"
	"time"
)

// Report contains all the financial data needed by accounting for our pool
// services in a given period of time.
type Report struct {
	// Start is the time from which our report will be created, inclusive.
	Start time.Time

	// End is the time until which our report will be created, exclusive.
	End time.Time

	// BatchEntries contain the information of every transaction included in
	// the report.
	BatchEntries []*BatchEntry

	// LSATEntries contain the information of every LSAT token sold.
	LSATEntries []*LSATEntry
}

// CreateReport creates an accounting report for a given period of time.
func CreateReport(cfg *Config) (*Report, error) {
	ctx := context.Background()

	batches, err := cfg.GetBatches(ctx)
	if err != nil {
		return nil, err
	}

	report := &Report{
		Start:        cfg.Start,
		End:          cfg.End,
		BatchEntries: make([]*BatchEntry, 0, len(batches)),
		LSATEntries:  make([]*LSATEntry, 0),
	}

	// Populate batch entries
	for batchID, snapshot := range batches {
		batchEntry, err := extractBatchEntry(cfg, batchID, snapshot)
		if err != nil {
			log.Info(err)
			return nil, err
		}
		report.BatchEntries = append(report.BatchEntries, batchEntry)
	}

	invoices, err := getLSATInvoices(ctx, cfg)
	if err != nil {
		log.Info(err)
		return nil, err
	}

	// Populate LSAT entries.
	for _, invoice := range invoices {
		lsatEntry, err := extractLSATEntry(cfg, invoice)
		if err != nil {
			log.Info(err)
			return nil, err
		}
		report.LSATEntries = append(report.LSATEntries, lsatEntry)
	}

	return report, nil
}
