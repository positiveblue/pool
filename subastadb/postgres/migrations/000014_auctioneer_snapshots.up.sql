CREATE TABLE auctioneer_snapshots (
        batch_key BYTEA NOT NULL REFERENCES batches(batch_key) 
                ON DELETE RESTRICT,

        balance BIGINT NOT NULL,
        
        out_point_hash BYTEA NOT NULL,
        out_point_index BIGINT NOT NULL,
        
        version SMALLINT NOT NULL
);

CREATE INDEX idx_batches_created_at ON batches(created_at);

