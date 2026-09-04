package main

import (
	"net"
	"sync"
	"time"

	"go.sia.tech/coreutils/syncer"
)

type peerStore struct {
	mu    sync.Mutex
	peers map[string]syncer.PeerInfo
	bans  map[string]time.Time
}

var _ syncer.PeerStore = (*peerStore)(nil)

// AddPeer adds a peer to the store. If the peer already exists, nil is
// returned.
func (ps *peerStore) AddPeer(addr string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if _, ok := ps.peers[addr]; !ok {
		ps.peers[addr] = syncer.PeerInfo{Address: addr, FirstSeen: time.Now()}
	}
	return nil
}

// Peers returns the set of known peers.
func (ps *peerStore) Peers() ([]syncer.PeerInfo, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	peers := make([]syncer.PeerInfo, 0, len(ps.peers))
	for _, p := range ps.peers {
		peers = append(peers, p)
	}
	return peers, nil
}

// PeerInfo returns the metadata for the specified peer or ErrPeerNotFound if
// the peer wasn't found in the store.
func (ps *peerStore) PeerInfo(addr string) (syncer.PeerInfo, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.peers[addr]
	if !ok {
		return syncer.PeerInfo{}, syncer.ErrPeerNotFound
	}
	return p, nil
}

// UpdatePeerInfo updates the metadata for the specified peer. If the peer is
// not found, ErrPeerNotFound is returned.
func (ps *peerStore) UpdatePeerInfo(addr string, fn func(*syncer.PeerInfo)) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.peers[addr]
	if !ok {
		return syncer.ErrPeerNotFound
	}
	fn(&p)
	ps.peers[addr] = p
	return nil
}

// Ban temporarily bans the host of the given address.
func (ps *peerStore) Ban(addr string, duration time.Duration, _ string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.bans[banKey(addr)] = time.Now().Add(duration)
	return nil
}

// Banned returns true if the host of the given address is banned.
func (ps *peerStore) Banned(addr string) (bool, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return time.Now().Before(ps.bans[banKey(addr)]), nil
}

func banKey(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func newPeerStore(peers []string) *peerStore {
	ps := &peerStore{
		peers: make(map[string]syncer.PeerInfo),
		bans:  make(map[string]time.Time),
	}
	for _, addr := range peers {
		ps.AddPeer(addr)
	}
	return ps
}
