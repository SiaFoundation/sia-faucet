package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/coreutils/wallet"
	"go.sia.tech/faucet/api"
	"go.sia.tech/faucet/faucet"
	"go.sia.tech/faucet/internal/persist/sqlite"
	"go.uber.org/zap"
)

func getChainIndex(ctx context.Context, url string) (index types.ChainIndex, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return types.ChainIndex{}, fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return types.ChainIndex{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return types.ChainIndex{}, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, msg)
	} else if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return types.ChainIndex{}, fmt.Errorf("failed to decode response: %w", err)
	}
	return index, nil
}

// instantSyncCheckpoint returns the checkpoint for the wallet address from the
// explorer.
func instantSyncCheckpoint(ctx context.Context, explorerURL string, address types.Address, n *consensus.Network) (types.ChainIndex, error) {
	checkpoint, err := getChainIndex(ctx, fmt.Sprintf("%s/addresses/%s/checkpoint", explorerURL, address))
	if err != nil {
		return types.ChainIndex{}, fmt.Errorf("failed to get address checkpoint: %w", err)
	} else if checkpoint.Height < n.HardforkV2.RequireHeight {
		return types.ChainIndex{}, fmt.Errorf("checkpoint height %d is before the v2 require height %d", checkpoint.Height, n.HardforkV2.RequireHeight)
	}
	return checkpoint, nil
}

// run starts the faucet daemon. It blocks until the context
// is canceled or an error occurs.
func run(ctx context.Context, signingKey types.PrivateKey, log *zap.Logger) error {
	var n *consensus.Network
	var genesis types.Block
	var peers []string
	var explorerURL string
	switch network {
	case "mainnet":
		n, genesis = chain.Mainnet()
		peers = syncer.MainnetBootstrapPeers
		explorerURL = "https://api.siascan.com"
	case "zen":
		n, genesis = chain.TestnetZen()
		peers = syncer.ZenBootstrapPeers
		explorerURL = "https://api.siascan.com/zen"
	default:
		return fmt.Errorf("unknown network %q", network)
	}

	store, err := sqlite.OpenDatabase(filepath.Join(dir, "faucetd.sqlite3"))
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer store.Close()

	consensusPath := filepath.Join(dir, "consensus.db")
	_, err = os.Stat(consensusPath)
	consensusExists := !errors.Is(err, os.ErrNotExist)

	var dbstore *chain.DBStore
	if instant && !consensusExists {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		checkpoint, err := instantSyncCheckpoint(ctx, explorerURL, types.StandardUnlockHash(signingKey.PublicKey()), n)
		if err != nil {
			return fmt.Errorf("failed to get checkpoint: %w", err)
		}
		log.Info("starting instant sync", zap.Stringer("checkpoint", checkpoint))

		cs, b, err := syncer.RetrieveCheckpoint(ctx, peers, checkpoint, n, genesis.ID())
		if err != nil {
			return fmt.Errorf("failed to retrieve checkpoint: %w", err)
		} else if err := store.ResetChainState(); err != nil {
			return fmt.Errorf("failed to reset chain state: %w", err)
		} else if err := store.SetCheckpoint(checkpoint); err != nil {
			return fmt.Errorf("failed to set checkpoint: %w", err)
		}

		bdb, err := coreutils.OpenBoltChainDB(consensusPath)
		if err != nil {
			return fmt.Errorf("failed to open consensus database: %w", err)
		}
		defer bdb.Close()

		dbstore, err = chain.NewDBStoreAtCheckpoint(bdb, cs, b, chain.NewZapMigrationLogger(log.Named("chain")))
		if err != nil {
			return fmt.Errorf("failed to create chain store: %w", err)
		}
	} else {
		bdb, err := coreutils.OpenBoltChainDB(consensusPath)
		if err != nil {
			return fmt.Errorf("failed to open consensus database: %w", err)
		}
		defer bdb.Close()

		dbstore, err = chain.NewDBStore(bdb, n, genesis, chain.NewZapMigrationLogger(log.Named("chain")))
		if err != nil {
			return fmt.Errorf("failed to create chain store: %w", err)
		}
	}
	cm := chain.NewManager(dbstore, chain.WithLog(log.Named("chain")))

	syncerListener, err := net.Listen("tcp", syncerAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on syncer address: %w", err)
	}
	defer syncerListener.Close()

	// peers will reject us if our hostname is empty or unspecified, so use loopback
	netAddress := syncerListener.Addr().String()
	if host, port, _ := net.SplitHostPort(netAddress); net.ParseIP(host) == nil || net.ParseIP(host).IsUnspecified() {
		netAddress = net.JoinHostPort("127.0.0.1", port)
	}

	s := syncer.New(syncerListener, cm, newPeerStore(peers), gateway.Header{
		GenesisID:  genesis.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: netAddress,
	}, syncer.WithLogger(log.Named("syncer")))
	go s.Run()
	defer s.Close()

	wm, err := wallet.NewSingleAddressWallet(signingKey, cm, store, s, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		return fmt.Errorf("failed to create wallet: %w", err)
	}
	defer wm.Close()

	// initialize the faucet
	f := faucet.New(store, cm, wm, maxRequestsPerDay, maxSCPerDay, log.Named("faucet"))
	defer f.Close()

	// start the listener
	l, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on http address: %w", err)
	}
	defer l.Close()

	srv := http.Server{
		ReadTimeout: 30 * time.Second,
		Handler:     api.New(cm, f, log.Named("api")),
	}
	defer srv.Close()
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Panic("failed to start API server", zap.Error(err))
		}
	}()
	log.Info("faucet started", zap.Stringer("api", l.Addr()), zap.String("network", n.Name), zap.Stringer("address", wm.Address()), zap.Stringer("tip", cm.Tip()))

	<-ctx.Done()
	return nil
}
