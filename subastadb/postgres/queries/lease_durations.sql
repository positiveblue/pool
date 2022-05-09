--- Lease duration Queries ---

-- name: UpsertLeaseDuration :exec
INSERT INTO lease_durations(
        duration, state
) VALUES ($1, $2) 
ON CONFLICT (duration)
DO UPDATE SET 
    state=$2;

-- name: GetLeaseDuration :one
SELECT *
FROM lease_durations
WHERE duration=$1;

-- name: GetLeaseDurations :many
SELECT *
FROM lease_durations
ORDER BY duration
LIMIT NULLIF(@limit_param::int, 0) OFFSET @offset_param;

-- name: DeleteLeaseDuration :execrows
DELETE 
FROM lease_durations
WHERE duration=$1;
