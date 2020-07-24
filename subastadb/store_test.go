package subastadb

import (
	"context"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg"
	"go.etcd.io/etcd/embed"
)

var (
	netParams = chaincfg.MainNetParams
	keyPrefix = "bitcoin/clm/subasta/" + netParams.Name + "/"
)

// getFreePort returns a random open TCP port.
func getFreePort() int {
	ln, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		panic(err)
	}

	port := ln.Addr().(*net.TCPAddr).Port

	err = ln.Close()
	if err != nil {
		panic(err)
	}

	return port
}

func newTestEtcdStore(t *testing.T) (*EtcdStore, func()) {
	t.Helper()

	tempDir, err := ioutil.TempDir("", "etcd")
	if err != nil {
		t.Fatalf("unable to create temp dir: %v", err)
	}

	cfg := embed.NewConfig()
	cfg.Logger = "zap"
	cfg.Dir = tempDir

	clientURL := fmt.Sprintf("127.0.0.1:%d", getFreePort())
	peerURL := fmt.Sprintf("127.0.0.1:%d", getFreePort())
	cfg.LCUrls = []url.URL{{Host: clientURL}}
	cfg.LPUrls = []url.URL{{Host: peerURL}}

	etcd, err := embed.StartEtcd(cfg)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("unable to start etcd: %v", err)
	}

	select {
	case <-etcd.Server.ReadyNotify():
	case <-time.After(5 * time.Second):
		etcd.Close()
		os.RemoveAll(tempDir)
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
		store.client.Close()
		etcd.Close()
		os.RemoveAll(tempDir)
	}
}

func fromHex(s string) *btcec.PublicKey {
	rawKey, _ := hex.DecodeString(s)
	key, _ := btcec.ParsePubKey(rawKey, btcec.S256())
	return key
}
