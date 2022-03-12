package subastadb

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
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

	// initialBanDuration is the initial ban duration in blocks of a trader.
	// Any consecutive bans after the initial will have a duration double
	// the previous. The current duration is equivalent to 1 day worth of
	// blocks on average.
	//
	// TODO(wilmer): Tune? Employ different strategy?
	initialBanDuration uint32 = 144

	// numBanKeyParts is the number of parts that a full ban key can be
	// split into when using the / character as delimiter. A full path looks
	// like this:
	// bitcoin/clm/subasta/<network>/ban/{ban_type}/{ban_key}.
	numBanKeyParts = 7
)

// BanInfo serves as a helper struct to store all ban-related information for a
// trader ban.
type BanInfo struct {
	// Height is the height at which the ban begins to apply.
	Height uint32

	// Duration is the number of blocks the ban will last for once applied.
	Duration uint32
}

// Expiration returns the height at which the ban expires.
func (i *BanInfo) Expiration() uint32 {
	return i.Height + i.Duration
}

// ExceedsBanExpiration determines whether the given height exceeds the ban
// expiration height.
func (i *BanInfo) ExceedsBanExpiration(currentHeight uint32) bool {
	return currentHeight >= i.Expiration()
}

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

// BanTrader attempts to ban the account and node public key associated with a
// trader starting from the current height of the chain. The duration of the ban
// will depend on how many times the node has been banned before and grows
// exponentially, otherwise it is 144 blocks.
//
// TODO(wilmer): Blacklist trader forever after a certain number of punishable
// offenses?
func (s *EtcdStore) BanTrader(ctx context.Context, accountKey,
	nodeKey *btcec.PublicKey, currentHeight uint32) error {

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		return s.banTrader(stm, accountKey, nodeKey, currentHeight)
	})
	return err
}

// banTrader attempts to ban the account and node public key associated with a
// trader starting from the current height of the chain. The duration of the ban
// will depend on how many times the node has been banned before and grows
// exponentially, otherwise it is 144 blocks.
func (s *EtcdStore) banTrader(stm conc.STM, accountKey,
	nodeKey *btcec.PublicKey, currentHeight uint32) error {

	if err := s.banAccount(stm, accountKey, currentHeight); err != nil {
		return err
	}
	return s.banNodeKey(stm, nodeKey, currentHeight)
}

// BanAccount attempts to ban the account associated with a trader starting from
// the current height of the chain. The duration of the ban will depend on how
// many times the node has been banned before and grows exponentially, otherwise
// it is 144 blocks.
func (s *EtcdStore) BanAccount(ctx context.Context, accountKey *btcec.PublicKey,
	currentHeight uint32) error {

	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		return s.banAccount(stm, accountKey, currentHeight)
	})
	return err
}

// banAccount attempts to ban the account associated with a trader starting from
// the current height of the chain. The duration of the ban will depend on how
// many times the node has been banned before and grows exponentially, otherwise
// it is 144 blocks.
func (s *EtcdStore) banAccount(stm conc.STM, accountKey *btcec.PublicKey,
	currentHeight uint32) error {

	// We'll start by determining how long we should ban the trader's
	// account for.
	accountBanInfo := &BanInfo{
		Height:   currentHeight,
		Duration: initialBanDuration,
	}

	// If the account has been banned before, apply a new ban duration
	// double the previous.
	banAccountKeyPath := s.banAccountKeyPath(accountKey)
	if v := stm.Get(banAccountKeyPath); len(v) > 0 {
		curBanInfo, err := deserializeBanInfo(strings.NewReader(v))
		if err != nil {
			return err
		}
		accountBanInfo.Duration = curBanInfo.Duration * 2
	}

	var buf bytes.Buffer
	if err := serializeBanInfo(&buf, accountBanInfo); err != nil {
		return err
	}
	stm.Put(banAccountKeyPath, buf.String())

	return nil
}

// banNodeKey attempts to ban the account associated with a trader starting from
// the current height of the chain. The duration of the ban will depend on how
// many times the node has been banned before and grows exponentially, otherwise
// it is 144 blocks.
func (s *EtcdStore) banNodeKey(stm conc.STM, nodeKey *btcec.PublicKey,
	currentHeight uint32) error {

	// We'll start by determining how long we should ban the node key for.
	nodeBanInfo := &BanInfo{
		Height:   currentHeight,
		Duration: initialBanDuration,
	}

	// If the node key has been banned before, apply a new ban duration
	// double the previous.
	banNodeKeyPath := s.banNodeKeyPath(nodeKey)
	if v := stm.Get(banNodeKeyPath); len(v) > 0 {
		banInfo, err := deserializeBanInfo(strings.NewReader(v))
		if err != nil {
			return err
		}
		nodeBanInfo.Duration = banInfo.Duration * 2
	}

	var buf bytes.Buffer
	if err := serializeBanInfo(&buf, nodeBanInfo); err != nil {
		return err
	}
	stm.Put(banNodeKeyPath, buf.String())

	return nil
}

// IsTraderBanned determines whether the trader's account or node is banned at
// the current height.
func (s *EtcdStore) IsTraderBanned(ctx context.Context, accountKey,
	nodeKey [33]byte, currentHeight uint32) (bool, error) {

	nodePubkey, err := btcec.ParsePubKey(nodeKey[:])
	if err != nil {
		return false, err
	}
	accountPubkey, err := btcec.ParsePubKey(accountKey[:])
	if err != nil {
		return false, err
	}

	var banned bool
	_, err = s.defaultSTM(ctx, func(stm conc.STM) error {
		// First, check the trader's account.
		var err error
		banned, _, err = s.isAccountBanned(
			stm, accountPubkey, currentHeight,
		)
		if err != nil {
			return err
		}

		// If it's banned, we don't need to check their node key.
		if banned {
			return nil
		}

		banned, _, err = s.isNodeBanned(stm, nodePubkey, currentHeight)
		return err
	})
	return banned, err
}

// IsAccountBanned determines whether the given account is banned at the current
// height. The ban's expiration height is returned.
func (s *EtcdStore) IsAccountBanned(ctx context.Context,
	accountKey *btcec.PublicKey, currentHeight uint32) (bool, uint32, error) {

	var (
		banned     bool
		expiration uint32
	)
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		var err error
		banned, expiration, err = s.isAccountBanned(
			stm, accountKey, currentHeight,
		)
		return err
	})
	return banned, expiration, err
}

// isAccountBanned determines whether the given account is banned at the current
// height. The ban's expiration height is returned.
func (s *EtcdStore) isAccountBanned(stm conc.STM, accountKey *btcec.PublicKey,
	currentHeight uint32) (bool, uint32, error) {

	v := stm.Get(s.banAccountKeyPath(accountKey))

	// No existing ban information, return.
	if len(v) == 0 {
		return false, 0, nil
	}

	ban, err := deserializeBanInfo(strings.NewReader(v))
	if err != nil {
		return false, 0, err
	}
	return !ban.ExceedsBanExpiration(currentHeight), ban.Expiration(), nil
}

// IsNodeBanned determines whether the given node public key is banned at the
// current height. The ban's expiration height is returned.
func (s *EtcdStore) IsNodeBanned(ctx context.Context, nodeKey *btcec.PublicKey,
	currentHeight uint32) (bool, uint32, error) {

	var (
		banned     bool
		expiration uint32
	)
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		var err error
		banned, expiration, err = s.isNodeBanned(
			stm, nodeKey, currentHeight,
		)
		return err
	})
	return banned, expiration, err
}

// isNodeBanned determines whether the given node public key is banned at the
// current height. The ban's expiration height is returned.
func (s *EtcdStore) isNodeBanned(stm conc.STM, nodeKey *btcec.PublicKey,
	currentHeight uint32) (bool, uint32, error) {

	v := stm.Get(s.banNodeKeyPath(nodeKey))

	// No existing ban information, return.
	if len(v) == 0 {
		return false, 0, nil
	}

	ban, err := deserializeBanInfo(strings.NewReader(v))
	if err != nil {
		return false, 0, err
	}
	return !ban.ExceedsBanExpiration(currentHeight), ban.Expiration(), nil
}

// ListBannedAccounts returns a map of all accounts that are currently banned.
// The map key is the account's trader key and the value is the ban info.
func (s *EtcdStore) ListBannedAccounts(
	ctx context.Context) (map[[33]byte]*BanInfo, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	key := s.banKeyPath(banAccountDir)
	resultMap, err := s.getAllValuesByPrefix(ctx, key)
	if err != nil {
		return nil, err
	}
	bannedAccounts := make(map[[33]byte]*BanInfo, len(resultMap))
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
	ctx context.Context) (map[[33]byte]*BanInfo, error) {

	if !s.initialized {
		return nil, errNotInitialized
	}

	key := s.banKeyPath(banNodeDir)
	resultMap, err := s.getAllValuesByPrefix(ctx, key)
	if err != nil {
		return nil, err
	}
	bannedNodes := make(map[[33]byte]*BanInfo, len(resultMap))
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
		nodeBanInfo := &BanInfo{
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
		accountBanInfo := &BanInfo{
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

func serializeBanInfo(w *bytes.Buffer, info *BanInfo) error {
	return WriteElements(w, info.Height, info.Duration)
}

func deserializeBanInfo(r io.Reader) (*BanInfo, error) {
	var info BanInfo
	if err := ReadElements(r, &info.Height, &info.Duration); err != nil {
		return nil, err
	}
	return &info, nil
}
