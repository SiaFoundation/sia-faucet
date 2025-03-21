package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3" // import sqlite3 driver
)

type (
	sqlScanner interface {
		Scan(args ...any) error
	}

	txn interface {
		// Exec executes a query without returning any rows. The args are for
		// any placeholder parameters in the query.
		Exec(query string, args ...any) (sql.Result, error)
		// Prepare creates a prepared statement for later queries or executions.
		// Multiple queries or executions may be run concurrently from the
		// returned statement. The caller must call the statement's Close method
		// when the statement is no longer needed.
		Prepare(query string) (*sql.Stmt, error)
		// Query executes a query that returns rows, typically a SELECT. The
		// args are for any placeholder parameters in the query.
		Query(query string, args ...any) (*sql.Rows, error)
		// QueryRow executes a query that is expected to return at most one row.
		// QueryRow always returns a non-nil value. Errors are deferred until
		// Row's Scan method is called. If the query selects no rows, the *Row's
		// Scan will return ErrNoRows. Otherwise, the *Row's Scan scans the
		// first selected row and discards the rest.
		QueryRow(query string, args ...any) *sql.Row
	}

	// A Store is a persistent store that uses a SQL database as its backend.
	Store struct {
		db *sql.DB
	}
)

// transaction executes a function within a database transaction. If the
// function returns an error, the transaction is rolled back. Otherwise, the
// transaction is committed.
func (s *Store) transaction(fn func(txn) error) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		if err := tx.Rollback(); err != nil {
			return fmt.Errorf("failed to rollback transaction: %w", err)
		}
		return fmt.Errorf("failed to execute transaction: %w", err)
	} else if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// getDBVersion returns the current version of the database.
func getDBVersion(tx txn) (version uint64) {
	const query = `SELECT db_version FROM global_settings;`
	err := tx.QueryRow(query).Scan(&version)
	if err != nil {
		return 0
	}
	return
}

// setDBVersion sets the current version of the database.
func setDBVersion(tx txn, version uint64) error {
	const query = `INSERT INTO global_settings (db_version) VALUES (?) ON CONFLICT (id) DO UPDATE SET db_version=excluded.db_version;`
	_, err := tx.Exec(query, version)
	return err
}

// OpenDatabase creates a new SQLite store and initializes the database. If the
// database does not exist, it is created.
func OpenDatabase(fp string) (*Store, error) {
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%v?_busy_timeout=30000&_journal_mode=WAL&_foreign_keys=true", fp))
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.init(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	return store, nil
}
