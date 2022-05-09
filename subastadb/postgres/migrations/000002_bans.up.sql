CREATE TABLE node_bans (
        id BIGSERIAL PRIMARY KEY,
        
        disabled BOOLEAN NOT NULL,

        node_key BYTEA NOT NULL,
        expiry_height BIGINT NOT NULL,
        duration BIGINT NOT NULL
);
CREATE INDEX idx_bans_node_key ON node_bans(node_key);

CREATE TABLE account_bans (
        id BIGSERIAL PRIMARY KEY,

        disabled BOOLEAN NOT NULL,

        trader_key BYTEA NOT NULL,
        expiry_height BIGINT NOT NULL,
        duration BIGINT NOT NULL
);
CREATE INDEX idx_bans_trader_key ON account_bans(trader_key);
 