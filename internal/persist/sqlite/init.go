package sqlite

import (
	_ "embed"

	"fmt"
)

// init queries are run when the database is first created.
//
//go:embed init.sql
var initDatabase string

func (s *Store) init() error {
	// calculate the expected final database version
	target := uint64(1 + len(migrations))
	return s.transaction(func(tx txn) error {
		// check the current database version and perform any necessary
		// migrations
		version := getDBVersion(tx)
		switch {
		case version == 0:
			if _, err := tx.Exec(initDatabase); err != nil {
				return fmt.Errorf("failed to initialize database: %w", err)
			}
			return nil
		case version == target:
			return nil
		case version > target:
			return fmt.Errorf("database version %v is newer than expected %v", version, target)
		}
		for ; version < target; version++ {
			if err := migrations[version-1](tx); err != nil {
				return fmt.Errorf("failed to migrate database to version %v: %w", version+1, err)
			}
		}
		return setDBVersion(tx, target)
	})
}
