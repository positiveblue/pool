package subastadb

import (
	"encoding/hex"
	"net"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg"
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

func fromHex(s string) *btcec.PublicKey {
	rawKey, _ := hex.DecodeString(s)
	key, _ := btcec.ParsePubKey(rawKey)
	return key
}
