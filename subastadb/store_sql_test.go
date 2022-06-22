//go:build sql
// +build sql

package subastadb

import (
	"context"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (AdminStore, func()) {
	t.Helper()

	sqlFixture := NewTestPgFixture(t, time.Minute)

	ctxb := context.Background()
	store, err := NewSQLStore(ctxb, sqlFixture.GetConfig())
	if err != nil {
		t.Fatalf("unable to create sql store: %v", err)
	}
	if err := store.RunMigrations(ctxb); err != nil {
		t.Fatalf("unable to run migrations for sql store: %v", err)
	}
	if err := store.Init(ctxb); err != nil {
		t.Fatalf("unable to initialize sql store: %v", err)
	}

	return store, func() {
		sqlFixture.TearDown(t)
	}
}
