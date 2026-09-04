package faucet_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/testutil"
	"go.sia.tech/coreutils/wallet"
	"go.sia.tech/faucet/faucet"
	"go.sia.tech/faucet/internal/persist/sqlite"
	"go.uber.org/zap/zaptest"
)

type testNode struct {
	cm     *chain.Manager
	wallet *wallet.SingleAddressWallet
	faucet *faucet.Faucet
}

func newTestNode(t *testing.T, maxRequestsPerDay int, maxSCPerDay types.Currency) *testNode {
	t.Helper()

	log := zaptest.NewLogger(t)
	store, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "faucetd.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	network, genesis := testutil.V2Network()
	network.BlockInterval = 10 * time.Minute
	dbstore, err := chain.NewDBStore(chain.NewMemDB(), network, genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore)

	wm, err := wallet.NewSingleAddressWallet(types.GeneratePrivateKey(), cm, store, &testutil.MockSyncer{}, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wm.Close() })

	f := faucet.New(store, cm, wm, maxRequestsPerDay, maxSCPerDay, log.Named("faucet"))
	t.Cleanup(func() { f.Close() })
	return &testNode{cm: cm, wallet: wm, faucet: f}
}

func (n *testNode) mineAndSync(t *testing.T, addr types.Address, blocks int) {
	t.Helper()

	testutil.MineBlocks(t, n.cm, addr, blocks)
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if tip, err := n.wallet.Tip(); err != nil {
			t.Fatal(err)
		} else if tip == n.cm.Tip() {
			return
		} else if time.Since(start) > 30*time.Second {
			t.Fatalf("wallet did not sync to %v", n.cm.Tip())
		}
	}
}

func (n *testNode) waitForBroadcast(t *testing.T, id faucet.RequestID) faucet.Request {
	t.Helper()

	for start := time.Now(); ; time.Sleep(100 * time.Millisecond) {
		if req, err := n.faucet.Request(id); err != nil {
			t.Fatal(err)
		} else if req.Status == faucet.RequestStatusBroadcast {
			return req
		} else if time.Since(start) > time.Minute {
			t.Fatalf("request %v was not processed", id)
		}
	}
}

func (n *testNode) totalBalance(t *testing.T) types.Currency {
	t.Helper()

	balance, err := n.wallet.Balance()
	if err != nil {
		t.Fatal(err)
	}
	return balance.Confirmed.Add(balance.Immature)
}

func TestFaucet(t *testing.T) {
	n := newTestNode(t, 5, types.Siacoins(1000))

	// fund the wallet and wait for the payout to mature
	n.mineAndSync(t, n.wallet.Address(), int(n.cm.TipState().MaturityHeight())+1)
	balance, err := n.wallet.Balance()
	if err != nil {
		t.Fatal(err)
	} else if balance.Spendable.IsZero() {
		t.Fatal("expected spendable balance")
	}
	before := n.totalBalance(t)

	recipient := types.Address{1, 2, 3}
	amount := types.Siacoins(10)
	id, err := n.faucet.RequestAmount(recipient, "127.0.0.1", amount)
	if err != nil {
		t.Fatal(err)
	}

	req, err := n.faucet.Request(id)
	if err != nil {
		t.Fatal(err)
	} else if req.Status != faucet.RequestStatusPending {
		t.Fatalf("expected status %q, got %q", faucet.RequestStatusPending, req.Status)
	} else if req.UnlockHash != recipient || !req.Amount.Equals(amount) {
		t.Fatalf("unexpected request %+v", req)
	}

	req = n.waitForBroadcast(t, id)
	if req.TransactionID == (types.TransactionID{}) {
		t.Fatal("expected transaction id")
	}

	// confirm the transaction
	n.mineAndSync(t, types.VoidAddress, 1)
	b, ok := n.cm.Block(n.cm.Tip().ID)
	if !ok {
		t.Fatal("expected tip block")
	}
	var txn types.V2Transaction
	var found bool
	for _, txn = range b.V2Transactions() {
		if found = txn.ID() == req.TransactionID; found {
			break
		}
	}
	if !found {
		t.Fatalf("transaction %v not found in block %v", req.TransactionID, b.ID())
	}

	var paid bool
	for _, sco := range txn.SiacoinOutputs {
		paid = paid || (sco.Address == recipient && sco.Value.Equals(amount))
	}
	if !paid {
		t.Fatalf("transaction %v did not pay %v to %v", txn.ID(), amount, recipient)
	}

	if spent := before.Sub(n.totalBalance(t)); !spent.Equals(amount.Add(txn.MinerFee)) {
		t.Fatalf("expected wallet to spend %v, spent %v", amount.Add(txn.MinerFee), spent)
	}
}

func TestRequestLimits(t *testing.T) {
	n := newTestNode(t, 2, types.Siacoins(100))

	recipient := types.Address{1}
	// amount limit is shared by address and ip
	if _, err := n.faucet.RequestAmount(recipient, "10.0.0.1", types.Siacoins(60)); err != nil {
		t.Fatal(err)
	} else if _, err := n.faucet.RequestAmount(recipient, "10.0.0.2", types.Siacoins(50)); !errors.Is(err, faucet.ErrAmountExceeded) {
		t.Fatalf("expected %v, got %v", faucet.ErrAmountExceeded, err)
	} else if _, err := n.faucet.RequestAmount(types.Address{2}, "10.0.0.1", types.Siacoins(50)); !errors.Is(err, faucet.ErrAmountExceeded) {
		t.Fatalf("expected %v, got %v", faucet.ErrAmountExceeded, err)
	}

	// count limit is shared by address and ip
	if _, err := n.faucet.RequestAmount(recipient, "10.0.0.1", types.Siacoins(10)); err != nil {
		t.Fatal(err)
	} else if _, err := n.faucet.RequestAmount(recipient, "10.0.0.3", types.Siacoins(1)); !errors.Is(err, faucet.ErrCountExceeded) {
		t.Fatalf("expected %v, got %v", faucet.ErrCountExceeded, err)
	} else if _, err := n.faucet.RequestAmount(types.Address{3}, "10.0.0.1", types.Siacoins(1)); !errors.Is(err, faucet.ErrCountExceeded) {
		t.Fatalf("expected %v, got %v", faucet.ErrCountExceeded, err)
	} else if _, err := n.faucet.RequestAmount(types.Address{3}, "10.0.0.3", types.Siacoins(1)); err != nil {
		t.Fatal(err)
	}
}

func TestPrune(t *testing.T) {
	n := newTestNode(t, 5, types.Siacoins(100))

	genesisID := n.cm.Tip().ID
	pruneTarget := int(7 * 24 * time.Hour / n.cm.TipState().Network.BlockInterval)
	n.mineAndSync(t, types.VoidAddress, pruneTarget/2)
	if _, ok := n.cm.Block(genesisID); !ok {
		t.Fatal("expected genesis block to be retained")
	}

	// pruned blocks are only removed from the store at the next commit
	n.mineAndSync(t, types.VoidAddress, pruneTarget/2+1)
	for start := time.Now(); ; time.Sleep(100 * time.Millisecond) {
		n.mineAndSync(t, types.VoidAddress, 1)
		if _, ok := n.cm.Block(genesisID); !ok {
			break
		} else if time.Since(start) > 30*time.Second {
			t.Fatal("expected genesis block to be pruned")
		}
	}

	tip := n.cm.Tip()
	if _, ok := n.cm.Block(tip.ID); !ok {
		t.Fatal("expected tip block to be retained")
	} else if retained, ok := n.cm.BestIndex(tip.Height - uint64(pruneTarget) + 1); !ok {
		t.Fatal("expected best index")
	} else if _, ok := n.cm.Block(retained.ID); !ok {
		t.Fatalf("expected block %v to be retained", retained)
	}
}
