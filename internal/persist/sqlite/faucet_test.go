package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"go.sia.tech/faucet/faucet"
	"go.sia.tech/siad/types"
	"lukechampine.com/frand"
)

func TestAddRequest(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	fs := NewFaucetStore(db)

	requestUnlockHash := types.UnlockHash(frand.Entropy256())
	expectedIPAddress := "10.10.0.10"
	expectedAmount := types.SiacoinPrecision.Mul64(1000)

	// Create a new request.
	id, err := fs.AddRequest(requestUnlockHash, expectedIPAddress, expectedAmount)
	if err != nil {
		t.Fatal(err)
	}

	// Get the request.
	request, err := fs.Request(id)
	if err != nil {
		t.Fatal(err)
	}

	// validate the request fields
	switch {
	case request.ID != id:
		t.Fatalf("expected request ID %v, got %v", id, request.ID)
	case request.UnlockHash != requestUnlockHash:
		t.Fatalf("expected request unlock hash %v, got %v", requestUnlockHash, request.UnlockHash)
	case request.IPAddress != expectedIPAddress:
		t.Fatalf("expected request IP address %v, got %v", expectedIPAddress, request.IPAddress)
	case request.Amount.Cmp(expectedAmount) != 0:
		t.Fatalf("expected request amount %v, got %v", expectedAmount, request.Amount)
	case time.Since(request.Timestamp) > time.Minute:
		t.Fatalf("expected request timestamp to be in the recent past, got %v", request.Timestamp)
	}
}

func TestAmountRequested(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	fs := NewFaucetStore(db)

	expectedUnlockHash := types.UnlockHash(frand.Entropy256())
	expectedIPAddress := "10.10.0.10"
	expectedAmount := types.SiacoinPrecision.Mul64(1000)

	// check that the amount requested is zero
	amountRequested, err := fs.AmountRequested(expectedUnlockHash, expectedIPAddress)
	if err != nil {
		t.Fatal(err)
	} else if amountRequested.Cmp(types.ZeroCurrency) != 0 {
		t.Fatalf("expected amount requested %v, got %v", types.ZeroCurrency, amountRequested)
	}

	// Create a new request.
	firstID, err := fs.AddRequest(expectedUnlockHash, expectedIPAddress, expectedAmount)
	if err != nil {
		t.Fatal(err)
	}

	// Get the amount requested.
	amountRequested, err = fs.AmountRequested(expectedUnlockHash, expectedIPAddress)
	if err != nil {
		t.Fatal(err)
	} else if amountRequested.Cmp(expectedAmount) != 0 {
		t.Fatalf("expected amount requested %v, got %v", expectedAmount, amountRequested)
	}

	// check the amount requested by only the IP
	amountRequested, err = fs.AmountRequested(types.UnlockHash{}, expectedIPAddress)
	if err != nil {
		t.Fatal(err)
	} else if amountRequested.Cmp(expectedAmount) != 0 {
		t.Fatalf("expected amount requested %v, got %v", expectedAmount, amountRequested)
	}

	// check the amount requested by only the unlock hash
	amountRequested, err = fs.AmountRequested(expectedUnlockHash, "10.10.10.10")
	if err != nil {
		t.Fatal(err)
	} else if amountRequested.Cmp(expectedAmount) != 0 {
		t.Fatalf("expected amount requested %v, got %v", expectedAmount, amountRequested)
	}

	// add an additional request with a different IP, but the same unlock hash
	if _, err = fs.AddRequest(expectedUnlockHash, "10.10.10.10", amountRequested); err != nil {
		t.Fatal(err)
	}

	// check the amount requested by only the unlock hash
	amountRequested, err = fs.AmountRequested(expectedUnlockHash, "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	} else if amountRequested.Cmp(expectedAmount.Mul64(2)) != 0 {
		t.Fatalf("expected amount requested %v, got %v", expectedAmount.Mul64(2), amountRequested)
	}

	// check the amount requested for the second IP
	amountRequested, err = fs.AmountRequested(types.UnlockHash{}, "10.10.10.10")
	if err != nil {
		t.Fatal(err)
	} else if amountRequested.Cmp(expectedAmount) != 0 {
		t.Fatalf("expected amount requested %v, got %v", expectedAmount, amountRequested)
	}

	// add an additional request with a different unlock hash, but the same IP
	if _, err = fs.AddRequest(types.UnlockHash(frand.Entropy256()), expectedIPAddress, expectedAmount); err != nil {
		t.Fatal(err)
	}

	// check the amount requested by only the IP
	amountRequested, err = fs.AmountRequested(types.UnlockHash{}, expectedIPAddress)
	if err != nil {
		t.Fatal(err)
	} else if amountRequested.Cmp(expectedAmount.Mul64(2)) != 0 {
		t.Fatalf("expected amount requested %v, got %v", expectedAmount.Mul64(2), amountRequested)
	}

	// expire the first request
	_, err = fs.db.db.Exec("UPDATE faucet_requests SET date_created=date_created-86401 WHERE id=?", valueHash(firstID))
	if err != nil {
		t.Fatal(err)
	}

	// check the amount requested by the unlock hash and IP
	amountRequested, err = fs.AmountRequested(expectedUnlockHash, expectedIPAddress)
	if err != nil {
		t.Fatal(err)
	} else if amountRequested.Cmp(expectedAmount.Mul64(2)) != 0 {
		t.Fatalf("expected amount requested %v, got %v", expectedAmount.Mul64(2), amountRequested)
	}
}

func TestRequestProcessing(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	fs := NewFaucetStore(db)

	expectedUnlockHash := types.UnlockHash(frand.Entropy256())
	expectedIPAddress := "10.10.10.10"
	expectedAmount := types.SiacoinPrecision.Mul64(1000)

	// check that there are no pending requests
	pendingRequests, err := fs.UnprocessedRequests(100)
	if err != nil {
		t.Fatal(err)
	} else if len(pendingRequests) != 0 {
		t.Fatalf("expected no pending requests, got %v", pendingRequests)
	}

	// add a pending request
	firstID, err := fs.AddRequest(expectedUnlockHash, expectedIPAddress, expectedAmount)
	if err != nil {
		t.Fatal(err)
	}

	// check that there is a pending request
	pendingRequests, err = fs.UnprocessedRequests(100)
	if err != nil {
		t.Fatal(err)
	} else if len(pendingRequests) != 1 {
		t.Fatalf("expected 1 pending request, got %v", pendingRequests)
	} else if pendingRequests[0].ID != firstID {
		t.Fatalf("expected pending request %v, got %v", firstID, pendingRequests[0].ID)
	}

	// mark the request as broadcast
	txnID := types.TransactionID(frand.Entropy256())
	if err := fs.ProcessRequests([]faucet.RequestID{firstID}, txnID); err != nil {
		t.Fatal(err)
	}

	// check that the status and transaction ID are correct
	request, err := fs.Request(firstID)
	if err != nil {
		t.Fatal(err)
	} else if request.TransactionID != txnID {
		t.Fatalf("expected transaction ID %v, got %v", txnID, request.TransactionID)
	} else if request.Status != faucet.RequestStatusBroadcast {
		t.Fatalf("expected request status %v, got %v", faucet.RequestStatusBroadcast, request.Status)
	}

	// check that there are no pending requests
	pendingRequests, err = fs.UnprocessedRequests(100)
	if err != nil {
		t.Fatal(err)
	} else if len(pendingRequests) != 0 {
		t.Fatalf("expected no pending requests, got %v", pendingRequests)
	}

	// add a new pending request
	secondID, err := fs.AddRequest(expectedUnlockHash, expectedIPAddress, expectedAmount)
	if err != nil {
		t.Fatal(err)
	}

	// check that there is a pending request
	pendingRequests, err = fs.UnprocessedRequests(100)
	if err != nil {
		t.Fatal(err)
	} else if len(pendingRequests) != 1 {
		t.Fatalf("expected 1 pending request, got %v", pendingRequests)
	} else if pendingRequests[0].ID != secondID {
		t.Fatalf("expected pending request %v, got %v", secondID, pendingRequests[0].ID)
	}

	// expire the first request -- it should become pending again
	_, err = fs.db.db.Exec("UPDATE faucet_requests SET date_created=date_created-86401 WHERE id=?", valueHash(firstID))
	if err != nil {
		t.Fatal(err)
	}

	// check that there are two pending requests
	pendingRequests, err = fs.UnprocessedRequests(100)
	if err != nil {
		t.Fatal(err)
	} else if len(pendingRequests) != 2 {
		t.Fatalf("expected 2 pending requests, got %v", pendingRequests)
	}

	// mark them both as confirmed
	blockID := types.BlockID(frand.Entropy256())
	if err := fs.ProcessRequests([]faucet.RequestID{firstID, secondID}, txnID); err != nil {
		t.Fatal(err)
	}

	// check that the status, transaction ID, and block are correct
	request, err = fs.Request(firstID)
	if err != nil {
		t.Fatal(err)
	} else if request.TransactionID != txnID {
		t.Fatalf("expected transaction ID %v, got %v", txnID, request.TransactionID)
	} else if request.BlockID != blockID {
		t.Fatalf("expected block ID %v, got %v", blockID, request.BlockID)
	} else if request.Status != faucet.RequestStatusConfirmed {
		t.Fatalf("expected request status %v, got %v", faucet.RequestStatusConfirmed, request.Status)
	}

	request, err = fs.Request(secondID)
	if err != nil {
		t.Fatal(err)
	} else if request.TransactionID != txnID {
		t.Fatalf("expected transaction ID %v, got %v", txnID, request.TransactionID)
	} else if request.BlockID != blockID {
		t.Fatalf("expected block ID %v, got %v", blockID, request.BlockID)
	} else if request.Status != faucet.RequestStatusConfirmed {
		t.Fatalf("expected request status %v, got %v", faucet.RequestStatusConfirmed, request.Status)
	}

	// check that there are no pending requests
	pendingRequests, err = fs.UnprocessedRequests(100)
	if err != nil {
		t.Fatal(err)
	} else if len(pendingRequests) != 0 {
		t.Fatalf("expected no pending requests, got %v", pendingRequests)
	}

	// revert the block
	err = fs.Update(func(tx faucet.UpdateTx) error {
		return tx.RevertBlock(blockID)
	})
	if err != nil {
		t.Fatal(err)
	}

	// check that the status of both requests were reset to broadcast
	request, err = fs.Request(firstID)
	if err != nil {
		t.Fatal(err)
	} else if request.TransactionID != txnID {
		t.Fatalf("expected transaction ID %v, got %v", txnID, request.TransactionID)
	} else if request.BlockID != (types.BlockID{}) {
		t.Fatalf("expected block ID %v, got %v", types.BlockID{}, request.BlockID)
	} else if request.Status != faucet.RequestStatusBroadcast {
		t.Fatalf("expected request status %v, got %v", faucet.RequestStatusBroadcast, request.Status)
	}

	// check that the status was reset to broadcast
	request, err = fs.Request(secondID)
	if err != nil {
		t.Fatal(err)
	} else if request.TransactionID != txnID {
		t.Fatalf("expected transaction ID %v, got %v", txnID, request.TransactionID)
	} else if request.BlockID != (types.BlockID{}) {
		t.Fatalf("expected block ID %v, got %v", types.BlockID{}, request.BlockID)
	} else if request.Status != faucet.RequestStatusBroadcast {
		t.Fatalf("expected request status %v, got %v", faucet.RequestStatusBroadcast, request.Status)
	}

	// check that only the first request is back to pending
	pendingRequests, err = fs.UnprocessedRequests(100)
	if err != nil {
		t.Fatal(err)
	} else if len(pendingRequests) != 1 {
		t.Fatalf("expected 1 pending request, got %v", pendingRequests)
	} else if pendingRequests[0].ID != firstID {
		t.Fatalf("expected pending request %v, got %v", firstID, pendingRequests[0].ID)
	}
}
