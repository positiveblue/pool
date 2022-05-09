--- Node ban Queries ---

-- name: CreateNodeBan :exec
INSERT INTO node_bans(
        disabled, node_key, expiry_height, duration) 
VALUES ($1, $2, $3, $4);

-- name: GetNodeBan :one
SELECT * 
FROM node_bans 
WHERE node_key=$1 AND expiry_height > $2 AND NOT disabled;

-- name: GetAllNodeBans :many
SELECT * 
FROM node_bans;

-- name: GetAllActiveNodeBans :many
SELECT * 
FROM node_bans
WHERE expiry_height > $1 AND NOT disabled;

-- name: GetAllNodeBansByNodeKey :many
SELECT * 
FROM node_bans
WHERE node_key=$1;

-- name: UpdateNodeBan :exec
UPDATE node_bans SET 
        disabled=$2, expiry_height=$3, duration=$4
WHERE id=$1;

-- name: DisableNodeBan :execrows
UPDATE node_bans 
SET disabled=TRUE
WHERE node_key=$1;


--- Account ban Queries ---

-- name: CreateAccountBan :exec
INSERT INTO account_bans(
        disabled, trader_key, expiry_height, duration)
VALUES ($1, $2, $3, $4);

-- name: GetAccountBan :one
SELECT * 
FROM account_bans
WHERE trader_key=$1 AND expiry_height > $2 AND NOT disabled;

-- name: GetAllAccountBans :many
SELECT * 
FROM account_bans;

-- name: GetAllActiveAccountBans :many
SELECT * 
FROM account_bans
WHERE expiry_height > $1 AND NOT disabled;

-- name: GetAllAccountBansByTraderKey :many
SELECT * 
FROM account_bans
WHERE trader_key=$1;

-- name: UpdateAccountBan :exec
UPDATE account_bans SET 
        disabled=$2, expiry_height=$3, duration=$4
WHERE id=$1;

-- name: DisableAccountBan :execrows
UPDATE account_bans 
SET disabled=TRUE
WHERE trader_key=$1;
