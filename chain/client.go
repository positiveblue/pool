package chain

import (
	"fmt"
	"io/ioutil"
	"os"
	"sync"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
)

// BitcoinConfig defines exported config options for the connection to the
// btcd/bitcoind backend.
type BitcoinConfig struct {
	Host         string `long:"host" description:"bitcoind/btcd instance address"`
	User         string `long:"user" description:"bitcoind/btcd user name"`
	Password     string `long:"password" description:"bitcoind/btcd password"`
	HTTPPostMode bool   `long:"httppostmode" description:"Use HTTP POST mode? bitcoind only supports this mode"`
	UseTLS       bool   `long:"usetls" description:"Use TLS to connect? bitcoind only supports non-TLS connections"`
	TLSPath      string `long:"tlspath" description:"Path to btcd's TLS certificate, if TLS is enabled"`
}

// BitcoinClient is a wrapper around the RPC connection to the chain backend
// and allows transactions to be queried.
type BitcoinClient struct {
	sync.Mutex

	rpcClient     *rpcclient.Client
	txDetailCache map[string]*btcjson.TxRawResult
	blockCache    map[string]*btcjson.GetBlockVerboseResult
}

// GetTxDetail fetches a single transaction from the chain and returns it
// in a format that contains more details, like the block hash it was included
// in for example.
func (c *BitcoinClient) GetTxDetail(txHash *chainhash.Hash) (
	*btcjson.TxRawResult, error) {

	c.Lock()
	cachedTx, ok := c.txDetailCache[txHash.String()]
	c.Unlock()
	if ok {
		return cachedTx, nil
	}

	tx, err := c.rpcClient.GetRawTransactionVerbose(txHash)
	if err != nil {
		return nil, err
	}

	// Do not cache details of txes that are not yet confirmed. Otherwise
	// they will be served from cache later on and never be reported as
	// confirmed.
	if tx.BlockHash != "" {
		c.Lock()
		c.txDetailCache[txHash.String()] = tx
		c.Unlock()
	}

	return tx, nil
}

// GetBlockHeight fetches a block by its hash and returns its height.
func (c *BitcoinClient) GetBlockHeight(blockHash string) (
	int64, error) {

	c.Lock()
	cachedBlock, ok := c.blockCache[blockHash]
	c.Unlock()
	if ok {
		return cachedBlock.Height, nil
	}

	hash, err := chainhash.NewHashFromStr(blockHash)
	if err != nil {
		return 0, err
	}
	block, err := c.rpcClient.GetBlockVerbose(hash)
	if err != nil {
		return 0, err
	}

	c.Lock()
	c.blockCache[blockHash] = block
	c.Unlock()

	return block.Height, nil
}

// GetTxFee calculates the on chain fees for a single transaction.
func (c *BitcoinClient) GetTxFee(tx *wire.MsgTx) (btcutil.Amount, error) {
	inValue := int64(0)
	for _, in := range tx.TxIn {
		op := in.PreviousOutPoint
		inTx, err := c.GetTxDetail(&op.Hash)
		if err != nil {
			return 0, err
		}

		// In the Vout we get from GetTxDetail, the output values are in
		// btc, we need to convert to satoshis!
		satValue, err := btcutil.NewAmount(inTx.Vout[op.Index].Value)
		if err != nil {
			return 0, fmt.Errorf("error converting btc to sat: %v",
				err)
		}
		inValue += int64(satValue)
	}

	outValue := int64(0)
	for _, out := range tx.TxOut {
		outValue += out.Value
	}

	return btcutil.Amount(inValue - outValue), nil
}

// GetTxFeeFromJSON calculates the on chain fees for a set of inputs and outputs
// that come from a transaction fetched with GetTxDetail.
func (c *BitcoinClient) GetTxFeeFromJSON(vin []btcjson.Vin,
	vout []btcjson.Vout) (btcutil.Amount, error) {

	inValue := int64(0)
	for _, in := range vin {
		txHash, err := chainhash.NewHashFromStr(in.Txid)
		if err != nil {
			return 0, err
		}

		inTx, err := c.GetTxDetail(txHash)
		if err != nil {
			return 0, err
		}

		// In the Vout we get from GetTxDetail, the output values are in
		// btc, we need to convert to satoshis!
		satValue, err := btcutil.NewAmount(inTx.Vout[in.Vout].Value)
		if err != nil {
			return 0, fmt.Errorf("error converting btc to sat: %v",
				err)
		}
		inValue += int64(satValue)
	}

	outValue := int64(0)
	for _, out := range vout {
		// In the btcjson.Vout we got as parameters, the output values
		// are in fractions of BTC!
		outAmt, err := btcutil.NewAmount(out.Value)
		if err != nil {
			return 0, err
		}
		outValue += int64(outAmt)
	}

	return btcutil.Amount(inValue - outValue), nil
}

// Shutdown closes the connection to the chain backend and should always be
// called on cleanup.
func (c *BitcoinClient) Shutdown() {
	c.rpcClient.Shutdown()
}

// NewClient opens a new RPC connection to the chain backend.
func NewClient(cfg *BitcoinConfig) (*BitcoinClient, error) {
	var err error
	client := &BitcoinClient{
		txDetailCache: make(map[string]*btcjson.TxRawResult),
		blockCache:    make(map[string]*btcjson.GetBlockVerboseResult),
	}
	client.rpcClient, err = getBitcoinConn(cfg)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func getBitcoinConn(cfg *BitcoinConfig) (*rpcclient.Client, error) {
	// In case we use TLS and a certificate argument is provided, we need to
	// read that file and provide it to the RPC connection as byte slice.
	var rpcCert []byte
	if cfg.UseTLS && cfg.TLSPath != "" {
		certFile, err := os.Open(cfg.TLSPath)
		if err != nil {
			return nil, err
		}
		rpcCert, err = ioutil.ReadAll(certFile)
		if err != nil {
			return nil, err
		}
		if err := certFile.Close(); err != nil {
			return nil, err
		}
	}

	// Connect to the backend with the certs we just loaded.
	connCfg := &rpcclient.ConnConfig{
		Host:         cfg.Host,
		User:         cfg.User,
		Pass:         cfg.Password,
		HTTPPostMode: cfg.HTTPPostMode,
		DisableTLS:   !cfg.UseTLS,
		Certificates: rpcCert,
	}

	// Notice the notification parameter is nil since notifications are
	// not supported in HTTP POST mode.
	return rpcclient.New(connCfg, nil)
}

// TxDetails contains details about a specific chain transaction.
type TxDetails struct {
	// TxRaw is the raw json result as returned from the backend.
	TxRaw *btcjson.TxRawResult

	// TxFee is the miner fee paid for this transaction.
	TxFee btcutil.Amount

	// BlockHeight is the height at which the transaction confirmed. Will be
	// nil for unconfirmed transactions.
	BlockHeight *int64
}

// GetFullTxDetails returns full details about the transaction that is
// referenced by hash.
func (c *BitcoinClient) GetFullTxDetails(txHash chainhash.Hash) (*TxDetails,
	error) {

	txRaw, err := c.GetTxDetail(&txHash)
	if err != nil {
		return nil, fmt.Errorf("unable to find tx %v: %v", txHash, err)
	}

	txFee, err := c.GetTxFeeFromJSON(txRaw.Vin, txRaw.Vout)
	if err != nil {
		return nil, fmt.Errorf("unable to calculate fee for tx %v: %v",
			txHash, err)
	}

	details := &TxDetails{
		TxRaw: txRaw,
		TxFee: txFee,
	}

	// Can only set the block height if the transaction is actually
	// confirmed in a block.
	if txRaw.BlockHash != "" {
		blockHeight, err := c.GetBlockHeight(txRaw.BlockHash)
		if err != nil {
			return nil, fmt.Errorf("unable to find block height "+
				"for tx %v in block %v on chain: %v",
				txHash, txRaw.BlockHash, err)
		}

		details.BlockHeight = &blockHeight
	}

	return details, nil
}
