/*
* Orders rows match the data of an order seen as the server sees it.
* That means that some client side fields are missing:
*     - preimage
*     - multisig_key descriptor
*/
CREATE TABLE orders (
        nonce BYTEA PRIMARY KEY,
        type SMALLINT NOT NULL,
        
        trader_key BYTEA NOT NULL REFERENCES accounts(trader_key) 
                ON DELETE RESTRICT,

        version BIGINT NOT NULL,
        state SMALLINT NOT NULL,

        fixed_rate BIGINT NOT NULL,
        amount BIGINT NOT NULL,

        units BIGINT NOT NULL,
        units_unfulfilled BIGINT NOT NULL,
        min_units_match BIGINT NOT NULL,

        max_batch_fee_rate BIGINT NOT NULL,
        lease_duration BIGINT NOT NULL,

        channel_type SMALLINT NOT NULL,

        signature BYTEA NOT NULL,

        multisig_key BYTEA NOT NULL,

        node_key BYTEA NOT NULL,

        token_id BYTEA NOT NULL,

        user_agent TEXT NOT NULL,

        archived BOOLEAN NOT NULL
);
CREATE INDEX idx_orders_trader_key ON orders(trader_key);
CREATE INDEX idx_orders_archived ON orders(archived);

CREATE TABLE order_allowed_node_ids(
        nonce BYTEA REFERENCES orders(nonce) ON DELETE RESTRICT,
        node_key BYTEA NOT NULL,
        allowed BOOLEAN NOT NULL
);
CREATE INDEX idx_order_allowed_node_ids_nonce ON order_allowed_node_ids(nonce);

CREATE TABLE order_node_network_addresses(
        nonce BYTEA REFERENCES orders(nonce) ON DELETE RESTRICT,
        network TEXT NOT NULL,
        address TEXT NOT NULL
);
CREATE INDEX idx_order_node_network_addresses ON order_node_network_addresses(nonce);

CREATE TABLE order_bid(
        nonce BYTEA REFERENCES orders(nonce) ON DELETE RESTRICT PRIMARY KEY,
        min_node_tier BIGINT NOT NULL,
        self_chan_balance BIGINT NOT NULL,
        is_sidecar BOOLEAN NOT NULL
);
