package faucet

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/wallet"
	"go.uber.org/zap"
)

// request statuses are used to track the status of a faucet request
const (
	RequestStatusPending   RequestStatus = "pending"
	RequestStatusBroadcast RequestStatus = "broadcast"
	RequestStatusConfirmed RequestStatus = "confirmed"
)

const (
	processInterval  = 10 * time.Second
	processBatchSize = 50
	updateBatchSize  = 100
	estimatedTxnSize = 4096
	pruneWindow      = 7 * 24 * time.Hour
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

	// An UpdateTx atomically applies chain updates to the wallet state.
	UpdateTx interface {
		wallet.UpdateTx

		SetLastIndex(types.ChainIndex) error
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

		// ResetChainState removes all wallet state so the chain can be
		// rescanned.
		ResetChainState() error
		// UpdateChainState atomically applies chain updates to the wallet
		// state.
		UpdateChainState(func(UpdateTx) error) error
	}

	// A Faucet fulfills payment requests.
	Faucet struct {
		maxSCPerDay       types.Currency
		maxRequestsPerDay int

		cm     *chain.Manager
		wallet *wallet.SingleAddressWallet
		store  Store
		log    *zap.Logger

		close chan struct{}
		wg    sync.WaitGroup
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

// syncChain applies chain updates to the wallet until it reaches the chain
// manager's tip.
func (f *Faucet) syncChain() error {
	index, err := f.wallet.Tip()
	if err != nil {
		return fmt.Errorf("failed to get wallet tip: %w", err)
	}

	pruneTarget := uint64(pruneWindow / f.cm.TipState().Network.BlockInterval)

	var resetAttempts int
	for index != f.cm.Tip() {
		select {
		case <-f.close:
			return nil
		default:
		}

		reverted, applied, err := f.cm.UpdatesSince(index, updateBatchSize)
		if err != nil {
			resetAttempts++
			if resetAttempts > 3 {
				return fmt.Errorf("failed to get chain updates: %w", err)
			}
			f.log.Warn("resetting chain state", zap.Stringer("index", index), zap.Error(err))
			if err := f.store.ResetChainState(); err != nil {
				return fmt.Errorf("failed to reset chain state: %w", err)
			}
			index = types.ChainIndex{}
			continue
		} else if len(reverted) == 0 && len(applied) == 0 {
			break
		}

		err = f.store.UpdateChainState(func(tx UpdateTx) error {
			if err := f.wallet.UpdateChainState(tx, reverted, applied); err != nil {
				return fmt.Errorf("failed to update wallet state: %w", err)
			}
			if len(applied) > 0 {
				index = applied[len(applied)-1].State.Index
			} else {
				index = reverted[len(reverted)-1].State.Index
			}
			return tx.SetLastIndex(index)
		})
		if err != nil {
			return fmt.Errorf("failed to update chain state: %w", err)
		}
		f.log.Debug("synced wallet", zap.Stringer("index", index))

		if index.Height > pruneTarget {
			f.cm.PruneBlocks(index.Height - pruneTarget)
		}
	}

	return nil
}

// processRequests processes pending requests and broadcasts them to the
// blockchain.
func (f *Faucet) processRequests(limit uint64) (int, types.Currency, error) {
	requests, err := f.store.UnprocessedRequests(limit)
	if err != nil {
		return 0, types.ZeroCurrency, fmt.Errorf("failed to get unprocessed requests: %w", err)
	} else if len(requests) == 0 {
		return 0, types.ZeroCurrency, nil
	}

	var processed []RequestID
	var total types.Currency
	txn := types.V2Transaction{
		MinerFee: f.wallet.RecommendedFee().Mul64(estimatedTxnSize),
	}
	for _, req := range requests {
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
			Value:   req.Amount,
			Address: req.UnlockHash,
		})
		processed = append(processed, req.ID)
		total = total.Add(req.Amount)
	}

	basis, toSign, err := f.wallet.FundV2Transaction(&txn, total.Add(txn.MinerFee), false)
	if err != nil {
		return 0, types.ZeroCurrency, fmt.Errorf("failed to fund transaction: %w", err)
	}
	f.wallet.SignV2Inputs(&txn, toSign)

	basis, txnset, err := f.cm.V2TransactionSet(basis, txn)
	if err != nil {
		f.wallet.ReleaseInputs(nil, []types.V2Transaction{txn})
		return 0, types.ZeroCurrency, fmt.Errorf("failed to create transaction set: %w", err)
	} else if err := f.wallet.BroadcastV2TransactionSet(basis, txnset); err != nil {
		f.wallet.ReleaseInputs(nil, []types.V2Transaction{txn})
		return 0, types.ZeroCurrency, fmt.Errorf("failed to broadcast transaction: %w", err)
	} else if err := f.store.ProcessRequests(processed, txn.ID()); err != nil {
		return 0, types.ZeroCurrency, fmt.Errorf("failed to process requests: %w", err)
	}
	return len(requests), total, nil
}

// Close closes the faucet and stops processing requests.
func (f *Faucet) Close() error {
	select {
	case <-f.close:
		return nil
	default:
	}
	close(f.close)
	f.wg.Wait()
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
	f.log.Debug("requesting funds", zap.String("ip", ipAddress), zap.Stringer("address", address), zap.Stringer("amount", amount), zap.Stringer("remainder", f.maxSCPerDay.Sub(amountRequested)), zap.Int("requests", count))
	return f.store.AddRequest(address, ipAddress, amount)
}

// New initializes a new faucet.
func New(store Store, cm *chain.Manager, w *wallet.SingleAddressWallet, maxRequestsPerDay int, maxSCPerDay types.Currency, log *zap.Logger) *Faucet {
	f := &Faucet{
		store:  store,
		cm:     cm,
		wallet: w,

		maxSCPerDay:       maxSCPerDay,
		maxRequestsPerDay: maxRequestsPerDay,

		log:   log,
		close: make(chan struct{}),
	}

	reorgCh := make(chan struct{}, 1)
	reorgCh <- struct{}{}
	stop := cm.OnReorg(func(types.ChainIndex) {
		select {
		case reorgCh <- struct{}{}:
		default:
		}
	})

	f.wg.Add(2)
	go func() {
		defer f.wg.Done()
		defer stop()

		for {
			select {
			case <-f.close:
				return
			case <-reorgCh:
				if err := f.syncChain(); err != nil {
					f.log.Error("failed to sync chain", zap.Error(err))
				}
			}
		}
	}()

	go func() {
		defer f.wg.Done()

		log := f.log.Named("process")
		t := time.NewTicker(processInterval)
		defer t.Stop()

		var current types.ChainIndex
		for {
			select {
			case <-f.close:
				return
			case <-t.C:
				cs := f.cm.TipState()
				if cs.Index == current {
					continue // skip processing if the consensus tip hasn't changed
				}

				walletTip, err := f.wallet.Tip()
				if err != nil {
					log.Error("failed to get wallet tip", zap.Error(err))
					continue
				} else if walletTip != cs.Index || time.Since(cs.PrevTimestamps[0]) > 4*cs.Network.BlockInterval {
					continue // skip processing until the wallet and chain are synced
				}

				n, amount, err := f.processRequests(processBatchSize)
				if err != nil {
					log.Error("failed to process requests", zap.Error(err))
				} else if n > 0 {
					log.Info("processed requests", zap.Int("requests", n), zap.Stringer("amount", amount))
					current = cs.Index // only change tips if requests were processed
				}
			}
		}
	}()
	return f
}
