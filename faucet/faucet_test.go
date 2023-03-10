package faucet_test

import (
	"crypto/ed25519"
	"errors"
	"log"
	"path/filepath"
	"testing"
	"time"

	"go.sia.tech/faucet/faucet"
	"go.sia.tech/faucet/internal/persist/sqlite"
	"go.sia.tech/faucet/internal/test"
	"go.sia.tech/siad/types"
	"lukechampine.com/frand"
)

func TestFaucet(t *testing.T) {
	db, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "faucetd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	node, err := test.NewNode(ed25519.NewKeyFromSeed(frand.Bytes(ed25519.SeedSize)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	// mine enough blocks to fund the wallet
	if err := node.MineBlocks(node.Wallet().Address(), int(types.MaturityDelay)*2); err != nil {
		t.Fatal(err)
	}

	cm := node.ChainManager()
	tp := node.TPool()
	w := node.Wallet()

	node2, err := test.NewNode(ed25519.NewKeyFromSeed(frand.Bytes(ed25519.SeedSize)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer node2.Close()

	// connect the two nodes
	node2.ConnectPeer(node.GatewayAddr())

	f, err := faucet.New(cm, tp, w, sqlite.NewFaucetStore(db), 5, types.SiacoinPrecision.Mul64(10), time.Second, log.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// request too much
	_, err = f.RequestAmount(node2.Wallet().Address(), "10.10.10.10", types.SiacoinPrecision.Mul64(100))
	if !errors.Is(err, faucet.ErrAmountExceeded) {
		t.Fatalf("expected %v, got %v", faucet.ErrAmountExceeded, err)
	}

	// create a new request
	requestID, err := f.RequestAmount(node2.Wallet().Address(), "10.10.10.10", types.SiacoinPrecision)
	if err != nil {
		t.Fatal(err)
	}

	// verify the request was added
	request, err := f.Request(requestID)
	if err != nil {
		t.Fatal(err)
	}

	switch {
	case request.UnlockHash != node2.Wallet().Address():
		t.Fatalf("expected %v, got %v", node2.Wallet().Address(), request.UnlockHash)
	case !request.Amount.Equals(types.SiacoinPrecision):
		t.Fatalf("expected %v, got %v", types.SiacoinPrecision, request.Amount)
	case request.IPAddress != "10.10.10.10":
		t.Fatalf("expected %v, got %v", "10.10.10.10", request.IPAddress)
	case request.Status != faucet.RequestStatusPending:
		t.Fatalf("expected %v, got %v", faucet.RequestStatusBroadcast, request.Status)
	}

	// wait for processing
	time.Sleep(5 * time.Second)

	// check that the request has moved to broadcast
	request, err = f.Request(requestID)
	if err != nil {
		t.Fatal(err)
	} else if request.Status != faucet.RequestStatusBroadcast {
		t.Fatalf("expected %v, got %v", faucet.RequestStatusBroadcast, request.Status)
	} else if request.TransactionID == (types.TransactionID{}) {
		t.Fatal("expected transaction id")
	}

	// mine a block to trigger confirmation
	if err := node.MineBlocks(node.Wallet().Address(), 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)

	// check that the request has moved to confirmed
	request, err = f.Request(requestID)
	if err != nil {
		t.Fatal(err)
	} else if request.Status != faucet.RequestStatusConfirmed {
		t.Fatalf("expected %v, got %v", faucet.RequestStatusConfirmed, request.Status)
	} else if request.TransactionID == (types.TransactionID{}) {
		t.Fatal("expected transaction id")
	} else if request.BlockID == (types.BlockID{}) {
		t.Fatal("expected block id")
	}

	// validate that the wallet balance was increased
	_, balance, err := node2.Wallet().Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Equals(types.SiacoinPrecision) {
		t.Fatalf("expected %v, got %v", types.SiacoinPrecision, balance)
	}

	// try to request more than the limit
	for i := 0; i < 4; i++ {
		_, err = f.RequestAmount(node2.Wallet().Address(), "10.10.10.10", types.SiacoinPrecision)
		if err != nil {
			t.Fatal(err)
		}
	}

	_, err = f.RequestAmount(node2.Wallet().Address(), "10.10.10.10", types.SiacoinPrecision)
	if !errors.Is(err, faucet.ErrCountExceeded) {
		t.Fatalf("expected %v, got %v", faucet.ErrCountExceeded, err)
	}
}
