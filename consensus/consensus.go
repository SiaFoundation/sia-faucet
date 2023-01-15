package consensus

import (
	"errors"
	"fmt"
	"sync"

	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

type (
	// A ChainIndex groups a block's ID and height.
	ChainIndex struct {
		ID     types.BlockID
		Height uint64
	}

	// State represents the full state of the chain as of a particular block.
	State struct {
		Index ChainIndex
	}

	// A ChainManager manages the current state of the blockchain.
	ChainManager struct {
		cs modules.ConsensusSet

		close chan struct{}
		mu    sync.Mutex
		tip   State
	}
)

var (
	// ErrBlockNotFound is returned when a block is not found.
	ErrBlockNotFound = errors.New("block not found")
)

// ProcessConsensusChange implements the modules.ConsensusSetSubscriber interface.
func (cm *ChainManager) ProcessConsensusChange(cc modules.ConsensusChange) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.tip = State{
		Index: ChainIndex{
			ID:     types.BlockID(cc.AppliedBlocks[len(cc.AppliedBlocks)-1].ID()),
			Height: uint64(cc.BlockHeight),
		},
	}
}

// Close closes the chain manager.
func (cm *ChainManager) Close() error {
	select {
	case <-cm.close:
		return nil
	default:
	}
	close(cm.close)
	return nil
}

// Synced returns true if the chain manager is synced with the consensus set.
func (cm *ChainManager) Synced() bool {
	return cm.cs.Synced()
}

// BlockAtHeight returns the block at the given height.
func (cm *ChainManager) BlockAtHeight(height uint64) (types.Block, bool) {
	return cm.cs.BlockAtHeight(types.BlockHeight(height))
}

// IndexAtHeight return the chain index at the given height.
func (cm *ChainManager) IndexAtHeight(height uint64) (ChainIndex, error) {
	block, ok := cm.cs.BlockAtHeight(types.BlockHeight(height))
	if !ok {
		return ChainIndex{}, ErrBlockNotFound
	}
	return ChainIndex{
		ID:     types.BlockID(block.ID()),
		Height: height,
	}, nil
}

// ConsensusSetSubscribe adds a subscriber to the list of subscribers and gives them every consensus change that has occurred since the change with the provided id. There are a few special cases, described by the ConsensusChangeX variables in this package. A channel can be provided to abort the subscription process.
func (cm *ChainManager) ConsensusSetSubscribe(subscriber modules.ConsensusSetSubscriber, start modules.ConsensusChangeID, cancel <-chan struct{}) error {
	return cm.cs.ConsensusSetSubscribe(subscriber, start, cancel)
}

// Tip returns the current tip of the blockchain.
func (cm *ChainManager) Tip() State {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.tip
}

// NewChainManager creates a new chain manager.
func NewChainManager(cs modules.ConsensusSet) (*ChainManager, error) {
	height := cs.Height()
	block, ok := cs.BlockAtHeight(height)
	if !ok {
		return nil, fmt.Errorf("failed to get block at height %d", height)
	}

	cm := &ChainManager{
		cs: cs,
		tip: State{
			Index: ChainIndex{
				ID:     block.ID(),
				Height: uint64(height),
			},
		},

		close: make(chan struct{}),
	}

	// prevent chain manager blocking
	go func() {
		if err := cs.ConsensusSetSubscribe(cm, modules.ConsensusChangeRecent, cm.close); err != nil {
			panic(err)
		}
	}()
	return cm, nil
}
