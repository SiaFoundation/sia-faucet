package sqlite

import (
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"go.sia.tech/siad/types"
)

type (
	sqlCurrency types.Currency
	sqlHash     [32]byte
	sqlTime     time.Time

	sqlNullable[T sql.Scanner] struct {
		Value T
		Valid bool
	}
)

func (sn *sqlNullable[T]) Scan(src interface{}) error {
	if src == nil {
		return nil
	} else if err := sn.Value.Scan(src); err != nil {
		return err
	}
	sn.Valid = true
	return nil
}

// Scan implements the sql.Scanner interface.
func (sc *sqlCurrency) Scan(src interface{}) error {
	var i big.Int
	var ok bool
	switch src := src.(type) {
	case []byte:
		_, ok = i.SetString(string(src), 10)
	case string:
		_, ok = i.SetString(src, 10)
	default:
		return fmt.Errorf("cannot scan %T to Currency", src)
	}
	if !ok {
		return fmt.Errorf("failed to scan %v to Currency", src)
	}
	*sc = (sqlCurrency)(types.NewCurrency(&i))
	return nil
}

// Value implements the driver.Valuer interface.
func (sc sqlCurrency) Value() (driver.Value, error) {
	return types.Currency(sc).String(), nil
}

// Scan implements the sql.Scanner interface.
func (sh *sqlHash) Scan(src interface{}) error {
	n, err := hex.Decode(sh[:], []byte(src.(string)))
	if err != nil {
		return fmt.Errorf("failed to decode hex: %w", err)
	} else if n != len(sh) {
		return fmt.Errorf("expected %d bytes, got %d", len(sh), n)
	}
	return nil
}

// Value implements the driver.Valuer interface.
func (sh sqlHash) Value() (driver.Value, error) {
	return hex.EncodeToString(sh[:]), nil
}

func (st *sqlTime) Scan(src interface{}) error {
	switch src := src.(type) {
	case int64:
		*st = sqlTime(time.Unix(src, 0))
		return nil
	default:
		return fmt.Errorf("cannot scan %T to Time", src)
	}
}

func (st sqlTime) Value() (driver.Value, error) {
	return time.Time(st).Unix(), nil
}

func scanCurrency(c *types.Currency) *sqlCurrency {
	return (*sqlCurrency)(c)
}

func valueCurrency(c types.Currency) sqlCurrency {
	return (sqlCurrency)(c)
}

func scanHash(h *[32]byte) *sqlHash {
	return (*sqlHash)(h)
}

func valueHash(h [32]byte) sqlHash {
	return (sqlHash)(h)
}

func scanTime(t *time.Time) *sqlTime {
	return (*sqlTime)(t)
}

func valueTime(t time.Time) sqlTime {
	return (sqlTime)(t)
}

func newSqlNullable[T sql.Scanner](v T) *sqlNullable[T] {
	return &sqlNullable[T]{Value: v}
}
