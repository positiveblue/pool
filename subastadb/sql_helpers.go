package subastadb

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/tor"
)

// addrToString converts a net.Addr to string given that the addr is either
// a net.TCPAddr or a tor.OnionAddr.
func addrToString(addr net.Addr) (string, error) {
	switch a := addr.(type) {
	case *net.TCPAddr:
		return fmt.Sprintf("tcp://%s:%d", a.IP.String(), a.Port), nil

	case *tor.OnionAddr:
		if a == nil {
			return "", fmt.Errorf("nil onion address")
		}

		return fmt.Sprintf("tor://%s:%d", a.OnionService, a.Port), nil
	}

	return "", fmt.Errorf("unknown net.Addr type")
}

// addrFromString converts a string to a net.Addr given that the string was
// encoded with addrToString.
func addrFromString(str string) (net.Addr, error) {
	const prefix = 6

	if strings.HasPrefix(str, "tcp://") {
		rawAddr := str[prefix:]
		parts := strings.Split(rawAddr, ":")
		portStr := parts[len(parts)-1]

		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("unable to parse port")
		}

		ip := net.ParseIP(rawAddr[:len(rawAddr)-len(portStr)-1])
		if ip == nil {
			return nil, fmt.Errorf("unable to parse IP address")
		}

		return &net.TCPAddr{
			IP:   ip,
			Port: port,
		}, nil
	}

	if strings.HasPrefix(str, "tor://") {
		rawAddr := str[prefix:]
		parts := strings.Split(rawAddr, ":")
		portStr := parts[len(parts)-1]

		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("unable to parse port")
		}

		return &tor.OnionAddr{
			OnionService: rawAddr[:len(rawAddr)-len(portStr)-1],
			Port:         port,
		}, nil
	}

	return nil, fmt.Errorf("unable to parse address")
}

// addressesToString converts a slice of net.Addr addresses to a coma separated
// string, given that all passed addresses are either net.TCPAddr or
// tor.OnionAddr.
func addressesToString(addrs []net.Addr) (string, error) {
	strAddrs := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		addrStr, err := addrToString(addr)
		if err != nil {
			return "", err
		}
		strAddrs = append(strAddrs, addrStr)
	}

	return strings.Join(strAddrs, ","), nil
}

// keyFromHexString is a helper to parse a hex encoded 33 byte long key.
func keyFromHexString(keyStr string) ([33]byte, error) {
	var keyArr [33]byte

	key, err := hex.DecodeString(keyStr)
	if err != nil {
		return keyArr, err
	}
	if len(key) != 33 {
		return keyArr, fmt.Errorf("invalid key length")
	}
	copy(keyArr[:], key)
	return keyArr, nil
}

func msgTxToString(tx *wire.MsgTx) (string, error) {
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return "", err
	}

	return hex.EncodeToString(buf.Bytes()), nil
}

func msgTxFromString(txStr string) (*wire.MsgTx, error) {
	if txStr == "" {
		return nil, nil
	}

	rawTx, err := hex.DecodeString(txStr)
	if err != nil {
		return nil, err
	}

	var tx wire.MsgTx
	r := bytes.NewReader(rawTx)
	if err := tx.Deserialize(r); err != nil {
		return nil, err
	}

	return &tx, nil
}

func outpointFromString(outPointStr string) (*wire.OutPoint, error) {
	outpointParts := strings.Split(outPointStr, ":")
	if len(outpointParts) != 2 {
		return nil, fmt.Errorf("invalid outpoint")
	}

	hash, err := chainhash.NewHashFromStr(outpointParts[0])
	if err != nil {
		return nil, err
	}

	outIdx, err := strconv.Atoi(outpointParts[1])
	if err != nil {
		return nil, err
	}

	return wire.NewOutPoint(hash, uint32(outIdx)), nil
}
