package accounting

import (
	"context"
	"time"

	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/subastadb"
)

// BatchSnapshotMap is an alias for the type returned by our Store.
type BatchSnapshotMap map[orderT.BatchID]*subastadb.BatchSnapshot

type Config struct {
	// Start is the time from which our report will be created, inclusive.
	Start time.Time

	// End is the time until which our report will be created, exclusive.
	End time.Time

	// GetBatches returns the batches that need to be included in the report.
	GetBatches func(context.Context) (BatchSnapshotMap, error)

	// GetPrice returns the timestamped btc price.
	GetPrice PriceFunc
}
