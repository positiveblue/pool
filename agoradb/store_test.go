package agoradb

import (
	"context"
	"encoding/hex"
	"io/ioutil"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg"
	"go.etcd.io/etcd/embed"
)

const (
	clientHost = "127.0.0.1:9125"
)

var (
	netParams = chaincfg.MainNetParams
	keyPrefix = "bitcoin/clm/agora/" + netParams.Name + "/"
)

func newTestEtcdStore(t *testing.T) (*EtcdStore, func()) {
	t.Helper()

	tempDir, err := ioutil.TempDir("", "etcd")
	if err != nil {
		t.Fatalf("unable to create temp dir: %v", err)
	}

	cfg := embed.NewConfig()
	cfg.Dir = tempDir
	cfg.LCUrls = []url.URL{{Host: clientHost}}
	cfg.LPUrls = []url.URL{{Host: "127.0.0.1:9126"}}

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

	store, err := NewEtcdStore(netParams, clientHost, "user", "pass")
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
