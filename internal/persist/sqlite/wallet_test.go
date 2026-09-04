package sqlite

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/wallet"
	"go.sia.tech/faucet/faucet"
	"lukechampine.com/frand"
)

type proofUpdaterFunc func(*types.StateElement)

func (fn proofUpdaterFunc) UpdateElementProof(se *types.StateElement) { fn(se) }

func randomElement(maturityHeight uint64) types.SiacoinElement {
	return types.SiacoinElement{
		ID: frand.Entropy256(),
		StateElement: types.StateElement{
			LeafIndex:   frand.Uint64n(1 << 40),
			MerkleProof: []types.Hash256{frand.Entropy256(), frand.Entropy256()},
		},
		SiacoinOutput: types.SiacoinOutput{
			Value:   types.Siacoins(uint32(frand.Uint64n(1000) + 1)),
			Address: frand.Entropy256(),
		},
		MaturityHeight: maturityHeight,
	}
}

func randomEvent(index types.ChainIndex) wallet.Event {
	return wallet.Event{
		ID:             frand.Entropy256(),
		Index:          index,
		Type:           wallet.EventTypeV2Transaction,
		Data:           wallet.EventV2Transaction{ArbitraryData: frand.Bytes(10)},
		MaturityHeight: index.Height,
		Timestamp:      time.Now().Truncate(time.Second),
	}
}

func assertUnspent(t *testing.T, db *Store, basis types.ChainIndex, expected ...types.SiacoinElement) {
	t.Helper()

	tip, err := db.Tip()
	if err != nil {
		t.Fatal(err)
	} else if tip != basis {
		t.Fatalf("expected tip %v, got %v", basis, tip)
	}

	gotBasis, utxos, err := db.UnspentSiacoinElements()
	if err != nil {
		t.Fatal(err)
	} else if gotBasis != basis {
		t.Fatalf("expected basis %v, got %v", basis, gotBasis)
	} else if len(utxos) != len(expected) {
		t.Fatalf("expected %d utxos, got %d", len(expected), len(utxos))
	}
	for _, want := range expected {
		var found bool
		for _, got := range utxos {
			if got.ID == want.ID {
				found = true
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("expected utxo %+v, got %+v", want, got)
				}
			}
		}
		if !found {
			t.Fatalf("utxo %v not found", want.ID)
		}
	}
}

func assertEventCount(t *testing.T, db *Store, n uint64) {
	t.Helper()

	count, err := db.WalletEventCount()
	if err != nil {
		t.Fatal(err)
	} else if count != n {
		t.Fatalf("expected %d events, got %d", n, count)
	}

	events, err := db.WalletEvents(0, 100)
	if err != nil {
		t.Fatal(err)
	} else if uint64(len(events)) != n {
		t.Fatalf("expected %d events, got %d", n, len(events))
	}
}

func TestWalletStore(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	assertUnspent(t, db, types.ChainIndex{})
	assertEventCount(t, db, 0)

	index1 := types.ChainIndex{Height: 1, ID: frand.Entropy256()}
	se1 := randomElement(10)
	ev1 := randomEvent(index1)
	err = db.UpdateChainState(func(tx faucet.UpdateTx) error {
		if err := tx.WalletApplyIndex(index1, []types.SiacoinElement{se1}, nil, []wallet.Event{ev1}, time.Now()); err != nil {
			return err
		}
		return tx.SetLastIndex(index1)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertUnspent(t, db, index1, se1)
	assertEventCount(t, db, 1)

	event, err := db.WalletEvent(ev1.ID)
	if err != nil {
		t.Fatal(err)
	} else if event.ID != ev1.ID || event.Index != ev1.Index || event.Type != ev1.Type {
		t.Fatalf("expected event %+v, got %+v", ev1, event)
	} else if !event.Timestamp.Equal(ev1.Timestamp) {
		t.Fatalf("expected timestamp %v, got %v", ev1.Timestamp, event.Timestamp)
	} else if !reflect.DeepEqual(event.Data, ev1.Data) {
		t.Fatalf("expected data %+v, got %+v", ev1.Data, event.Data)
	}

	proof := []types.Hash256{frand.Entropy256(), frand.Entropy256(), frand.Entropy256()}
	err = db.UpdateChainState(func(tx faucet.UpdateTx) error {
		return tx.UpdateWalletSiacoinElementProofs(proofUpdaterFunc(func(se *types.StateElement) {
			se.MerkleProof = proof
		}))
	})
	if err != nil {
		t.Fatal(err)
	}
	se1.StateElement.MerkleProof = proof
	assertUnspent(t, db, index1, se1)

	index2 := types.ChainIndex{Height: 2, ID: frand.Entropy256()}
	se2 := randomElement(20)
	ev2 := randomEvent(index2)
	err = db.UpdateChainState(func(tx faucet.UpdateTx) error {
		if err := tx.WalletApplyIndex(index2, []types.SiacoinElement{se2}, []types.SiacoinElement{se1}, []wallet.Event{ev2}, time.Now()); err != nil {
			return err
		}
		return tx.SetLastIndex(index2)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertUnspent(t, db, index2, se2)
	assertEventCount(t, db, 2)

	err = db.UpdateChainState(func(tx faucet.UpdateTx) error {
		if err := tx.WalletRevertIndex(index2, []types.SiacoinElement{se2}, []types.SiacoinElement{se1}, time.Now()); err != nil {
			return err
		}
		return tx.SetLastIndex(index1)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertUnspent(t, db, index1, se1)
	assertEventCount(t, db, 1)

	// spending an element created before the checkpoint is ignored
	err = db.UpdateChainState(func(tx faucet.UpdateTx) error {
		return tx.WalletApplyIndex(index2, nil, []types.SiacoinElement{se2}, nil, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	assertUnspent(t, db, index1, se1)

	if err := db.ResetChainState(); err != nil {
		t.Fatal(err)
	}
	assertUnspent(t, db, types.ChainIndex{})
	assertEventCount(t, db, 0)

	if err := db.SetCheckpoint(index2); err != nil {
		t.Fatal(err)
	}
	assertUnspent(t, db, index2)
}

func TestBroadcastedSets(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	set := wallet.BroadcastedSet{
		Basis:         types.ChainIndex{Height: 1, ID: frand.Entropy256()},
		BroadcastedAt: time.Now().Truncate(time.Second),
		Transactions: []types.V2Transaction{
			{ArbitraryData: frand.Bytes(10)},
			{ArbitraryData: frand.Bytes(10)},
		},
	}
	if err := db.AddBroadcastedSet(set); err != nil {
		t.Fatal(err)
	} else if err := db.AddBroadcastedSet(set); err != nil {
		t.Fatal(err)
	}

	sets, err := db.BroadcastedSets()
	if err != nil {
		t.Fatal(err)
	} else if len(sets) != 1 {
		t.Fatalf("expected 1 set, got %d", len(sets))
	} else if sets[0].ID() != set.ID() {
		t.Fatalf("expected id %v, got %v", set.ID(), sets[0].ID())
	} else if !reflect.DeepEqual(sets[0].Transactions, set.Transactions) {
		t.Fatalf("expected transactions %+v, got %+v", set.Transactions, sets[0].Transactions)
	}

	if err := db.RemoveBroadcastedSet(sets[0]); err != nil {
		t.Fatal(err)
	}
	sets, err = db.BroadcastedSets()
	if err != nil {
		t.Fatal(err)
	} else if len(sets) != 0 {
		t.Fatalf("expected 0 sets, got %d", len(sets))
	}
}
