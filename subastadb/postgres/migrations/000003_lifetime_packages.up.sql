CREATE TABLE lifetime_packages (
        /* 
        * We use channel_point_string as the primary key. It must match the
        * values in hash and index: `channel_point_hash:channel_point_index`
        */
        channel_point_string VARCHAR PRIMARY KEY,
        
        channel_point_hash BYTEA NOT NULL,
        channel_point_index BIGINT NOT NULL,

        channel_script BYTEA NOT NULL,
        height_hint BIGINT NOT NULL,
        maturity_height BIGINT NOT NULL,
        version SMALLINT NOT NULL,

        ask_account_key BYTEA NOT NULL,
        bid_account_key BYTEA NOT NULL,

        ask_node_key  BYTEA NOT NULL,
        bid_node_key  BYTEA NOT NULL,

        ask_payment_base_point BYTEA NOT NULL,
        bid_payment_base_point BYTEA NOT NULL
);
