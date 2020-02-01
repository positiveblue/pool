package agoradb

import (
	"context"
	"testing"

	"github.com/lightninglabs/agora/client/clmscript"
)

// TestBatchKey tests the different database operations that can be performed on
// the per-batch key.
func TestBatchKey(t *testing.T) {
	ctx := context.Background()
	store, cleanup := newTestEtcdStore(t)
	defer cleanup()

	// The store should be initialized with the initial batch key.
	batchKey, err := store.BatchKey(ctx)
	if err != nil {
		t.Fatalf("unable to retrieve initial batch key: %v", err)
	}
	if !batchKey.IsEqual(initialBatchKey) {
		t.Fatalf("expected initial batch key %x, got %x",
			initialBatchKey.SerializeCompressed(),
			batchKey.SerializeCompressed())
	}

	// Increment the initial batch key by its curve's base point. We should
	// expect to see the new key in the store.
	nextBatchKey, err := store.NextBatchKey(ctx)
	if err != nil {
		t.Fatalf("unable to update current batch key: %v", err)
	}
	storedBatchKey, err := store.BatchKey(ctx)
	if err != nil {
		t.Fatalf("unable to retrieve current batch key: %v", err)
	}
	if !storedBatchKey.IsEqual(nextBatchKey) {
		t.Fatalf("expected updated batch key %x, got %x",
			nextBatchKey.SerializeCompressed(),
			storedBatchKey.SerializeCompressed())
	}

	expectedNextBatchKey := clmscript.IncrementKey(initialBatchKey)
	if !nextBatchKey.IsEqual(expectedNextBatchKey) {
		t.Fatalf("expected updated batch key %x, got %x",
			expectedNextBatchKey.SerializeCompressed(),
			nextBatchKey.SerializeCompressed())
	}
}
