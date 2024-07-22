//go:build !testnet

package main

import "go.sia.tech/coreutils/syncer"

var peers = syncer.MainnetBootstrapPeers
