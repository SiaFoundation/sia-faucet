package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/faucet/api"
	"go.sia.tech/faucet/faucet"
	"go.sia.tech/faucet/internal/persist/sqlite"
	wapi "go.sia.tech/walletd/v2/api"
	"go.uber.org/zap"
)

// run starts the faucet daemon. It blocks until the context
// is canceled or an error occurs.
func run(ctx context.Context, signingKey types.PrivateKey, log *zap.Logger) error {
	store, err := sqlite.OpenDatabase(filepath.Join(dir, "faucetd.sqlite3"))
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer store.Close()

	client := wapi.NewClient(walletdAPIAddr, walletdAPIPassword)
	cs, err := client.ConsensusTipState()
	if err != nil {
		return fmt.Errorf("failed to connect to walletd: %w", err)
	}

	wc := client.Wallet(walletdWalletID)
	balance, err := wc.Balance()
	if err != nil {
		return fmt.Errorf("failed to connect to walletd: %w", err)
	}

	// initialize the faucet
	f, err := faucet.New(store, signingKey, client, wc, maxRequestsPerDay, maxSCPerDay, processInterval, log.Named("faucet"))
	if err != nil {
		return fmt.Errorf("failed to create faucet: %w", err)
	}
	defer f.Close()

	// start the listener
	l, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on http address: %w", err)
	}
	defer l.Close()

	s := http.Server{
		ReadTimeout: 30 * time.Second,
		Handler:     api.New(f, log.Named("api")),
	}
	defer s.Close()
	go func() {
		if err := s.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Panic("failed to start API server", zap.Error(err))
		}
	}()
	log.Info("faucet started", zap.Duration("interval", processInterval), zap.Stringer("api", l.Addr()), zap.String("network", cs.Network.Name), zap.Stringer("balance", balance.Siacoins))

	<-ctx.Done()
	return nil
}
