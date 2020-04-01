package venue

import (
	"context"
)

// BatchStorer is an interface that can store a batch to the local database by
// applying all the diffs to the orders and accounts.
type BatchStorer interface {
	// Store makes sure all changes executed by a batch are correctly and
	// atomically stored to the database.
	Store(context.Context, *ExecutionResult) error
}
