ALTER TABLE order_bid DROP COLUMN IF EXISTS zero_conf_channel;
ALTER TABLE order_ask DROP COLUMN IF EXISTS channel_confirmation_constraints;
