--- Lifetime pakages Queries ---

-- name: UpsertLifetimePackage :exec
INSERT INTO lifetime_packages(
        channel_point_string, channel_point_hash, channel_point_index,
        channel_script, height_hint, maturity_height, version, ask_account_key,
        bid_account_key, ask_node_key, bid_node_key, ask_payment_base_point, 
        bid_payment_base_point)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (channel_point_string)
DO UPDATE SET 
        channel_script=$4, height_hint=$5, maturity_height=$6, version=$7,
        ask_account_key=$8, bid_account_key=$9, ask_node_key=$10, 
        bid_node_key=$11, ask_payment_base_point=$12, 
        bid_payment_base_point=$13;

-- name: GetLifetimePackage :one
SELECT * 
FROM lifetime_packages
WHERE channel_point_string = $1;

-- name: GetLifetimePackages :many
SELECT * 
FROM lifetime_packages
ORDER BY channel_point_string
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;

-- name: DeleteLifetimePackage :execrows
DELETE 
FROM lifetime_packages
WHERE channel_point_string = $1;
