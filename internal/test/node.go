package test

import (
	"crypto/ed25519"
	"fmt"
	"path/filepath"

	"go.sia.tech/faucet/consensus"
	"go.sia.tech/faucet/internal/persist/sqlite"
	"go.sia.tech/faucet/wallet"
	"go.sia.tech/siad/modules"
	mconsensus "go.sia.tech/siad/modules/consensus"
	"go.sia.tech/siad/modules/gateway"
	"go.sia.tech/siad/modules/transactionpool"
	"go.sia.tech/siad/types"
)

type (
	// A Node is a base Sia node that can be extended
	Node struct {
		privKey ed25519.PrivateKey

		g  modules.Gateway
		cs modules.ConsensusSet
		tp modules.TransactionPool

		cm *consensus.ChainManager
		m  *Miner
		w  *wallet.SingleAddressWallet

		db *sqlite.Store
	}
)

// Close closes the Sia node
func (n *Node) Close() error {
	n.w.Close()
	n.cm.Close()
	n.tp.Close()
	n.cs.Close()
	n.g.Close()
	n.db.Close()
	return nil
}

// ChainManager returns the node's chain manager
func (n *Node) ChainManager() *consensus.ChainManager {
	return n.cm
}

func (n *Node) TPool() modules.TransactionPool {
	return n.tp
}

// Wallet returns the node's wallet
func (n *Node) Wallet() *wallet.SingleAddressWallet {
	return n.w
}

// PublicKey returns the node's public key
func (n *Node) PublicKey() ed25519.PublicKey {
	return n.privKey.Public().(ed25519.PublicKey)
}

// GatewayAddr returns the address of the gateway
func (n *Node) GatewayAddr() string {
	return string(n.g.Address())
}

// ConnectPeer connects the host's gateway to a peer
func (n *Node) ConnectPeer(addr string) error {
	return n.g.Connect(modules.NetAddress(addr))
}

// MineBlocks mines n blocks sending the reward to address
func (n *Node) MineBlocks(address types.UnlockHash, count int) error {
	return n.m.Mine(address, count)
}

// NewNode creates a new Sia node and wallet with the given key
func NewNode(privKey ed25519.PrivateKey, dir string) (*Node, error) {
	db, err := sqlite.OpenDatabase(filepath.Join(dir, "test.db"))
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	g, err := gateway.New("localhost:0", false, filepath.Join(dir, "gateway"))
	if err != nil {
		return nil, fmt.Errorf("failed to create gateway: %w", err)
	}

	cs, errCh := mconsensus.New(g, false, filepath.Join(dir, "consensus"))
	if err := <-errCh; err != nil {
		return nil, fmt.Errorf("failed to create consensus set: %w", err)
	}
	cm, err := consensus.NewChainManager(cs)
	if err != nil {
		return nil, fmt.Errorf("failed to create chain manager: %w", err)
	}

	tp, err := transactionpool.New(cs, g, filepath.Join(dir, "transactionpool"))
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction pool: %w", err)
	}
	m := NewMiner(cs)
	if err := cs.ConsensusSetSubscribe(m, modules.ConsensusChangeBeginning, nil); err != nil {
		return nil, fmt.Errorf("failed to subscribe miner to consensus set: %w", err)
	}
	tp.TransactionPoolSubscribe(m)

	w, err := wallet.NewSingleAddressWallet(privKey, cm, tp, sqlite.NewWalletStore(db))
	if err != nil {
		return nil, fmt.Errorf("failed to create wallet: %w", err)
	}
	return &Node{
		privKey: privKey,

		g:  g,
		cm: cm,
		cs: cs,
		tp: tp,
		m:  m,
		w:  w,

		db: db,
	}, nil
}
