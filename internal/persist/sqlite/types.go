package sqlite

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"go.sia.tech/core/types"
)

type (
	sqlCurrency types.Currency
	sqlHash     [32]byte
	sqlTime     time.Time

	sqlNullable[T sql.Scanner] struct {
		Value T
		Valid bool
	}

	decodable struct {
		v types.DecoderFrom
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
	// Currency is encoded as two 64-bit big-endian integers for sorting
	buf, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("expected []byte, got %T", src)
	} else if len(buf) != 16 {
		return fmt.Errorf("expected 16 bytes, got %d", len(buf))
	}
	sc.Hi = binary.BigEndian.Uint64(buf)
	sc.Lo = binary.BigEndian.Uint64(buf[8:])
	return nil
}

// Value implements the driver.Valuer interface.
func (sc sqlCurrency) Value() (driver.Value, error) {
	// Currency is encoded as two 64-bit big-endian integers for sorting
	buf := make([]byte, 16)
	binary.BigEndian.PutUint64(buf, sc.Hi)
	binary.BigEndian.PutUint64(buf[8:], sc.Lo)
	return buf, nil
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

// Scan implements the sql.Scanner interface.
func (d *decodable) Scan(src interface{}) error {
	buf, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("expected []byte, got %T", src)
	}
	dec := types.NewBufDecoder(buf)
	d.v.DecodeFrom(dec)
	return dec.Err()
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

func encode(v types.EncoderTo) []byte {
	var buf bytes.Buffer
	e := types.NewEncoder(&buf)
	v.EncodeTo(e)
	e.Flush()
	return buf.Bytes()
}

func encodeSlice[T types.EncoderTo](v []T) []byte {
	var buf bytes.Buffer
	e := types.NewEncoder(&buf)
	types.EncodeSlice(e, v)
	e.Flush()
	return buf.Bytes()
}

func decode(v types.DecoderFrom) *decodable {
	return &decodable{v: v}
}
