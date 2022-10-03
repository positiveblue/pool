ALTER TABLE order_bid ADD zero_conf_channel BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE order_ask ADD channel_confirmation_constraints SMALLINT NOT NULL 
        DEFAULT 0;
