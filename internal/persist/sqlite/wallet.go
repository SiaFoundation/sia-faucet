package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.sia.tech/faucet/wallet"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

type (
	// A walletTxn is a SQL transaction that modifies the wallet state.
	walletTxn struct {
		tx txn
	}

	// A WalletStore gets and sets the current state of a wallet.
	WalletStore struct {
		db *Store
	}
)

// AddSiacoinElement adds a spendable siacoin output to the wallet.
func (wtx *walletTxn) AddSiacoinElement(utxo wallet.SiacoinElement) error {
	_, err := wtx.tx.Exec(`INSERT INTO wallet_utxos (id, amount, unlock_hash) VALUES (?, ?, ?)`, valueHash(utxo.ID), valueCurrency(utxo.Value), valueHash(utxo.UnlockHash))
	return err
}

// RemoveSiacoinElement removes a spendable siacoin output from the wallet
// either due to a spend or a reorg.
func (wtx *walletTxn) RemoveSiacoinElement(id types.SiacoinOutputID) error {
	_, err := wtx.tx.Exec(`DELETE FROM wallet_utxos WHERE id=?`, valueHash(id))
	return err
}

// SetLastChange sets the last processed consensus change.
func (wtx *walletTxn) SetLastChange(id modules.ConsensusChangeID) error {
	_, err := wtx.tx.Exec(`INSERT INTO wallet_settings (last_processed_change) VALUES(?) ON CONFLICT (ID) DO UPDATE SET last_processed_change=excluded.last_processed_change`, valueHash(id))
	return err
}

// GetLastChange gets the last processed consensus change.
func (ws *WalletStore) GetLastChange() (id modules.ConsensusChangeID, err error) {
	err = ws.db.db.QueryRow(`SELECT last_processed_change FROM wallet_settings`).Scan(scanHash((*[32]byte)(&id)))
	if errors.Is(err, sql.ErrNoRows) {
		return modules.ConsensusChangeBeginning, nil
	}
	return
}

// UnspentSiacoinElements returns the spendable siacoin outputs in the wallet.
func (ws *WalletStore) UnspentSiacoinElements() (utxos []wallet.SiacoinElement, err error) {
	rows, err := ws.db.db.Query(`SELECT id, amount, unlock_hash FROM wallet_utxos`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to query unspent siacoin elements: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var utxo wallet.SiacoinElement
		if err := rows.Scan(scanHash((*[32]byte)(&utxo.ID)), scanCurrency(&utxo.Value), scanHash((*[32]byte)(&utxo.UnlockHash))); err != nil {
			return nil, fmt.Errorf("failed to scan unspent siacoin element: %w", err)
		}
		utxos = append(utxos, utxo)
	}
	return utxos, nil
}

// Update begins an update transaction on the wallet store.
func (ws *WalletStore) Update(ctx context.Context, fn func(wallet.UpdateTransaction) error) error {
	return ws.db.transaction(func(tx txn) error {
		return fn(&walletTxn{tx})
	})
}

// NewWalletStore initializes a new wallet store.
func NewWalletStore(db *Store) *WalletStore {
	return &WalletStore{
		db: db,
	}
}
