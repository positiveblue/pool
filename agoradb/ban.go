package agoradb

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"strings"

	"github.com/btcsuite/btcd/btcec"
	conc "github.com/coreos/etcd/clientv3/concurrency"
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
)

// banInfo serves as a helper struct to store all ban-related information for a
// trader ban.
type banInfo struct {
	// height is the height at which the ban begins to apply.
	height uint32

	// duration is the number of blocks the ban will last for once applied.
	duration uint32
}

// expiration returns the height at which the ban expires.
func (i *banInfo) expiration() uint32 {
	return i.height + i.duration
}

// exceedsBanExpiration determines whether the given height exceeds the ban
// expiration height.
func (i *banInfo) exceedsBanExpiration(currentHeight uint32) bool {
	return currentHeight >= i.expiration()
}

// banAccountKeyPath returns the full path under which we store a trader's
// account ban info.
//
// The key path is represented as follows:
//	bitcoin/clm/agora/ban/account/{account_key}
func (s *EtcdStore) banAccountKeyPath(accountKey *btcec.PublicKey) string {
	accountKeyStr := hex.EncodeToString(accountKey.SerializeCompressed())
	parts := []string{banDir, banAccountDir, accountKeyStr}
	return s.getKeyPrefix(strings.Join(parts, keyDelimiter))
}

// banNodeKeyPath returns the full path under which we store a trader's node
// ban info.
//
// The key path is represented as follows:
//	bitcoin/clm/agora/ban/node/{node_key}
func (s *EtcdStore) banNodeKeyPath(nodeKey *btcec.PublicKey) string {
	nodeKeyStr := hex.EncodeToString(nodeKey.SerializeCompressed())
	parts := []string{banDir, banNodeDir, nodeKeyStr}
	return s.getKeyPrefix(strings.Join(parts, keyDelimiter))
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

	// We'll start by determining how long we should ban the trader's
	// account and node key for.
	accountBanInfo := &banInfo{
		height:   currentHeight,
		duration: initialBanDuration,
	}

	// If the account has been banned before, apply a new ban duration
	// double the previous.
	banAccountKeyPath := s.banAccountKeyPath(accountKey)
	if v := stm.Get(banAccountKeyPath); len(v) > 0 {
		curBanInfo, err := deserializeBanInfo(strings.NewReader(v))
		if err != nil {
			return err
		}
		accountBanInfo.duration = curBanInfo.duration * 2
	}

	nodeBanInfo := &banInfo{
		height:   currentHeight,
		duration: initialBanDuration,
	}

	// Similarly, if the node key has been banned before, apply a new ban
	// duration double the previous.
	banNodeKeyPath := s.banNodeKeyPath(nodeKey)
	if v := stm.Get(banNodeKeyPath); len(v) > 0 {
		banInfo, err := deserializeBanInfo(strings.NewReader(v))
		if err != nil {
			return err
		}
		nodeBanInfo.duration = banInfo.duration * 2
	}

	// Update the ban details for both the account and node key respectively.
	var buf bytes.Buffer
	if err := serializeBanInfo(&buf, accountBanInfo); err != nil {
		return err
	}
	stm.Put(banAccountKeyPath, buf.String())

	buf.Reset()
	if err := serializeBanInfo(&buf, nodeBanInfo); err != nil {
		return err
	}
	stm.Put(banNodeKeyPath, buf.String())

	return nil
}

// IsTraderBanned determines whether the trader's account or node is banned at
// the current height.
func (s *EtcdStore) IsTraderBanned(ctx context.Context, accountKey,
	nodeKey *btcec.PublicKey, currentHeight uint32) (bool, error) {

	var banned bool
	_, err := s.defaultSTM(ctx, func(stm conc.STM) error {
		// First, check the trader's account.
		var err error
		banned, _, err = s.isAccountBanned(
			stm, accountKey, currentHeight,
		)
		if err != nil {
			return err
		}

		// If it's banned, we don't need to check their node key.
		if banned {
			return nil
		}

		banned, _, err = s.isNodeBanned(stm, nodeKey, currentHeight)
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
	return !ban.exceedsBanExpiration(currentHeight), ban.expiration(), nil
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
	return !ban.exceedsBanExpiration(currentHeight), ban.expiration(), nil
}

func serializeBanInfo(w io.Writer, info *banInfo) error {
	return WriteElements(w, info.height, info.duration)
}

func deserializeBanInfo(r io.Reader) (*banInfo, error) {
	var info banInfo
	if err := ReadElements(r, &info.height, &info.duration); err != nil {
		return nil, err
	}
	return &info, nil
}
