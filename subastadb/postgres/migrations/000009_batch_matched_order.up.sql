ALTER TABLE batch_matched_orders ADD ask_units_unmatched BIGINT NOT NULL;
ALTER TABLE batch_matched_orders ADD bid_units_unmatched BIGINT NOT NULL;

ALTER TABLE batch_matched_orders ADD ask_state SMALLINT NOT NULL;
ALTER TABLE batch_matched_orders ADD bid_state SMALLINT NOT NULL;

ALTER TABLE batch_matched_orders ADD asker_expiry BIGINT NOT NULL;
ALTER TABLE batch_matched_orders ADD bidder_expiry BIGINT NOT NULL;

