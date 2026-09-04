package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"go.sia.tech/core/types"
	"lukechampine.com/frand"
)

const initVersion1 = `
CREATE TABLE faucet_requests (
	id TEXT PRIMARY KEY,
	ip_address TEXT NOT NULL,
	unlock_hash TEXT NOT NULL,
	amount TEXT NOT NULL,
	request_status TEXT NOT NULL,
	block_id TEXT,
	transaction_id TEXT,
	date_created UNSIGNED BIG INT NOT NULL
);
CREATE INDEX faucet_requests_unlock_hash_ip ON faucet_requests (unlock_hash, ip_address);

CREATE TABLE global_settings (
	id INT PRIMARY KEY NOT NULL DEFAULT 0 CHECK (id = 0),
	db_version UNSIGNED BIG INT NOT NULL DEFAULT 0
);

INSERT INTO global_settings (db_version) VALUES (1);`

func dbVersion(t *testing.T, db *Store) (version uint64) {
	t.Helper()

	err := db.transaction(func(tx txn) error {
		version = getDBVersion(tx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestInit(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDatabase(fp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if version := dbVersion(t, db); version != uint64(1+len(migrations)) {
		t.Fatalf("expected version %v, got %v", 1+len(migrations), version)
	}
}

func TestMigrateVersion1(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "test.db")
	raw, err := sql.Open("sqlite3", fmt.Sprintf("file:%v?_busy_timeout=30000&_journal_mode=WAL", fp))
	if err != nil {
		t.Fatal(err)
	}
	address := types.Address(frand.Entropy256())
	if _, err := raw.Exec(initVersion1); err != nil {
		t.Fatal(err)
	} else if _, err := raw.Exec(`INSERT INTO faucet_requests (id, ip_address, unlock_hash, amount, request_status, date_created) VALUES ($1, $2, $3, $4, $5, $6)`, valueHash(frand.Entropy256()), "127.0.0.1", valueHash(address), valueCurrency(types.Siacoins(1)), "pending", 1); err != nil {
		t.Fatal(err)
	} else if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenDatabase(fp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if version := dbVersion(t, db); version != uint64(1+len(migrations)) {
		t.Fatalf("expected version %v, got %v", 1+len(migrations), version)
	}

	if tip, err := db.Tip(); err != nil {
		t.Fatal(err)
	} else if tip != (types.ChainIndex{}) {
		t.Fatalf("expected zero tip, got %v", tip)
	}

	if amount, count, err := db.Requests(address, "127.0.0.1"); err != nil {
		t.Fatal(err)
	} else if count != 0 {
		t.Fatalf("expected 0 recent requests, got %d", count)
	} else if !amount.IsZero() {
		t.Fatalf("expected zero recent amount, got %v", amount)
	}

	requests, err := db.UnprocessedRequests(10)
	if err != nil {
		t.Fatal(err)
	} else if len(requests) != 1 {
		t.Fatalf("expected 1 unprocessed request, got %d", len(requests))
	} else if requests[0].UnlockHash != address {
		t.Fatalf("expected address %v, got %v", address, requests[0].UnlockHash)
	}
}
