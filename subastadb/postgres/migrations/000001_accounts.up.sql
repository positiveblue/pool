CREATE TABLE auctioneer_account (
        balance BIGINT NOT NULL,
        batch_key BYTEA NOT NULL,
        is_pending BOOLEAN NOT NULL,

        auctioneer_key_family BIGINT NOT NULL,
        auctioneer_key_index BIGINT NOT NULL,
        auctioneer_public_key BYTEA NOT NULL,

        out_point_hash BYTEA NOT NULL,
        out_point_index BIGINT NOT NULL,

        PRIMARY KEY(auctioneer_public_key, auctioneer_key_family)
);

CREATE TABLE account_reservations (
        trader_key BYTEA PRIMARY KEY,

        value BIGINT NOT NULL,
        auctioneer_key_family BIGINT NOT NULL,
        auctioneer_key_index BIGINT NOT NULL,
        auctioneer_public_key BYTEA NOT NULL,

        initial_batch_key BYTEA NOT NULL,

        expiry BIGINT NOT NULL,
        height_hint BIGINT NOT NULL,

        token_id BYTEA NOT NULL UNIQUE
);
CREATE INDEX idx_account_reservations_token_id ON account_reservations(token_id);

CREATE TABLE accounts (
        trader_key BYTEA PRIMARY KEY,

        token_id BYTEA NOT NULL, 
        value BIGINT NOT NULL,
        expiry BIGINT NOT NULL,

        auctioneer_key_family BIGINT NOT NULL,
        auctioneer_key_index BIGINT NOT NULL,
        auctioneer_public_key BYTEA NOT NULL,

        batch_key BYTEA NOT NULL,
        secret BYTEA NOT NULL,
        state SMALLINT NOT NULL,
        height_hint BIGINT NOT NULL,

        out_point_hash BYTEA NOT NULL,
        out_point_index BIGINT NOT NULL,

        latest_tx BYTEA,

        user_agent TEXT NOT NULL
);
CREATE INDEX idx_account_token_id ON accounts(token_id);

CREATE TABLE account_diffs (
        id BIGSERIAL PRIMARY KEY,
        trader_key BYTEA NOT NULL REFERENCES accounts(trader_key)
                ON DELETE RESTRICT,

        confirmed BOOLEAN NOT NULL,

        token_id BYTEA NOT NULL, 
        value BIGINT NOT NULL,
        expiry BIGINT NOT NULL,

        auctioneer_key_family BIGINT NOT NULL,
        auctioneer_key_index BIGINT NOT NULL,
        auctioneer_public_key BYTEA NOT NULL,

        batch_key BYTEA NOT NULL,
        secret BYTEA NOT NULL,
        state SMALLINT NOT NULL,
        height_hint BIGINT NOT NULL,

        out_point_hash BYTEA NOT NULL,
        out_point_index BIGINT NOT NULL,

        latest_tx BYTEA,

        user_agent TEXT NOT NULL
);
CREATE INDEX idx_account_diffs_trader_key ON account_diffs(trader_key);
CREATE INDEX idx_account_diffs_confirmed ON account_diffs(confirmed);
