package sqlite

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/faucet/faucet"
	"lukechampine.com/frand"
)

func scanRequest(row sqlScanner) (req faucet.Request, err error) {
	err = row.Scan(scanHash((*[32]byte)(&req.ID)),
		scanHash((*[32]byte)(&req.UnlockHash)),
		&req.IPAddress,
		scanCurrency(&req.Amount),
		&req.Status,
		newSqlNullable((*sqlHash)(&req.TransactionID)),
		newSqlNullable((*sqlHash)(&req.BlockID)),
		scanTime(&req.Timestamp))
	return
}

// Request returns the request with the given id
func (s *Store) Request(id faucet.RequestID) (faucet.Request, error) {
	row := s.db.QueryRow(`SELECT id, unlock_hash, ip_address, amount, request_status, transaction_id, block_id, date_created 
FROM faucet_requests WHERE id=$1`, valueHash(id))
	req, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return faucet.Request{}, faucet.ErrNotFound
	}
	return req, err
}

// AddRequest adds a new pending payment request to the store
func (s *Store) AddRequest(address types.Address, ipAddress string, amount types.Currency) (faucet.RequestID, error) {
	id := faucet.RequestID(frand.Entropy256())
	_, err := s.db.Exec(`INSERT INTO faucet_requests (id, ip_address, unlock_hash, amount, request_status, date_created) VALUES($1, $2, $3, $4, $5, $6)`, valueHash(id), ipAddress, valueHash(address), valueCurrency(amount), faucet.RequestStatusPending, valueTime(time.Now()))
	return id, err
}

// ProcessRequests updates the transaction id for the requests and
// marks them as "broadcast"
func (s *Store) ProcessRequests(requests []faucet.RequestID, transactionID types.TransactionID) error {
	requestIDs := make([]string, len(requests))
	for i, id := range requests {
		requestIDs[i] = `'` + hex.EncodeToString(id[:]) + `'`
	}
	// sqlite3 cannot parameterize the IN clause -- add the WHERE manually
	query := fmt.Sprintf(`UPDATE faucet_requests SET transaction_id=$1, request_status=$2 WHERE id IN (%s);`, strings.Join(requestIDs, ","))
	_, err := s.db.Exec(query, valueHash(transactionID), faucet.RequestStatusBroadcast)
	return err
}

// Requests returns the sum and count of all requests for the given address and
// ip address in the last 24 hours.
func (s *Store) Requests(address types.Address, ipAddress string) (types.Currency, int, error) {
	minTime := time.Now().Add(-24 * time.Hour).Unix()
	rows, err := s.db.Query(`SELECT amount FROM faucet_requests WHERE (unlock_hash=$1 OR ip_address=$2) AND date_created > $3`, valueHash(address), ipAddress, minTime)
	if err != nil {
		return types.ZeroCurrency, 0, fmt.Errorf("failed to get amount requested: %w", err)
	}
	defer rows.Close()

	var total types.Currency
	var count int
	for rows.Next() {
		var amount types.Currency
		if err := rows.Scan(scanCurrency(&amount)); err != nil {
			return types.ZeroCurrency, 0, fmt.Errorf("failed to scan amount requested: %w", err)
		}
		total = total.Add(amount)
		count++
	}
	return total, count, nil
}

// UnprocessedRequests returns the first n unprocessed requests
func (s *Store) UnprocessedRequests(limit uint64) ([]faucet.Request, error) {
	rows, err := s.db.Query(`SELECT id, unlock_hash, ip_address, amount, request_status, transaction_id, block_id, date_created 
FROM faucet_requests 
WHERE request_status=$1 OR (request_status=$2 AND block_id IS NULL AND date_created < $3)
ORDER BY date_created ASC
LIMIT $3`, faucet.RequestStatusPending, faucet.RequestStatusBroadcast, valueTime(time.Now().Add(-6*time.Hour)), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get unprocessed requests: %w", err)
	}
	defer rows.Close()

	var requests []faucet.Request
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan request: %w", err)
		}
		requests = append(requests, req)
	}
	return requests, nil
}
