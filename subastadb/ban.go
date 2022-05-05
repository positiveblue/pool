package subastadb

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/subasta/ban"
	conc "go.etcd.io/etcd/client/v3/concurrency"
)

const (
	// banDir is the directory name under which we'll store all trader ban
	// related information. This needs be prefixed with topLevelDir to
	// obtain the full path.
	banDir = "ban"

	// banAccountdir is a sub-directory of banDir under which we'll store
	// trader accounts ban related information.
	banAccountDir = "account"

	// banNodeDir is a sub-directory of banDir under which we'll store
	// trader node public keys ban related information.
	banNodeDir = "node"

	// numBanKeyParts is the number of parts that a full ban key can be
	// split into when using the / character as delimiter. A full path looks
	// like this:
	// bitcoin/clm/subasta/<network>/ban/{ban_type}/{ban_key}.
	numBanKeyParts = 7
)

// banKeyPath returns the prefix path under which we store a trader's ban info.
//
// The key path is represented as follows:
//	bitcoin/clm/subasta/<network>/ban/{ban_type}
func (s *EtcdStore) banKeyPath(banType string) string {
	parts := []string{banDir, banType}
	return s.getKeyPrefix(strings.Join(parts, keyDelimiter))
}

// banAccountKeyPath returns the full path under which we store a trader's
// account ban info.
//
// The key path is represented as follows:
//	bitcoin/clm/subasta/<network>/ban/account/{account_key}
func (s *EtcdStore) banAccountKeyPath(accountKey *btcec.PublicKey) string {
	accountKeyStr := hex.EncodeToString(accountKey.SerializeCompressed())
	parts := []string{s.banKeyPath(banAccountDir), accountKeyStr}
	return strings.Join(parts, keyDelimiter)
}

// banNodeKeyPath returns the full path under which we store a trader's node
// ban info.
//
// The key path is represented as follows:
//	bitcoin/clm/subasta/<network>/ban/node/{node_key}
func (s *EtcdStore) banNodeKeyPath(nodeKey *btcec.PublicKey) string {
	nodeKeyStr := hex.EncodeToString(nodeKey.SerializeCompressed())
	parts := []string{s.banKeyPath(banNodeDir), nodeKeyStr}
	return strings.Join(parts, keyDelimiter)
}

// BanAccount creates/updates the ban Info for the given accountKey.
func (s *EtcdStore) BanAccount(ctx context.Context, accountKey *btcec.PublicKey,
	banInfo *ban.Info) error {

	if !s.initialized {
		return errNotInitialized
	}
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		return s.banAccount(stm, accountKey, banInfo)
	})
	return err
}

// banAccount attempts creates/updates the ban Info for the given accountKey.
func (s *EtcdStore) banAccount(stm conc.STM, accountKey *btcec.PublicKey,
	banInfo *ban.Info) error {

	// If the account has been banned before, apply a new ban duration
	// double the previous.
	banAccountKeyPath := s.banAccountKeyPath(accountKey)

	var buf bytes.Buffer
	if err := serializeBanInfo(&buf, banInfo); err != nil {
		return err
	}
	stm.Put(banAccountKeyPath, buf.String())

	return nil
}

// GetAccountBan returns the ban Info for the given accountKey.
// Info will be nil if the account is not currently banned.
func (s *EtcdStore) GetAccountBan(ctx context.Context,
	accKey *btcec.PublicKey) (*ban.Info, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	var banInfo *ban.Info
	var err error

	banAccountKeyPath := s.banAccountKeyPath(accKey)
	_, err = s.defaultSTM(ctx, func(stm conc.STM) error {
		v := stm.Get(banAccountKeyPath)
		if len(v) == 0 {
			return nil
		}
		banInfo, err = deserializeBanInfo(strings.NewReader(v))
		return err
	})
	return banInfo, err
}

// BanNode creates/updates the ban Info for the given nodeKey.
func (s *EtcdStore) BanNode(ctx context.Context, nodeKey *btcec.PublicKey,
	banInfo *ban.Info) error {

	if !s.initialized {
		return errNotInitialized
	}

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		return s.banNode(stm, nodeKey, banInfo)
	})
	return err
}

// banNodeKey attempts creates/updates the ban Info for the given nodeKey.
func (s *EtcdStore) banNode(stm conc.STM, nodeKey *btcec.PublicKey,
	banInfo *ban.Info) error {

	// If the node key has been banned before, apply a new ban duration
	// double the previous.
	banNodeKeyPath := s.banNodeKeyPath(nodeKey)

	var buf bytes.Buffer
	if err := serializeBanInfo(&buf, banInfo); err != nil {
		return err
	}
	stm.Put(banNodeKeyPath, buf.String())

	return nil
}

// GetNodeBan returns the ban Info for the given nodeKey.
// Info will be nil if the node is not currently banned.
func (s *EtcdStore) GetNodeBan(ctx context.Context,
	nodeKey *btcec.PublicKey) (*ban.Info, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	var banInfo *ban.Info
	var err error

	banNodeKeyPath := s.banNodeKeyPath(nodeKey)
	_, err = s.defaultSTM(ctx, func(stm conc.STM) error {
		v := stm.Get(banNodeKeyPath)
		if len(v) == 0 {
			return nil
		}
		banInfo, err = deserializeBanInfo(strings.NewReader(v))
		return err
	})
	return banInfo, err
}

// ListBannedAccounts returns a map of all accounts that are currently banned.
// The map key is the account's trader key and the value is the ban info.
func (s *EtcdStore) ListBannedAccounts(
	ctx context.Context) (map[[33]byte]*ban.Info, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	key := s.banKeyPath(banAccountDir)
	resultMap, err := s.getAllValuesByPrefix(ctx, key)
	if err != nil {
		return nil, err
	}
	bannedAccounts := make(map[[33]byte]*ban.Info, len(resultMap))
	for key, value := range resultMap {
		acctKey, err := rawKeyFromBanKey(key)
		if err != nil {
			return nil, err
		}
		ban, err := deserializeBanInfo(bytes.NewReader(value))
		if err != nil {
			return nil, err
		}
		bannedAccounts[acctKey] = ban
	}

	return bannedAccounts, nil
}

// ListBannedNodes returns a map of all nodes that are currently banned.
// The map key is the node's identity pubkey and the value is the ban info.
func (s *EtcdStore) ListBannedNodes(
	ctx context.Context) (map[[33]byte]*ban.Info, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	key := s.banKeyPath(banNodeDir)
	resultMap, err := s.getAllValuesByPrefix(ctx, key)
	if err != nil {
		return nil, err
	}
	bannedNodes := make(map[[33]byte]*ban.Info, len(resultMap))
	for key, value := range resultMap {
		acctKey, err := rawKeyFromBanKey(key)
		if err != nil {
			return nil, err
		}
		ban, err := deserializeBanInfo(bytes.NewReader(value))
		if err != nil {
			return nil, err
		}
		bannedNodes[acctKey] = ban
	}

	return bannedNodes, nil
}

// RemoveAccountBan removes the ban information for a given trader's account
// key. Returns an error if no ban exists.
func (s *EtcdStore) RemoveAccountBan(ctx context.Context,
	acctKey *btcec.PublicKey) error {

	if !s.initialized {
		return errNotInitialized
	}

	key := s.banAccountKeyPath(acctKey)
	_, err := s.client.Delete(ctx, key)
	return err
}

// RemoveNodeBan removes the ban information for a given trader's node identity
// key. Returns an error if no ban exists.
func (s *EtcdStore) RemoveNodeBan(ctx context.Context,
	nodeKey *btcec.PublicKey) error {

	if !s.initialized {
		return errNotInitialized
	}

	key := s.banNodeKeyPath(nodeKey)
	_, err := s.client.Delete(ctx, key)
	return err
}

// SetNodeBanInfo stores or overwrites the ban info for a node.
func (s *EtcdStore) SetNodeBanInfo(ctx context.Context,
	nodeKey *btcec.PublicKey, currentHeight, duration uint32) error {

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		nodeBanInfo := &ban.Info{
			Height:   currentHeight,
			Duration: duration,
		}

		// Store or overwrite the ban info.
		banNodeKeyPath := s.banNodeKeyPath(nodeKey)
		var buf bytes.Buffer
		if err := serializeBanInfo(&buf, nodeBanInfo); err != nil {
			return err
		}
		stm.Put(banNodeKeyPath, buf.String())

		return nil
	})
	return err
}

// SetAccountBanInfo stores or overwrites the ban info for a trader account.
func (s *EtcdStore) SetAccountBanInfo(ctx context.Context,
	accountKey *btcec.PublicKey, currentHeight, duration uint32) error {

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		accountBanInfo := &ban.Info{
			Height:   currentHeight,
			Duration: duration,
		}

		// Store or overwrite the ban info.
		banAccountKeyPath := s.banAccountKeyPath(accountKey)
		var buf bytes.Buffer
		if err := serializeBanInfo(&buf, accountBanInfo); err != nil {
			return err
		}
		stm.Put(banAccountKeyPath, buf.String())

		return nil
	})
	return err
}

// rawKeyFromBanKey parses a whole ban key and tries to extract the pubkey from
// the last part of it. This function also checks that the key has the expected
// length and number of key parts.
func rawKeyFromBanKey(key string) ([33]byte, error) {
	var rawKey [33]byte
	if len(key) == 0 {
		return rawKey, fmt.Errorf("key cannot be empty")
	}
	keyParts := strings.Split(key, keyDelimiter)
	if len(keyParts) != numBanKeyParts {
		return rawKey, fmt.Errorf("invalid ban key: %s", key)
	}
	keyBytes, err := hex.DecodeString(keyParts[len(keyParts)-1])
	if err != nil {
		return rawKey, fmt.Errorf("cannot hex decode key: %v", err)
	}
	pubKey, err := btcec.ParsePubKey(keyBytes)
	if err != nil {
		return rawKey, fmt.Errorf("cannot parse public key: %v", err)
	}
	copy(rawKey[:], pubKey.SerializeCompressed())
	return rawKey, nil
}

func serializeBanInfo(w *bytes.Buffer, info *ban.Info) error {
	return WriteElements(w, info.Height, info.Duration)
}

func deserializeBanInfo(r io.Reader) (*ban.Info, error) {
	var info ban.Info
	if err := ReadElements(r, &info.Height, &info.Duration); err != nil {
		return nil, err
	}
	return &info, nil
}
