ALTER TABLE batch_matched_orders DROP COLUMN ask_units_unmatched; 
ALTER TABLE batch_matched_orders DROP COLUMN bid_units_unmatched; 

ALTER TABLE batch_matched_orders DROP COLUMN ask_state; 
ALTER TABLE batch_matched_orders DROP COLUMN bid_state; 

ALTER TABLE batch_matched_orders DROP COLUMN asker_expiry; 
ALTER TABLE batch_matched_orders DROP COLUMN bidder_expiry;

