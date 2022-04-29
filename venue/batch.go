package venue

import (
	"context"

	orderT "github.com/lightninglabs/pool/order"
)

const (
	// CurrentServerBatchVersion indicates the last batch version that
	// this server implements.
	//
	// NOTE: a sever may support more than one version.
	CurrentServerBatchVersion = orderT.ExtendAccountBatchVersion
)

// BatchStorer is an interface that can store a batch to the local database by
// applying all the diffs to the orders and accounts.
type BatchStorer interface {
	// Store makes sure all changes executed by a batch are correctly and
	// atomically stored to the database.
	Store(context.Context, *ExecutionResult) error
}

// SupportedBatchVersion returns if the server implements a given
// batch version or not.
func SupportedBatchVersion(version orderT.BatchVersion) bool {
	return version <= CurrentServerBatchVersion
}
