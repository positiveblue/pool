ALTER TABLE account_reservations DROP IF EXISTS COLUMN version;
ALTER TABLE accounts DROP IF EXISTS COLUMN version;
ALTER TABLE account_diffs DROP IF EXISTS COLUMN version;
