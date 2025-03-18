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

CREATE TABLE global_settings (
	id INT PRIMARY KEY NOT NULL DEFAULT 0 CHECK (id = 0), -- enforce a single row
	db_version UNSIGNED BIG INT NOT NULL DEFAULT 0 -- used for migrations
);

INSERT INTO global_settings (db_version) VALUES (1); -- version must be updated when the schema changes