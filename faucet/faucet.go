package faucet

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/api"
	"go.uber.org/zap"
)

// request statuses are used to track the status of a faucet request
const (
	RequestStatusPending   RequestStatus = "pending"
	RequestStatusBroadcast RequestStatus = "broadcast"
	RequestStatusConfirmed RequestStatus = "confirmed"
)

type (
	// A RequestID uniquely identifies a faucet request
	RequestID [32]byte
	// RequestStatus is the status of a faucet request
	RequestStatus string

	// A Request represents a payment request
	Request struct {
		ID            RequestID           `json:"id"`
		IPAddress     string              `json:"ipAddress"`
		UnlockHash    types.Address       `json:"unlockHash"`
		Amount        types.Currency      `json:"amount"`
		BlockID       types.BlockID       `json:"blockID"`
		TransactionID types.TransactionID `json:"transactionID"`
		Status        RequestStatus       `json:"status"`
		Timestamp     time.Time           `json:"timestamp"`
	}

	// A Store manages requests and the state of the faucet.
	Store interface {
		// Request returns the request with the given id
		Request(id RequestID) (Request, error)
		// AddRequest adds a new pending payment request to the store
		AddRequest(address types.Address, ipAddress string, amount types.Currency) (RequestID, error)
		// Requests returns the sum and count of all requests for the given
		// address and ip address in the last 24 hours.
		Requests(address types.Address, ipAddress string) (types.Currency, int, error)
		// UnprocessedRequests returns the first n unprocessed requests
		UnprocessedRequests(limit uint64) ([]Request, error)
		// ProcessRequests updates the transaction id for the requests and
		// marks them as "broadcast"
		ProcessRequests(requests []RequestID, transactionID types.TransactionID) error
	}

	// A Faucet fulfills payment requests.
	Faucet struct {
		maxSCPerDay       types.Currency
		maxRequestsPerDay int

		signingKey types.PrivateKey
		api        *api.Client
		wallet     *api.WalletClient

		log                 *zap.Logger
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

	var processed []RequestID
	var outputs []types.SiacoinOutput
	for _, req := range requests {
		outputs = append(outputs, types.SiacoinOutput{
			Value:   req.Amount,
			Address: req.UnlockHash,
		})
		processed = append(processed, req.ID)
	}

	changeAddress := types.StandardUnlockHash(f.signingKey.PublicKey())
	cs, err := f.api.ConsensusTipState()
	if err != nil {
		return 0, fmt.Errorf("failed to get consensus tip state: %w", err)
	}

	var transactionID types.TransactionID
	if cs.Index.Height < cs.Network.HardforkV2.AllowHeight {
		resp, err := f.wallet.Construct(outputs, nil, changeAddress)
		if err != nil {
			return 0, fmt.Errorf("failed to construct transaction: %w", err)
		}

		for i, sig := range resp.Transaction.Signatures {
			sigHash := cs.WholeSigHash(resp.Transaction, sig.ParentID, 0, 0, nil)
			signature := f.signingKey.SignHash(sigHash)
			resp.Transaction.Signatures[i].Signature = signature[:]
		}

		if err := f.api.TxpoolBroadcast(resp.Basis, []types.Transaction{resp.Transaction}, nil); err != nil {
			return 0, fmt.Errorf("failed to broadcast transaction: %w", err)
		}
		transactionID = resp.ID
	} else {
		resp, err := f.wallet.ConstructV2(outputs, nil, changeAddress)
		if err != nil {
			return 0, fmt.Errorf("failed to construct transaction: %w", err)
		}

		sigHash := cs.InputSigHash(resp.Transaction)
		for i := range resp.Transaction.SiacoinInputs {
			resp.Transaction.SiacoinInputs[i].SatisfiedPolicy.Signatures = []types.Signature{f.signingKey.SignHash(sigHash)}
		}

		if err := f.api.TxpoolBroadcast(resp.Basis, nil, []types.V2Transaction{resp.Transaction}); err != nil {
			return 0, fmt.Errorf("failed to broadcast transaction: %w", err)
		}
		transactionID = resp.ID
	}

	if err := f.store.ProcessRequests(processed, transactionID); err != nil {
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
func (f *Faucet) RequestAmount(address types.Address, ipAddress string, amount types.Currency) (RequestID, error) {
	amountRequested, count, err := f.store.Requests(address, ipAddress)
	if err != nil {
		return RequestID{}, fmt.Errorf("failed to get amount requested: %w", err)
	}

	// validate the request is not limited
	if amountRequested.Add(amount).Cmp(f.maxSCPerDay) > 0 {
		return RequestID{}, ErrAmountExceeded
	} else if count >= f.maxRequestsPerDay {
		return RequestID{}, ErrCountExceeded
	}
	return f.store.AddRequest(address, ipAddress, amount)
}

// New initializes a new faucet.
func New(store Store, signingKey types.PrivateKey, client *api.Client, walletd *api.WalletClient, maxRequestsPerDay int, maxSCPerDay types.Currency, interval time.Duration, log *zap.Logger) (*Faucet, error) {
	f := &Faucet{
		store:  store,
		api:    client,
		wallet: walletd,

		signingKey:        signingKey,
		maxSCPerDay:       maxSCPerDay,
		maxRequestsPerDay: maxRequestsPerDay,

		log:                 log,
		lastConsensusChange: time.Now(),

		close: make(chan struct{}),
	}

	go func() {
		// initialize the processing timer
		t := time.NewTicker(interval)
		defer t.Stop()

		for {
			log := f.log.Named("process")
			select {
			case <-f.close: // close received, stop processing
				return
			case <-t.C: // timer fired, begin processing request queue
				err := func() error {
					// batch requests until either the queue or wallet are empty
					n, err := f.processRequests(50)
					if err != nil { // stop processing if an error occurred
						return fmt.Errorf("failed to process requests: %w", err)
					} else if n == 0 { // don't log if no requests were processed
						return nil
					}
					// log the number of requests processed and remaining balance
					resp, _ := f.wallet.Balance()
					log.Debug("processed requests", zap.Int("requests", n), zap.Stringer("balance", resp.Siacoins))
					return nil
				}()
				if err != nil {

				}
			}
		}
	}()
	return f, nil
}
