package sqlite

// migrations is a list of functions that are run to migrate the database from
// one version to the next. Migrations are used to update existing databases to
// match the schema in init.sql.
var migrations = []func(tx txn) error{
	migrateVersion2,
}

func migrateVersion2(tx txn) error {
	_, err := tx.Exec(`
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

ALTER TABLE global_settings ADD COLUMN last_scanned_index BLOB;`)
	return err
}
