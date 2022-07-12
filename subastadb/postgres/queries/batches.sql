--- Current Batch key queries ---

-- name: GetCurrentBatchKey :one
SELECT *
FROM current_batch_key;

-- name: UpsertCurrentBatchKey :exec
INSERT INTO current_batch_key(id, batch_key, updated_at)
VALUES ($1, $2, CURRENT_TIMESTAMP)
ON CONFLICT (id)
DO UPDATE SET batch_key=$2, updated_at=CURRENT_TIMESTAMP;


--- Batch Queries ---

-- name: UpsertBatch :exec
INSERT INTO batches(
        batch_key, batch_tx, batch_tx_fee, version, auctioneer_fees_accrued,
        confirmed, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (batch_key)
DO UPDATE SET
        batch_tx=$2, batch_tx_fee=$3, version=$4, auctioneer_fees_accrued=$5;

-- name: GetBatch :one
SELECT * 
FROM batches 
WHERE batch_key = $1;

-- name: GetBatchKeys :many
SELECT batch_key 
FROM batches 
ORDER BY id
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;

-- name: ConfirmBatch :execrows
UPDATE batches
SET confirmed=TRUE
WHERE batch_key = $1;

-- name: IsBatchConfirmed :one
SELECT confirmed
FROM batches
WHERE batch_key = $1;

-- name: GetBatchesCount :one
SELECT COUNT(*)
FROM batches;

-- name: GetBatches :many
SELECT *
FROM batches
ORDER BY id
LIMIT $1 OFFSET $2;


--- Matched Order Queries ---

-- name: CreateMatchedOrder :copyfrom
INSERT INTO batch_matched_orders(
        batch_key, ask_order_nonce, bid_order_nonce, lease_duration,
        matching_rate, total_sats_cleared, units_matched, units_unmatched,
        ask_units_unmatched, bid_units_unmatched, fulfill_type, ask_state, 
        bid_state, asker_expiry, bidder_expiry)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: GetAllMatchedOrdersByBatchID :many
SELECT * 
FROM batch_matched_orders
WHERE batch_key=$1;

-- name: GetMatchedOrdersCount :one
SELECT COUNT(*)
FROM batch_matched_orders;

-- name: GetMatchedOrders :many
SELECT * 
FROM batch_matched_orders
ORDER BY batch_key
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;


--- Batch Account Diff Queries ---

-- name: CreateBatchAccountDiff :copyfrom
INSERT INTO batch_account_diffs(
        batch_key, trader_key, trader_batch_key, trader_next_batch_key,
        secret, total_execution_fees_paid, total_taker_fees_paid, 
        total_maker_fees_accrued, num_chans_created, starting_balance, 
        starting_account_expiry, starting_out_point_hash,
        starting_out_point_index, ending_balance, new_account_expiry,
        tx_out_value, tx_out_pkscript
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
        $17);

-- name: GetBatchAccountDiffs :many
SELECT *
FROM batch_account_diffs
WHERE batch_key=$1;

-- name: GetBatchAccountDiffsMatchingBatchIDs :many
SELECT * 
FROM batch_account_diffs
WHERE batch_key = ANY($1::BYTEA[]);

--- Clearing Price Queries ---

-- name: CreateClearingPrice :copyfrom
INSERT INTO batch_clearing_prices(
        batch_key, lease_duration, fixed_rate_premium
) VALUES ($1, $2, $3);

-- name: GetBatchClearingPrices :many
SELECT * 
FROM batch_clearing_prices 
WHERE batch_key=$1;

-- name: GetBatchClearingPricesMatchingBatchIDs :many
SELECT * 
FROM batch_clearing_prices
WHERE batch_key = ANY($1::BYTEA[]);
