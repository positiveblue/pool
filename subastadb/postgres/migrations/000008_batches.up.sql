CREATE TABLE current_batch_key(
        -- We one only one row in this table so we add a "synthetic" PK to 
        -- select that row during the upserts.
        id BIGSERIAL PRIMARY KEY,
        batch_key BYTEA NOT NULL,
        updated_at TIMESTAMP WITH TIME ZONE
);

CREATE TABLE batches(
        batch_key BYTEA PRIMARY KEY,
        batch_tx BYTEA NOT NULL,
        batch_tx_fee BIGINT NOT NULL,

        version BIGINT NOT NULL,

        auctioneer_fees_accrued BIGINT NOT NULL,
        confirmed BOOLEAN NOT NULL,
        created_at TIMESTAMP WITH TIME ZONE
);

CREATE TABLE batch_matched_orders(
        batch_key BYTEA NOT NULL REFERENCES batches(batch_key)
                ON DELETE RESTRICT,

        ask_order_nonce BYTEA NOT NULL REFERENCES orders(nonce)
                ON DELETE RESTRICT, 
        
        bid_order_nonce BYTEA NOT NULL REFERENCES orders(nonce)
                ON DELETE RESTRICT,

        lease_duration BIGINT NOT NULL,

        matching_rate BIGINT NOT NULL,
        total_sats_cleared BIGINT NOT NULL,
        units_matched BIGINT NOT NULL,
        units_unmatched BIGINT NOT NULL,
        fulfill_type SMALLINT NOT NULL,

        PRIMARY KEY (batch_key, ask_order_nonce, bid_order_nonce, lease_duration)
);

CREATE TABLE batch_account_diffs(
        batch_key BYTEA NOT NULL REFERENCES batches(batch_key) 
                ON DELETE RESTRICT,
        trader_key BYTEA NOT NULL REFERENCES accounts(trader_key) 
                ON DELETE RESTRICT,

        trader_batch_key BYTEA NOT NULL,
        trader_next_batch_key BYTEA NOT NULL,
        secret BYTEA NOT NULL,

        -- Account tally.
        total_execution_fees_paid BIGINT NOT NULL,
        total_taker_fees_paid BIGINT NOT NULL,
        total_maker_fees_accrued BIGINT NOT NULL,
        num_chans_created BIGINT NOT NULL,

        -- Starting account state.
        starting_balance BIGINT NOT NULL,
        starting_account_expiry BIGINT NOT NULL,
        starting_out_point_hash BYTEA NOT NULL,
        starting_out_point_index BIGINT NOT NULL,

        -- Ending account state.
        ending_balance BIGINT NOT NULL,
        -- If this value is 0 means no account extension, like in the go app.
        new_account_expiry BIGINT NOT NULL DEFAULT 0,

        -- Recreated account output in the batch transaction. Can be null if
        -- the account had sufficient balance left for a new on-chain output
        -- and wasn't considered to be dust.
        tx_out_value BIGINT,
        tx_out_pkscript BYTEA,

        PRIMARY KEY (batch_key, trader_key)
);

CREATE TABLE batch_clearing_prices(
        batch_key BYTEA NOT NULL REFERENCES batches(batch_key) 
                ON DELETE RESTRICT,
        lease_duration BIGINT NOT NULL,
        fixed_rate_premium BIGINT NOT NULL,

        PRIMARY KEY (batch_key, lease_duration)
);
