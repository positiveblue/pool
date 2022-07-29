ALTER TABLE order_bid ADD unannounced_channel BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE order_ask(
        nonce BYTEA REFERENCES orders(nonce) ON DELETE RESTRICT PRIMARY KEY,
        channel_announcement_constraints SMALLINT NOT NULL DEFAULT 0
);

