package venue

import (
	"bytes"
	"context"
	"fmt"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/agora/account"
	"github.com/lightninglabs/agora/agoradb"
	"github.com/lightninglabs/agora/client/clmscript"
	orderT "github.com/lightninglabs/agora/client/order"
	"github.com/lightninglabs/agora/order"
)

// ExeBatchStorer is a type that implements BatchStorer and can persist a batch
// to the etcd database.
type ExeBatchStorer struct {
	store agoradb.Store
}

// NewExeBatchStorer returns a new instance of the ExeBatchStorer given an
// initialized database.
func NewExeBatchStorer(store agoradb.Store) *ExeBatchStorer {
	return &ExeBatchStorer{
		store: store,
	}
}

// Store transforms the execution result of a batch into order and account
// modifications that are then passed to the store to be persisted.
//
// NOTE: This method is part of the BatchStorer interface.
func (s *ExeBatchStorer) Store(ctx context.Context, result *ExecutionResult) error {
	batch := result.Batch
	batchTxHash := result.BatchTx.TxHash()

	// We need to calculate the number of units filled for each order first.
	uniqueOrders := make(map[orderT.Nonce]order.ServerOrder)
	unitsFilled := make(map[orderT.Nonce]orderT.SupplyUnit)
	for _, matchedOrder := range batch.Orders {
		// Orders can appear in multiple matches. Put them all in a map
		// to track the filled units and to flatten them into a list of
		// unique orders later. The references to the actual orders are
		// all to the database state _before_ the match happened. Orders
		// with the same nonce should therefore be identical and all
		// represent the same pre-batch state.
		ask := matchedOrder.Details.Ask
		_, ok := uniqueOrders[ask.Nonce()]
		if !ok {
			uniqueOrders[ask.Nonce()] = ask
			unitsFilled[ask.Nonce()] = 0
		}
		bid := matchedOrder.Details.Bid
		_, ok = uniqueOrders[bid.Nonce()]
		if !ok {
			uniqueOrders[bid.Nonce()] = bid
			unitsFilled[bid.Nonce()] = 0
		}

		// Just sum up the units filled. We need to trust the matching
		// algorithm here that everything's correct and no order was
		// matched more units than it actually offered.
		matched := matchedOrder.Details.Quote.UnitsMatched
		unitsFilled[ask.Nonce()] += matched
		unitsFilled[bid.Nonce()] += matched
	}

	// Now that we know the unique involved orders and their filled units,
	// we can prepare the actual order modifications.
	orders := make([]orderT.Nonce, len(uniqueOrders))
	orderModifiers := make([][]order.Modifier, len(orders))
	orderIndex := 0
	for nonce, matchedOrder := range uniqueOrders {
		orders[orderIndex] = matchedOrder.Nonce()

		// Find out if the order has unfulfilled units left or not.
		// The order details themselves are pointing to the order state
		// as it was stored in the database before the batch was
		// executed. Therefore UnitsFulfilled should always be greater
		// than zero.
		unitsUnfulfilled := matchedOrder.Details().UnitsUnfulfilled
		unitsUnfulfilled -= unitsFilled[nonce]
		switch {
		// The order has been fully filled and can be archived.
		case unitsUnfulfilled == 0:
			orderModifiers[orderIndex] = []order.Modifier{
				order.StateModifier(orderT.StateExecuted),
				order.UnitsFulfilledModifier(0),
			}

		// Some units were not yet filled.
		default:
			orderModifiers[orderIndex] = []order.Modifier{
				order.StateModifier(orderT.StatePartiallyFilled),
				order.UnitsFulfilledModifier(unitsUnfulfilled),
			}
		}

		orderIndex++
	}

	// Next create our account modifiers.
	accounts := make([]*btcec.PublicKey, len(batch.FeeReport.AccountDiffs))
	accountModifiers := make([][]account.Modifier, len(accounts))
	accountIndex := 0
	for _, diff := range batch.FeeReport.AccountDiffs {
		// Get the current state of the account first so we can create
		// a proper diff.
		acctKey, err := btcec.ParsePubKey(
			diff.StartingState.AccountKey[:], btcec.S256(),
		)
		if err != nil {
			return fmt.Errorf("error parsing account key: %v", err)
		}
		accounts[accountIndex] = acctKey
		var modifiers []account.Modifier

		// Determine the new state of the account and set the on-chain
		// attributes accordingly.
		switch account.EndingState(diff.EndingBalance) {
		// The account output has been recreated and needs to wait to be
		// confirmed again.
		case account.OnChainStateRecreated:
			// Find the index of the re-created output in the final
			// batch transaction.
			outpointIndex := -1
			for idx, out := range result.BatchTx.TxOut {
				sameScript := bytes.Equal(
					out.PkScript,
					diff.RecreatedOutput.PkScript,
				)
				if sameScript {
					outpointIndex = idx
				}
			}
			if outpointIndex == -1 {
				return fmt.Errorf("recreated account output "+
					"not found for account %x",
					diff.StartingState.AccountKey)
			}

			modifiers = append(
				modifiers,
				account.StateModifier(account.StatePendingUpdate),
				account.OutPointModifier(wire.OutPoint{
					Index: uint32(outpointIndex),
					Hash:  batchTxHash,
				}),
				account.IncrementBatchKey(),
			)

		// The account was fully spent on-chain. We need to wait for the
		// batch (spend) TX to be confirmed still.
		case account.OnChainStateFullySpent:
			modifiers = append(
				modifiers,
				account.StateModifier(account.StateClosed),
			)

		default:
			return fmt.Errorf("invalid ending account state %d",
				account.EndingState(diff.EndingBalance))
		}

		// Finally update the account value and expiry.
		accountModifiers[accountIndex] = append(
			modifiers, account.ValueModifier(diff.EndingBalance),
		)
		accountIndex++
	}

	// The last thing we need is to update the master account.
	auctAcct, err := s.store.FetchAuctioneerAccount(ctx)
	if err != nil {
		return err
	}
	auctAcct.OutPoint = *result.MasterAccountDiff.OutPoint
	auctAcct.Balance = result.MasterAccountDiff.AccountBalance

	// Parse the current per-batch key (=BatchID) and increment it by the
	// curve's base point to get the next one. We'll store the new/next
	// batch key in the end if everything else was successful.
	batchKey, err := btcec.ParsePubKey(result.BatchID[:], btcec.S256())
	if err != nil {
		return fmt.Errorf("error parsing batch ID: %v", err)
	}
	nextBatchKey := clmscript.IncrementKey(batchKey)

	// Everything is ready to be persisted now.
	return s.store.PersistBatchResult(
		ctx, orders, orderModifiers, accounts, accountModifiers,
		auctAcct, result.BatchID, batch, nextBatchKey,
		result.BatchTx,
	)
}
