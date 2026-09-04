/*
	When changing the schema, the version must be incremented at the bottom of
	this file and a migration added to migrations.go
*/

CREATE TABLE faucet_requests (
	id TEXT PRIMARY KEY,
	ip_address TEXT NOT NULL,
	unlock_hash TEXT NOT NULL,
	amount TEXT NOT NULL,
	request_status TEXT NOT NULL,
	block_id TEXT, -- set when the transaction is confirmed
	transaction_id TEXT, -- set when the transaction is broadcast
	date_created UNSIGNED BIG INT NOT NULL
);
CREATE INDEX faucet_requests_unlock_hash_ip ON faucet_requests (unlock_hash, ip_address);

CREATE TABLE wallet_siacoin_elements (
	id BLOB PRIMARY KEY,
	raw_data BLOB NOT NULL
);

CREATE TABLE wallet_events (
	id BLOB PRIMARY KEY,
	chain_index BLOB NOT NULL,
	maturity_height INTEGER NOT NULL,
	raw_data BLOB NOT NULL
);
CREATE INDEX wallet_events_chain_index ON wallet_events (chain_index);
CREATE INDEX wallet_events_maturity_height ON wallet_events (maturity_height DESC);

CREATE TABLE wallet_broadcasted_txnsets (
	id BLOB PRIMARY KEY,
	basis BLOB NOT NULL,
	raw_transactions BLOB NOT NULL,
	date_created INTEGER NOT NULL
);

CREATE TABLE global_settings (
	id INT PRIMARY KEY NOT NULL DEFAULT 0 CHECK (id = 0), -- enforce a single row
	db_version UNSIGNED BIG INT NOT NULL DEFAULT 0, -- used for migrations
	last_scanned_index BLOB -- chain index of the last scanned block
);

INSERT INTO global_settings (db_version) VALUES (2); -- version must be updated when the schema changes
