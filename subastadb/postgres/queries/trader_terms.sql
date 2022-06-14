--- Trader Terms Queries ---

-- name: UpsertTraderTerms :exec
INSERT INTO tarder_terms(token_id, base_fee, fee_rate) 
VALUES ($1, $2, $3)
ON CONFLICT (token_id) DO UPDATE
SET base_fee=$2, fee_rate=$3;

-- name: GetTraderTermsByTokenID :one
SELECT *
FROM tarder_terms
WHERE token_id=$1;

-- name: GetTraderTerms :many
SELECT *
FROM tarder_terms
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;

-- name: DeleteTraderTerms :execrows
DELETE
FROM tarder_terms
WHERE token_id=$1;
