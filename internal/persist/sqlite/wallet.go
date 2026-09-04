package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/wallet"
	"go.sia.tech/faucet/faucet"
)

type updateTx struct {
	tx txn
}

var _ wallet.SingleAddressStore = (*Store)(nil)

// UpdateWalletSiacoinElementProofs updates the proofs of all state elements
// affected by the update. ProofUpdater.UpdateElementProof must be called
// for each state element in the database.
func (ux *updateTx) UpdateWalletSiacoinElementProofs(updater wallet.ProofUpdater) error {
	elements, err := getSiacoinElements(ux.tx)
	if err != nil {
		return fmt.Errorf("failed to get siacoin elements: %w", err)
	}

	stmt, err := ux.tx.Prepare(`UPDATE wallet_siacoin_elements SET raw_data=? WHERE id=?`)
	if err != nil {
		return fmt.Errorf("failed to prepare update statement: %w", err)
	}
	defer stmt.Close()

	for i := range elements {
		updater.UpdateElementProof(&elements[i].StateElement)
		if _, err := stmt.Exec(encode(elements[i]), encode(elements[i].ID)); err != nil {
			return fmt.Errorf("failed to update siacoin element %q: %w", elements[i].ID, err)
		}
	}
	return nil
}

// WalletApplyIndex is called with the chain index that is being applied.
// Any transactions and siacoin elements that were created by the index
// should be added and any siacoin elements that were spent should be
// removed.
func (ux *updateTx) WalletApplyIndex(_ types.ChainIndex, created, spent []types.SiacoinElement, events []wallet.Event, _ time.Time) error {
	if err := deleteSiacoinElements(ux.tx, spent); err != nil {
		return fmt.Errorf("failed to delete siacoin elements: %w", err)
	} else if err := createSiacoinElements(ux.tx, created); err != nil {
		return fmt.Errorf("failed to create siacoin elements: %w", err)
	} else if err := createWalletEvents(ux.tx, events); err != nil {
		return fmt.Errorf("failed to create wallet events: %w", err)
	}
	return nil
}

// WalletRevertIndex is called with the chain index that is being reverted.
// Any transactions that were added by the index should be removed. Any
// siacoin elements that were spent by the index should be recreated.
func (ux *updateTx) WalletRevertIndex(index types.ChainIndex, removed, unspent []types.SiacoinElement, _ time.Time) error {
	if err := deleteSiacoinElements(ux.tx, removed); err != nil {
		return fmt.Errorf("failed to delete siacoin elements: %w", err)
	} else if err := createSiacoinElements(ux.tx, unspent); err != nil {
		return fmt.Errorf("failed to create siacoin elements: %w", err)
	} else if _, err := ux.tx.Exec(`DELETE FROM wallet_events WHERE chain_index=?`, encode(index)); err != nil {
		return fmt.Errorf("failed to delete wallet events: %w", err)
	}
	return nil
}

// SetLastIndex sets the last scanned chain index.
func (ux *updateTx) SetLastIndex(index types.ChainIndex) error {
	_, err := ux.tx.Exec(`UPDATE global_settings SET last_scanned_index=?`, encode(index))
	return err
}

// Tip returns the last scanned chain index.
func (s *Store) Tip() (index types.ChainIndex, err error) {
	err = s.db.QueryRow(`SELECT last_scanned_index FROM global_settings`).Scan(newSqlNullable(decode(&index)))
	return
}

// UnspentSiacoinElements returns the last scanned chain index along with all
// unspent siacoin elements, including immature ones.
func (s *Store) UnspentSiacoinElements() (basis types.ChainIndex, utxos []types.SiacoinElement, err error) {
	err = s.transaction(func(tx txn) error {
		if err := tx.QueryRow(`SELECT last_scanned_index FROM global_settings`).Scan(newSqlNullable(decode(&basis))); err != nil {
			return fmt.Errorf("failed to get last scanned index: %w", err)
		}
		utxos, err = getSiacoinElements(tx)
		return err
	})
	return
}

// WalletEvent returns the event with the given ID. If the event does not
// exist, wallet.ErrEventNotFound is returned.
func (s *Store) WalletEvent(id types.Hash256) (event wallet.Event, err error) {
	err = s.db.QueryRow(`SELECT raw_data FROM wallet_events WHERE id=?`, encode(id)).Scan(decode(&event))
	if errors.Is(err, sql.ErrNoRows) {
		return wallet.Event{}, wallet.ErrEventNotFound
	}
	return
}

// WalletEvents returns a paginated list of events ordered by maturity height,
// descending.
func (s *Store) WalletEvents(offset, limit int) (events []wallet.Event, err error) {
	rows, err := s.db.Query(`SELECT raw_data FROM wallet_events ORDER BY maturity_height DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query wallet events: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var event wallet.Event
		if err := rows.Scan(decode(&event)); err != nil {
			return nil, fmt.Errorf("failed to scan wallet event: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// WalletEventCount returns the total number of events relevant to the wallet.
func (s *Store) WalletEventCount() (count uint64, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*) FROM wallet_events`).Scan(&count)
	return
}

// AddBroadcastedSet adds a set of broadcasted transactions.
func (s *Store) AddBroadcastedSet(set wallet.BroadcastedSet) error {
	_, err := s.db.Exec(`INSERT INTO wallet_broadcasted_txnsets (id, basis, raw_transactions, date_created) VALUES (?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		encode(set.ID()), encode(set.Basis), encodeSlice(set.Transactions), valueTime(set.BroadcastedAt))
	return err
}

// BroadcastedSets returns recently broadcasted sets.
func (s *Store) BroadcastedSets() (sets []wallet.BroadcastedSet, err error) {
	rows, err := s.db.Query(`SELECT basis, raw_transactions, date_created FROM wallet_broadcasted_txnsets ORDER BY date_created DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to query broadcasted sets: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var buf []byte
		var set wallet.BroadcastedSet
		if err := rows.Scan(decode(&set.Basis), &buf, scanTime(&set.BroadcastedAt)); err != nil {
			return nil, fmt.Errorf("failed to scan broadcasted set: %w", err)
		}
		dec := types.NewBufDecoder(buf)
		types.DecodeSlice(dec, &set.Transactions)
		if err := dec.Err(); err != nil {
			return nil, fmt.Errorf("failed to decode broadcasted transactions: %w", err)
		}
		sets = append(sets, set)
	}
	return sets, rows.Err()
}

// RemoveBroadcastedSet removes a set so it's no longer rebroadcasted.
func (s *Store) RemoveBroadcastedSet(set wallet.BroadcastedSet) error {
	_, err := s.db.Exec(`DELETE FROM wallet_broadcasted_txnsets WHERE id=?`, encode(set.ID()))
	return err
}

// ResetChainState removes all wallet state so the chain can be rescanned.
func (s *Store) ResetChainState() error {
	return s.transaction(func(tx txn) error {
		if _, err := tx.Exec(`DELETE FROM wallet_siacoin_elements`); err != nil {
			return fmt.Errorf("failed to delete siacoin elements: %w", err)
		} else if _, err := tx.Exec(`DELETE FROM wallet_events`); err != nil {
			return fmt.Errorf("failed to delete wallet events: %w", err)
		} else if _, err := tx.Exec(`UPDATE global_settings SET last_scanned_index=NULL`); err != nil {
			return fmt.Errorf("failed to reset last scanned index: %w", err)
		}
		return nil
	})
}

// SetCheckpoint sets the chain index scanning will resume from.
func (s *Store) SetCheckpoint(index types.ChainIndex) error {
	_, err := s.db.Exec(`UPDATE global_settings SET last_scanned_index=?`, encode(index))
	return err
}

// UpdateChainState atomically applies chain updates to the wallet state.
func (s *Store) UpdateChainState(fn func(faucet.UpdateTx) error) error {
	return s.transaction(func(tx txn) error {
		return fn(&updateTx{tx: tx})
	})
}

func getSiacoinElements(tx txn) (elements []types.SiacoinElement, err error) {
	rows, err := tx.Query(`SELECT raw_data FROM wallet_siacoin_elements`)
	if err != nil {
		return nil, fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var se types.SiacoinElement
		if err := rows.Scan(decode(&se)); err != nil {
			return nil, fmt.Errorf("failed to scan siacoin element: %w", err)
		}
		elements = append(elements, se)
	}
	return elements, rows.Err()
}

func createSiacoinElements(tx txn, elements []types.SiacoinElement) error {
	if len(elements) == 0 {
		return nil
	}

	stmt, err := tx.Prepare(`INSERT INTO wallet_siacoin_elements (id, raw_data) VALUES (?, ?) ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer stmt.Close()

	for _, se := range elements {
		if _, err := stmt.Exec(encode(se.ID), encode(se)); err != nil {
			return fmt.Errorf("failed to insert siacoin element %q: %w", se.ID, err)
		}
	}
	return nil
}

func deleteSiacoinElements(tx txn, elements []types.SiacoinElement) error {
	if len(elements) == 0 {
		return nil
	}

	stmt, err := tx.Prepare(`DELETE FROM wallet_siacoin_elements WHERE id=?`)
	if err != nil {
		return fmt.Errorf("failed to prepare delete statement: %w", err)
	}
	defer stmt.Close()

	for _, se := range elements {
		if _, err := stmt.Exec(encode(se.ID)); err != nil {
			return fmt.Errorf("failed to delete siacoin element %q: %w", se.ID, err)
		}
	}
	return nil
}

func createWalletEvents(tx txn, events []wallet.Event) error {
	if len(events) == 0 {
		return nil
	}

	stmt, err := tx.Prepare(`INSERT INTO wallet_events (id, chain_index, maturity_height, raw_data) VALUES (?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer stmt.Close()

	for i := range events {
		if _, err := stmt.Exec(encode(events[i].ID), encode(events[i].Index), events[i].MaturityHeight, encode(&events[i])); err != nil {
			return fmt.Errorf("failed to insert wallet event %q: %w", events[i].ID, err)
		}
	}
	return nil
}
