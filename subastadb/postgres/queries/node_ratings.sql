--- Node Rating Queries ---

-- name: UpsertNodeRating :exec
INSERT INTO node_ratings(node_key, node_tier) 
VALUES ($1, $2)
ON CONFLICT (node_key) 
DO UPDATE SET node_tier=$2;

-- name: GetNodeRating :one
SELECT *
FROM node_ratings
WHERE node_key = $1;

-- name: GetNodeRatings :many
SELECT *
FROM node_ratings
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;

-- name: DeleteNodeRating :execrows
DELETE
FROM node_ratings
WHERE node_key=$1;
