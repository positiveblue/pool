//go:build !sql
// +build !sql

package subastadb

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"testing"
	"time"

	"go.etcd.io/etcd/server/v3/embed"
)

func newTestStore(t *testing.T) (AdminStore, func()) {
	t.Helper()

	tempDir, err := ioutil.TempDir("", "etcd")
	if err != nil {
		t.Fatalf("unable to create temp dir: %v", err)
	}

	cfg := embed.NewConfig()
	cfg.Logger = "zap"
	cfg.LogLevel = "error"
	cfg.Dir = tempDir

	clientURL := fmt.Sprintf("127.0.0.1:%d", getFreePort())
	peerURL := fmt.Sprintf("127.0.0.1:%d", getFreePort())
	cfg.LCUrls = []url.URL{{Host: clientURL}}
	cfg.LPUrls = []url.URL{{Host: peerURL}}

	etcd, err := embed.StartEtcd(cfg)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		t.Fatalf("unable to start etcd: %v", err)
	}

	select {
	case <-etcd.Server.ReadyNotify():
	case <-time.After(5 * time.Second):
		etcd.Close()
		_ = os.RemoveAll(tempDir)
		t.Fatal("server took too long to start")
	}

	store, err := NewEtcdStore(netParams, clientURL, "user", "pass")
	if err != nil {
		t.Fatalf("unable to create etcd store: %v", err)
	}
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("unable to initialize etcd store: %v", err)
	}

	return store, func() {
		_ = store.client.Close()
		etcd.Close()
		_ = os.RemoveAll(tempDir)
	}
}
