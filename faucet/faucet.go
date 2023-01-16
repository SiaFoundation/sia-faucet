package faucet

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// request statuses are used to track the status of a faucet request
const (
	RequestStatusPending   RequestStatus = "pending"
	RequestStatusBroadcast RequestStatus = "broadcast"
	RequestStatusConfirmed RequestStatus = "confirmed"
)

type (
	// An UpdateTx atomically updates the store
	UpdateTx interface {
		// ApplyTransactionBlock marks all faucet requests in a transaction as confirmed
		ApplyTransactionBlock(transactionID types.TransactionID, blockID types.BlockID) error
		// RevertBlock marks a faucet request for the siacoin output as not confirmed
		RevertBlock(blockID types.BlockID) error
		// SetLastChange sets the last consensus change id
		SetLastChange(cc modules.ConsensusChangeID) error
	}

	// A RequestID uniquely identifies a faucet request
	RequestID [32]byte
	// RequestStatus is the status of a faucet request
	RequestStatus string

	// A Request represents a payment request
	Request struct {
		ID            RequestID           `json:"id"`
		IPAddress     string              `json:"ipAddress"`
		UnlockHash    types.UnlockHash    `json:"unlockHash"`
		Amount        types.Currency      `json:"amount"`
		BlockID       types.BlockID       `json:"blockID"`
		TransactionID types.TransactionID `json:"transactionID"`
		Status        RequestStatus       `json:"status"`
		Timestamp     time.Time           `json:"timestamp"`
	}

	// A Store manages requests and the state of the faucet.
	Store interface {
		// Update atomically updates the store
		Update(func(tx UpdateTx) error) error

		// Request returns the request with the given id
		Request(id RequestID) (Request, error)
		// AddRequest adds a new pending payment request to the store
		AddRequest(address types.UnlockHash, ipAddress string, amount types.Currency) (RequestID, error)
		// Requests returns the sum and count of all requests for the given
		// address and ip address in the last 24 hours.
		Requests(address types.UnlockHash, ipAddress string) (types.Currency, int, error)
		// UnprocessedRequests returns the first n unprocessed requests
		UnprocessedRequests(limit uint64) ([]Request, error)
		// ProcessRequests updates the transaction id for the requests and
		// marks them as "broadcast"
		ProcessRequests(requests []RequestID, transactionID types.TransactionID) error

		// GetLastChange returns the last processed consensus change
		GetLastChange() (modules.ConsensusChangeID, error)
	}

	// A Wallet funds and signs transactions.
	Wallet interface {
		Balance() (spendable, confirmed types.Currency, err error)
		FundTransaction(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, func(), error)
		SignTransaction(txn *types.Transaction, toSign []crypto.Hash, cf types.CoveredFields) error
	}

	// A ChainManager manages the current state of the blockchain.
	ChainManager interface {
		// ConsensusSetSubscribe adds a subscriber to the list of subscribers
		// and gives them every consensus change that has occurred since the
		// change with the provided id. There are a few special cases, described
		// by the ConsensusChangeX variables in this package. A channel can be
		// provided to abort the subscription process.
		ConsensusSetSubscribe(subscriber modules.ConsensusSetSubscriber, start modules.ConsensusChangeID, cancel <-chan struct{}) error
	}

	// A TPool broadcasts transactions to the network.
	TPool interface {
		// AcceptTransactionSet accepts a set of potentially interdependent
		// transactions.
		AcceptTransactionSet([]types.Transaction) error
	}

	// A Faucet fulfills payment requests.
	Faucet struct {
		maxSCPerDay       types.Currency
		maxRequestsPerday int

		cm ChainManager
		tp TPool
		w  Wallet

		log                 *log.Logger
		lastConsensusChange time.Time

		close chan struct{}

		store Store
	}
)

var (
	// ErrAmountExceeded is returned if the amount requested exceeds the maximum
	ErrAmountExceeded = errors.New("amount exceeds max amount per day")
	// ErrCountExceeded is returned if the number of requests exceeds the maximum
	ErrCountExceeded = errors.New("request count exceeds max requests per day")
	// ErrNotFound is returned if a request is not found
	ErrNotFound = errors.New("not found")
)

// String returns the string representation of a RequestID
func (r RequestID) String() string {
	return hex.EncodeToString(r[:])
}

// MarshalText implements the encoding.TextMarshaler interface
func (r RequestID) MarshalText() ([]byte, error) {
	return []byte(r.String()), nil
}

// UnmarshalText implements the encoding.TextUnmarshaler interface
func (r *RequestID) UnmarshalText(b []byte) error {
	if len(b) != 64 {
		return fmt.Errorf("invalid request id: %s", string(b))
	}
	_, err := hex.Decode(r[:], b)
	return err
}

// processRequests processes pending requests and broadcasts them to the
// blockchain.
func (f *Faucet) processRequests(limit uint64) (int, error) {
	requests, err := f.store.UnprocessedRequests(limit)
	if err != nil {
		return 0, fmt.Errorf("failed to get unprocessed requests: %w", err)
	} else if len(requests) == 0 {
		return 0, nil
	}

	var faucetTxn types.Transaction
	var fundAmount types.Currency
	var processed []RequestID
	for _, req := range requests {
		faucetTxn.SiacoinOutputs = append(faucetTxn.SiacoinOutputs, types.SiacoinOutput{
			Value:      req.Amount,
			UnlockHash: req.UnlockHash,
		})
		fundAmount = fundAmount.Add(req.Amount)
		processed = append(processed, req.ID)
	}

	fundAmount = fundAmount.Add(types.SiacoinPrecision)
	faucetTxn.MinerFees = append(faucetTxn.MinerFees, types.SiacoinPrecision)

	toSign, release, err := f.w.FundTransaction(&faucetTxn, fundAmount)
	if err != nil {
		return 0, fmt.Errorf("failed to fund faucet transaction: %w", err)
	}
	defer release()

	if err = f.w.SignTransaction(&faucetTxn, toSign, types.FullCoveredFields); err != nil {
		return 0, fmt.Errorf("failed to sign faucet transaction: %w", err)
	} else if err := f.tp.AcceptTransactionSet([]types.Transaction{faucetTxn}); err != nil {
		return 0, fmt.Errorf("failed to accept faucet transaction: %w", err)
	} else if err := f.store.ProcessRequests(processed, faucetTxn.ID()); err != nil {
		return 0, fmt.Errorf("failed to process requests: %w", err)
	}
	return len(requests), nil
}

// Close closes the faucet and stops processing requests.
func (f *Faucet) Close() error {
	select {
	case <-f.close:
		return nil
	default:
	}
	close(f.close)
	return nil
}

// Request returns the request with the given id.
func (f *Faucet) Request(id RequestID) (Request, error) {
	return f.store.Request(id)
}

// RequestAmount requests an amount of siacoins to be sent to address.
func (f *Faucet) RequestAmount(address types.UnlockHash, ipAddress string, amount types.Currency) (RequestID, error) {
	amountRequested, count, err := f.store.Requests(address, ipAddress)
	if err != nil {
		return RequestID{}, fmt.Errorf("failed to get amount requested: %w", err)
	}

	// validate the request is not limited
	if amountRequested.Add(amount).Cmp(f.maxSCPerDay) > 0 {
		return RequestID{}, ErrAmountExceeded
	} else if count >= f.maxRequestsPerday {
		return RequestID{}, ErrCountExceeded
	}
	return f.store.AddRequest(address, ipAddress, amount)
}

// ProcessConsensusChange implements modules.ConsensusChangeSubscriber
func (f *Faucet) ProcessConsensusChange(cc modules.ConsensusChange) {
	select {
	case <-f.close: // prevent processing after close
		return
	default:
	}
	// update existing requests
	err := f.store.Update(func(tx UpdateTx) error {
		for _, reverted := range cc.RevertedBlocks {
			if err := tx.RevertBlock(reverted.ID()); err != nil {
				return fmt.Errorf("failed to revert block: %w", err)
			}
		}
		for _, applied := range cc.AppliedBlocks {
			for _, txn := range applied.Transactions {
				if err := tx.ApplyTransactionBlock(txn.ID(), applied.ID()); err != nil {
					return fmt.Errorf("failed to apply block: %w", err)
				}
			}
		}
		return tx.SetLastChange(cc.ID)
	})
	if err != nil {
		f.log.Printf("failed to process consensus change %v: %v", cc.ID, err)
	}
	if time.Since(f.lastConsensusChange) > 5*time.Minute {
		f.lastConsensusChange = time.Now()
		f.log.Printf("faucet synced to block %v at height %v", cc.AppliedBlocks[len(cc.AppliedBlocks)-1].ID(), cc.BlockHeight)
	}
}

// New initializes a new faucet.
func New(cm ChainManager, tp TPool, w Wallet, store Store, maxRequestsPerDay int, maxSCPerDay types.Currency, interval time.Duration, log *log.Logger) (*Faucet, error) {
	f := &Faucet{
		cm:    cm,
		tp:    tp,
		w:     w,
		store: store,

		maxSCPerDay:       maxSCPerDay,
		maxRequestsPerday: maxRequestsPerDay,

		log:                 log,
		lastConsensusChange: time.Now(),

		close: make(chan struct{}),
	}

	ccID, err := store.GetLastChange()
	if err != nil {
		return nil, fmt.Errorf("failed to get last change: %w", err)
	}

	go func() {
		// initialize the processing timer
		t := time.NewTimer(interval)
		for {
			select {
			case <-f.close: // close received, stop processing
				return
			case <-t.C: // timer fired, begin processing request queue
				for {
					// batch requests until either the queue or wallet are empty
					n, err := f.processRequests(50)
					if err != nil { // stop processing if an error occurred
						f.log.Printf("unable to process requests: %v", err)
						break
					} else if n == 0 { // stop processing if the queue is empty
						break
					}
					// log the number of requests processed and remaining balance
					spendable, _, _ := f.w.Balance()
					f.log.Printf("fulfilled %v requests (remaining balance: %s)", n, spendable.HumanString())
				}
				t.Reset(interval) // reset the timer
			}
		}
	}()

	go func() {
		if err := cm.ConsensusSetSubscribe(f, ccID, f.close); err != nil && ccID != modules.ConsensusChangeBeginning {
			// retry subscription if it failed and the change id is not the beginning
			if err := cm.ConsensusSetSubscribe(f, modules.ConsensusChangeBeginning, f.close); err != nil {
				f.log.Printf("failed to subscribe to consensus set: %v", err)
			}
		} else if err != nil {
			f.log.Printf("failed to subscribe to consensus set: %v", err)
		}
	}()
	return f, nil
}
