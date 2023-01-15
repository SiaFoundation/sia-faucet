package sqlite

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.sia.tech/faucet/faucet"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
	"lukechampine.com/frand"
)

type (
	// A faucetTxn is a SQL transaction that modifies the faucet state.
	faucetTxn struct {
		tx txn
	}

	// A FaucetStore gets and sets the current state of a faucet.
	FaucetStore struct {
		db *Store
	}
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
	if err != nil {
		return faucet.Request{}, fmt.Errorf("failed to scan request: %v", err)
	}
	return
}

// ApplyTransactionBlock marks all faucet requests in a transaction as confirmed
func (ft *faucetTxn) ApplyTransactionBlock(transactionID types.TransactionID, blockID types.BlockID) error {
	_, err := ft.tx.Exec(`UPDATE faucet_requests SET block_id=$1, request_status=$2 WHERE transaction_id=$3`, valueHash(blockID), faucet.RequestStatusConfirmed, valueHash(transactionID))
	return err
}

// RevertBlock marks a faucet request for the siacoin output as not confirmed
func (ft *faucetTxn) RevertBlock(blockID types.BlockID) error {
	_, err := ft.tx.Exec(`UPDATE faucet_requests SET block_id=NULL, request_status=$1 WHERE block_id=$2`, faucet.RequestStatusBroadcast, valueHash(blockID))
	return err
}

// SetLastChange sets the last consensus change id
func (ft *faucetTxn) SetLastChange(cc modules.ConsensusChangeID) error {
	_, err := ft.tx.Exec(`INSERT INTO faucet_settings (last_processed_change) VALUES($1) ON CONFLICT (id) DO UPDATE SET last_processed_change=excluded.last_processed_change`, valueHash(cc))
	return err
}

// Update atomically updates the store
func (fs *FaucetStore) Update(fn func(faucet.UpdateTx) error) error {
	return fs.db.exclusiveTransaction(func(tx txn) error {
		return fn(&faucetTxn{tx})
	})
}

// GetLastChange gets the last processed consensus change.
func (fs *FaucetStore) GetLastChange() (id modules.ConsensusChangeID, err error) {
	err = fs.db.db.QueryRow(`SELECT last_processed_change FROM faucet_settings`).Scan(scanHash((*[32]byte)(&id)))
	if errors.Is(err, sql.ErrNoRows) {
		return modules.ConsensusChangeBeginning, nil
	}
	return
}

// Request returns the request with the given id
func (fs *FaucetStore) Request(id faucet.RequestID) (req faucet.Request, err error) {
	row := fs.db.db.QueryRow(`SELECT id, unlock_hash, ip_address, amount, request_status, transaction_id, block_id, date_created 
FROM faucet_requests WHERE id=$1`, valueHash(id))
	return scanRequest(row)
}

// AddRequest adds a new pending payment request to the store
func (fs *FaucetStore) AddRequest(address types.UnlockHash, ipAddress string, amount types.Currency) (faucet.RequestID, error) {
	id := faucet.RequestID(frand.Entropy256())
	_, err := fs.db.db.Exec(`INSERT INTO faucet_requests (id, ip_address, unlock_hash, amount, request_status, date_created) VALUES($1, $2, $3, $4, $5, $6)`, valueHash(id), ipAddress, valueHash(address), valueCurrency(amount), faucet.RequestStatusPending, valueTime(time.Now()))
	return id, err
}

// ProcessRequests updates the transaction id for the requests and
// marks them as "broadcast"
func (fs *FaucetStore) ProcessRequests(requests []faucet.RequestID, transactionID types.TransactionID) error {
	requestIDs := make([]string, len(requests))
	for i, id := range requests {
		requestIDs[i] = `'` + hex.EncodeToString(id[:]) + `'`
	}
	// sqlite3 cannot parameterize the IN clause -- add the WHERE manually
	query := fmt.Sprintf(`UPDATE faucet_requests SET transaction_id=$1, request_status=$2 WHERE id IN (%s);`, strings.Join(requestIDs, ","))
	_, err := fs.db.db.Exec(query, valueHash(transactionID), faucet.RequestStatusBroadcast)
	return err
}

// AmountRequested returns the sum of all requests for the given address and
// ip address in the last 24 hours.
func (fs *FaucetStore) AmountRequested(address types.UnlockHash, ipAddress string) (types.Currency, error) {
	minTime := time.Now().Add(-24 * time.Hour).Unix()
	rows, err := fs.db.db.Query(`SELECT id, amount, date_created FROM faucet_requests WHERE (unlock_hash=$1 OR ip_address=$2) AND date_created > $3`, valueHash(address), ipAddress, minTime)
	if err != nil {
		return types.ZeroCurrency, fmt.Errorf("failed to get amount requested: %w", err)
	}
	defer rows.Close()

	var total types.Currency
	for rows.Next() {
		var id faucet.RequestID
		var amount types.Currency
		var timestamp int64
		if err := rows.Scan(scanHash((*[32]byte)(&id)), scanCurrency(&amount), &timestamp); err != nil {
			return types.ZeroCurrency, fmt.Errorf("failed to scan amount requested: %w", err)
		}
		total = total.Add(amount)
	}
	return total, nil
}

// UnprocessedRequests returns the first n unprocessed requests
func (fs *FaucetStore) UnprocessedRequests(limit uint64) ([]faucet.Request, error) {
	rows, err := fs.db.db.Query(`SELECT id, unlock_hash, ip_address, amount, request_status, transaction_id, block_id, date_created 
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

// NewFaucetStore creates a new faucet store
func NewFaucetStore(db *Store) *FaucetStore {
	return &FaucetStore{db}
}
