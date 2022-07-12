--- AuctioneerAccount Queries ---

-- name: UpsertAuctioneerAccount :exec
INSERT INTO auctioneer_account(
    balance, batch_key, is_pending, auctioneer_key_family, auctioneer_key_index,
    auctioneer_public_key, out_point_hash, out_point_index)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (auctioneer_public_key, auctioneer_key_family)
DO UPDATE SET balance=$1, batch_key=$2, is_pending=$3, auctioneer_key_index=$5,
    out_point_hash=$7, out_point_index=$8;

-- name: GetAuctioneerAccount :one
SELECT * 
FROM auctioneer_account;

-- name: DeleteAuctioneerAccount :execrows
DELETE FROM auctioneer_account
WHERE auctioneer_public_key=$1 AND auctioneer_key_family=$2;


--- AccountReservation Queries ---

-- name: CreateAccountReservation :exec
INSERT INTO account_reservations(
    trader_key, value, auctioneer_key_family, auctioneer_key_index, 
    auctioneer_public_key, initial_batch_key, expiry, height_hint, token_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetAccountReservationByTraderKey :one
SELECT * 
FROM account_reservations
WHERE trader_key = $1;

-- name: GetAccountReservationByTokenID :one
SELECT * 
FROM account_reservations
WHERE token_id = $1;

-- name: GetAccountReservations :many
SELECT * 
FROM account_reservations
ORDER BY token_id
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;

-- name: DeleteAccountReservation :execrows
DELETE 
FROM account_reservations
WHERE token_id = $1;


--- Account Queries ---

-- name: UpsertAccount :exec
INSERT INTO accounts (
    trader_key, token_id, value, expiry, auctioneer_key_family,
    auctioneer_key_index, auctioneer_public_key, batch_key, secret, state,
    height_hint, out_point_hash, out_point_index, latest_tx, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (trader_key)
DO UPDATE SET token_id=$2, value=$3, expiry=$4, auctioneer_key_family=$5,
    auctioneer_key_index=$6, auctioneer_public_key=$7, batch_key=$8, secret=$9,
    state=$10, height_hint=$11, out_point_hash=$12, out_point_index=$13, 
    latest_tx=$14, user_agent=$15;

-- name: GetAccount :one
SELECT * 
FROM accounts
WHERE trader_key = $1;

-- name: GetAccoutsCount :one
SELECT COUNT(*)
FROM accounts;

-- name: GetAccounts :many
SELECT * 
FROM accounts
ORDER BY trader_key
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;

-- name: DeleteAccount :execrows
DELETE FROM accounts
WHERE trader_key=$1;


--- Account Diff Queries ---

-- name: CreateAccountDiff :exec
INSERT INTO account_diffs (
    trader_key, token_id, confirmed, value, expiry, auctioneer_key_family,
    auctioneer_key_index, auctioneer_public_key, batch_key, secret, state,
    height_hint, out_point_hash, out_point_index, latest_tx, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16);

-- name: GetAccountDiffByID :one
SELECT * 
FROM account_diffs
WHERE id = $1;

-- name: GetNotConfirmedAccountDiff :one
SELECT * 
FROM account_diffs
WHERE trader_key = $1 AND confirmed = FALSE;

-- name: GetAccountDiffsByTraderID :many
SELECT * 
FROM account_diffs
WHERE trader_key = $1;

-- name: GetAccountDiffs :many
SELECT * 
FROM account_diffs
ORDER BY id
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;

-- name: ConfirmAccountDiff :execrows
UPDATE account_diffs SET
    confirmed=TRUE
WHERE trader_key = $1 AND confirmed = FALSE;

-- name: UpdateAccountDiff :exec
UPDATE account_diffs SET 
    trader_key=$2, token_id=$3, confirmed=$4, value=$5, expiry=$6, 
    auctioneer_key_family=$7, auctioneer_key_index=$8, auctioneer_public_key=$9,
    batch_key=$10, secret=$11, state=$12, height_hint=$13, out_point_hash=$14, 
    out_point_index=$15, latest_tx=$16, user_agent=$17
WHERE id=$1;

-- name: DeleteAccountDiff :execrows
DELETE 
FROM account_diffs
WHERE id = $1;
