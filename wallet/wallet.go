package wallet

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"

	"go.sia.tech/faucet/consensus"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

type (
	// A ChainManager manages the current state of the blockchain.
	ChainManager interface {
		Tip() consensus.State
		BlockAtHeight(height uint64) (types.Block, bool)
		ConsensusSetSubscribe(subscriber modules.ConsensusSetSubscriber, start modules.ConsensusChangeID, cancel <-chan struct{}) error
	}

	TPool interface {
		TransactionPoolSubscribe(subscriber modules.TransactionPoolSubscriber)
	}

	// A SiacoinElement is a SiacoinOutput along with its ID.
	SiacoinElement struct {
		types.SiacoinOutput
		ID types.SiacoinOutputID
	}

	// A SingleAddressWallet is a hot wallet that manages the outputs controlled by
	// a single address.
	SingleAddressWallet struct {
		priv  ed25519.PrivateKey
		addr  types.UnlockHash
		cm    ChainManager
		store SingleAddressStore

		close chan struct{}

		mu sync.Mutex // protects the following fields
		// txnsets maps a transaction set to its SiacoinOutputIDs.
		txnsets map[modules.TransactionSetID][]types.SiacoinOutputID
		// tpool is a set of siacoin output IDs that are currently in the
		// transaction pool.
		tpool map[types.SiacoinOutputID]bool
		// locked is a set of siacoin output IDs locked by FundTransaction. They
		// will be released either by calling Release for unused transactions or
		// being confirmed in a block.
		locked map[types.SiacoinOutputID]bool
	}

	// An UpdateTransaction atomically updates the wallet store
	UpdateTransaction interface {
		AddSiacoinElement(utxo SiacoinElement) error
		RemoveSiacoinElement(id types.SiacoinOutputID) error
		SetLastChange(id modules.ConsensusChangeID) error
	}

	// A SingleAddressStore stores the state of a single-address wallet.
	// Implementations are assumed to be thread safe.
	SingleAddressStore interface {
		Update(context.Context, func(UpdateTransaction) error) error
		// GetLastChange gets the last processed consensus change.
		GetLastChange() (modules.ConsensusChangeID, error)
		UnspentSiacoinElements() ([]SiacoinElement, error)
	}
)

// Close closes the wallet
func (sw *SingleAddressWallet) Close() error {
	select {
	case <-sw.close:
		return nil
	default:
	}
	close(sw.close)
	return nil
}

// Address returns the address of the wallet.
func (sw *SingleAddressWallet) Address() types.UnlockHash {
	return sw.addr
}

// Balance returns the balance of the wallet.
func (sw *SingleAddressWallet) Balance() (spendable, confirmed types.Currency, err error) {
	outputs, err := sw.store.UnspentSiacoinElements()
	if err != nil {
		return types.Currency{}, types.Currency{}, fmt.Errorf("failed to get unspent outputs: %w", err)
	}
	sw.mu.Lock()
	defer sw.mu.Unlock()
	for _, sco := range outputs {
		confirmed = confirmed.Add(sco.Value)
		if !sw.locked[sco.ID] || sw.tpool[sco.ID] {
			spendable = spendable.Add(sco.Value)
		}
	}
	return
}

// FundTransaction adds siacoin inputs worth at least amount to the provided
// transaction. If necessary, a change output will also be added. The inputs
// will not be available to future calls to FundTransaction unless ReleaseInputs
// is called.
func (sw *SingleAddressWallet) FundTransaction(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, func(), error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if amount.IsZero() {
		return nil, nil, nil
	}

	utxos, err := sw.store.UnspentSiacoinElements()
	if err != nil {
		return nil, nil, err
	}
	var inputSum types.Currency
	var fundingElements []SiacoinElement
	for _, sce := range utxos {
		if sw.locked[sce.ID] || sw.tpool[sce.ID] {
			continue
		}
		fundingElements = append(fundingElements, sce)
		inputSum = inputSum.Add(sce.Value)
		if inputSum.Cmp(amount) >= 0 {
			break
		}
	}
	if inputSum.Cmp(amount) < 0 {
		return nil, nil, errors.New("insufficient balance")
	} else if inputSum.Cmp(amount) > 0 {
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
			Value:      inputSum.Sub(amount),
			UnlockHash: sw.addr,
		})
	}

	toSign := make([]crypto.Hash, len(fundingElements))
	for i, sce := range fundingElements {
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
			ParentID:         types.SiacoinOutputID(sce.ID),
			UnlockConditions: StandardUnlockConditions(sw.priv.Public().(ed25519.PublicKey)),
		})
		toSign[i] = crypto.Hash(sce.ID)
		sw.locked[sce.ID] = true
	}

	release := func() {
		sw.mu.Lock()
		defer sw.mu.Unlock()
		for _, id := range toSign {
			delete(sw.locked, types.SiacoinOutputID(id))
		}
	}

	return toSign, release, nil
}

// SignTransaction adds a signature to each of the specified inputs using the
// provided seed.
func (sw *SingleAddressWallet) SignTransaction(txn *types.Transaction, toSign []crypto.Hash, cf types.CoveredFields) error {
	sigMap := make(map[crypto.Hash]bool)
	for _, id := range toSign {
		sigMap[id] = true
	}
	for _, id := range toSign {
		i := len(txn.TransactionSignatures)
		txn.TransactionSignatures = append(txn.TransactionSignatures, types.TransactionSignature{
			ParentID:       id,
			CoveredFields:  cf,
			PublicKeyIndex: 0,
		})
		sigHash := txn.SigHash(i, types.BlockHeight(sw.cm.Tip().Index.Height))
		txn.TransactionSignatures[i].Signature = ed25519.Sign(sw.priv, sigHash[:])
	}
	return nil
}

// ReceiveUpdatedUnconfirmedTransactions implements modules.TransactionPoolSubscriber.
func (sw *SingleAddressWallet) ReceiveUpdatedUnconfirmedTransactions(diff *modules.TransactionPoolDiff) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	for _, txnsetID := range diff.RevertedTransactions {
		for _, outputID := range sw.txnsets[txnsetID] {
			delete(sw.tpool, outputID)
		}
		delete(sw.txnsets, txnsetID)
	}

	for _, txnset := range diff.AppliedTransactions {
		var txnsetOutputs []types.SiacoinOutputID
		for _, txn := range txnset.Transactions {
			for _, sci := range txn.SiacoinInputs {
				if sci.UnlockConditions.UnlockHash() == sw.addr {
					sw.tpool[sci.ParentID] = true
					txnsetOutputs = append(txnsetOutputs, sci.ParentID)
				}
			}
		}
		if len(txnsetOutputs) > 0 {
			sw.txnsets[txnset.ID] = txnsetOutputs
		}
	}
}

// ProcessConsensusChange implements modules.ConsensusSetSubscriber.
func (sw *SingleAddressWallet) ProcessConsensusChange(cc modules.ConsensusChange) {
	// begin a database transaction to update the wallet state
	err := sw.store.Update(context.Background(), func(tx UpdateTransaction) error {
		// add new siacoin outputs and remove spent or reverted siacoin outputs
		for _, diff := range cc.SiacoinOutputDiffs {
			if diff.SiacoinOutput.UnlockHash != sw.addr {
				continue
			}
			if diff.Direction == modules.DiffApply {
				err := tx.AddSiacoinElement(SiacoinElement{
					SiacoinOutput: diff.SiacoinOutput,
					ID:            diff.ID,
				})
				if err != nil {
					return fmt.Errorf("failed to add siacoin element %v: %w", diff.ID, err)
				}
			} else {
				err := tx.RemoveSiacoinElement(diff.ID)
				if err != nil {
					return fmt.Errorf("failed to remove siacoin element %v: %w", diff.ID, err)
				}
				// release the locks on the spent outputs
				sw.mu.Lock()
				delete(sw.locked, diff.ID)
				delete(sw.tpool, diff.ID)
				sw.mu.Unlock()
			}
		}

		// update the change ID
		if err := tx.SetLastChange(cc.ID); err != nil {
			return fmt.Errorf("failed to set index: %w", err)
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
}

// NewSingleAddressWallet returns a new SingleAddressWallet using the provided
// private key and store. The wallet will be subscribed to the consensus set and
// transaction pool.
func NewSingleAddressWallet(priv ed25519.PrivateKey, cm ChainManager, tp TPool, store SingleAddressStore) (*SingleAddressWallet, error) {
	sw := &SingleAddressWallet{
		priv:    priv,
		addr:    StandardAddress(priv.Public().(ed25519.PublicKey)),
		store:   store,
		locked:  make(map[types.SiacoinOutputID]bool),
		tpool:   make(map[types.SiacoinOutputID]bool),
		txnsets: make(map[modules.TransactionSetID][]types.SiacoinOutputID),
		cm:      cm,

		close: make(chan struct{}),
	}

	lastChange, err := store.GetLastChange()
	if err != nil {
		return nil, fmt.Errorf("failed to get last change: %w", err)
	}
	go func() {
		cm.ConsensusSetSubscribe(sw, lastChange, sw.close)
		tp.TransactionPoolSubscribe(sw)
	}()
	return sw, nil
}

// StandardUnlockConditions returns the standard unlock conditions for a single
// Ed25519 key.
func StandardUnlockConditions(pub ed25519.PublicKey) types.UnlockConditions {
	return types.UnlockConditions{
		PublicKeys: []types.SiaPublicKey{{
			Algorithm: types.SignatureEd25519,
			Key:       pub,
		}},
		SignaturesRequired: 1,
	}
}

// StandardAddress returns the standard address for an Ed25519 key.
func StandardAddress(pub ed25519.PublicKey) types.UnlockHash {
	return StandardUnlockConditions(pub).UnlockHash()
}
