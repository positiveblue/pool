--- Order Queries ---

-- name: UpsertOrder :exec
INSERT INTO orders(
        nonce, type, trader_key, version, state, fixed_rate, amount, units,
        units_unfulfilled, min_units_match, max_batch_fee_rate, lease_duration,
        channel_type, signature, multisig_key, node_key, token_id, user_agent,
        archived, created_at, archived_at, is_public)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
        $17, $18, $19, $20, $21, $22)
ON CONFLICT (nonce)
DO UPDATE SET
        type=$2, trader_key=$3, version=$4, state=$5, fixed_rate=$6, amount=$7,
        units=$8, units_unfulfilled=$9, min_units_match=$10, 
        max_batch_fee_rate=$11, lease_duration=$12, channel_type=$13, 
        signature=$14, multisig_key=$15, node_key=$16, token_id=$17, 
        user_agent=$18, archived=$19, created_at=$20, archived_at=$21, 
        is_public=$22;

-- name: GetOrders :many
SELECT * 
FROM orders o LEFT JOIN order_bid ob ON o.nonce = ob.nonce
        LEFT JOIN order_ask oa ON o.nonce = oa.nonce
WHERE o.nonce = ANY(@nonces::BYTEA[]);

-- name: GetOrdersCount :one
SELECT COUNT(*)
FROM orders
WHERE archived = $1;

-- name: GetOrderNonces :many
SELECT o.nonce
FROM orders o
WHERE archived = @archived
ORDER BY nonce
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;

-- name: GetOrderNoncesByTraderKey :many
SELECT nonce, archived 
FROM orders o
WHERE trader_key = @trader_key
ORDER BY nonce
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;

-- name: DeleteOrder :execrows
DELETE 
FROM orders
WHERE nonce=$1;


--- Order allowed node ids Queries ---

-- name: CreateOrderAllowedNodeIds :copyfrom
INSERT INTO order_allowed_node_ids(
        nonce, node_key, allowed) 
VALUES ($1, $2, $3);

-- name: GetOrderAllowedNodeIds :many
SELECT * 
FROM order_allowed_node_ids
WHERE nonce = ANY(@nonces::BYTEA[]);

-- name: DeleteOrderAllowedNodeIds :execrows
DELETE 
FROM order_allowed_node_ids
WHERE nonce = $1;


--- Order network addresses Queries ---

-- name: CreateOrderNetworkAddress :copyfrom
INSERT INTO order_node_network_addresses(
        nonce, network, address) 
VALUES ($1, $2, $3);

-- name: GetOrderNetworkAddresses :many
SELECT * 
FROM order_node_network_addresses
WHERE nonce = ANY(@nonces::BYTEA[]);

-- name: DeleteOrderNetworkAddresses :execrows
DELETE 
FROM order_node_network_addresses
WHERE nonce=$1;


--- Order bid Queries ---

-- name: CreateOrderBid :exec
INSERT INTO order_bid(
        nonce, min_node_tier, self_chan_balance, is_sidecar, 
        unannounced_channel, zero_conf_channel
) VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetOrderBid :one
SELECT * 
FROM order_bid 
WHERE nonce=$1;

-- name: DeleteOrderBid :execrows
DELETE
FROM order_bid
WHERE nonce=$1;


--- Order ask Queries ---

-- name: CreateOrderAsk :exec
INSERT INTO order_ask(
        nonce, channel_announcement_constraints, 
        channel_confirmation_constraints
) VALUES ($1, $2, $3);

-- name: GetOrderAsk :one
SELECT * 
FROM order_ask 
WHERE nonce=$1;

-- name: DeleteOrderAsk :execrows
DELETE
FROM order_ask
WHERE nonce=$1;
