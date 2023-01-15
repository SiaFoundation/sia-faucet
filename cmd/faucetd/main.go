package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"go.sia.tech/faucet/api"
	"go.sia.tech/faucet/consensus"
	"go.sia.tech/faucet/faucet"
	"go.sia.tech/faucet/internal/persist/sqlite"
	"go.sia.tech/faucet/wallet"
	mconsensus "go.sia.tech/siad/modules/consensus"
	"go.sia.tech/siad/modules/gateway"
	"go.sia.tech/siad/modules/transactionpool"
	"go.sia.tech/siad/types"
)

var (
	httpAddr     string
	gatewayAddr  string
	dir          string
	maxPerDayStr string
	bootstrap    bool
)

func init() {
	flag.StringVar(&gatewayAddr, "gateway", ":0", "gateway address to listen on")
	flag.StringVar(&httpAddr, "http", ":8080", "HTTP address to listen on")
	flag.StringVar(&dir, "dir", "", "directory to store data in")
	flag.StringVar(&maxPerDayStr, "max", "100SC", "max amount of coins per day")
	flag.BoolVar(&bootstrap, "bootstrap", true, "bootstrap consensus network")
	flag.Parse()
}

func main() {
	log.SetFlags(0)

	walletRecoveryPhrase := os.Getenv("FAUCETD_WALLET_SEED")
	if len(walletRecoveryPhrase) == 0 {
		log.Fatalln("FAUCETD_WALLET_SEED not set")
	}

	walletKey, err := wallet.KeyFromPhrase(walletRecoveryPhrase)
	if err != nil {
		log.Fatalln("failed to parse wallet seed:", err)
	}

	hastings, err := types.ParseCurrency(maxPerDayStr)
	if err != nil {
		log.Fatalln("failed to parse --max:", err)
	}
	var maxPerDay types.Currency
	_, err = fmt.Sscan(hastings, &maxPerDay)
	if err != nil {
		log.Println("failed to conver to currency:", err)
	}

	g, err := gateway.New(gatewayAddr, bootstrap, filepath.Join(dir, "gateway"))
	if err != nil {
		log.Fatalln("failed to create gateway:", err)
	}
	defer g.Close()

	log.Println("gateway started on:", g.Address())

	cs, errCh := mconsensus.New(g, bootstrap, filepath.Join(dir, "consensus"))
	select {
	case err := <-errCh:
		if err != nil {
			log.Fatalln("failed to create consensus:", err)
		}
	default:
		go func() {
			if err := <-errCh; err != nil {
				log.Println("WARNING: consensus initialization returned an error:", err)
			}
		}()
	}
	defer cs.Close()

	cm, err := consensus.NewChainManager(cs)
	if err != nil {
		log.Fatalln("failed to create chain manager:", err)
	}

	tp, err := transactionpool.New(cs, g, filepath.Join(dir, "tpool"))
	if err != nil {
		log.Fatalln("failed to create transaction pool:", err)
	}
	defer tp.Close()

	log.Println("transaction pool loaded...")

	db, err := sqlite.OpenDatabase(filepath.Join(dir, "faucetd.db"))
	if err != nil {
		log.Fatalln("failed to open database:", err)
	}
	defer db.Close()

	ws := sqlite.NewWalletStore(db)
	w, err := wallet.NewSingleAddressWallet(walletKey, cm, tp, ws)
	if err != nil {
		log.Fatalln("failed to create wallet:", err)
	}
	defer w.Close()

	log.Println("Wallet Address:", w.Address())

	fs := sqlite.NewFaucetStore(db)
	f, err := faucet.New(cm, tp, w, fs, maxPerDay, 2*time.Minute, log.Default())
	if err != nil {
		log.Fatalln("failed to create faucet:", err)
	}
	defer f.Close()

	l, err := net.Listen("tcp", httpAddr)
	if err != nil {
		log.Fatalln("failed to listen on http address:", err)
	}
	defer l.Close()

	api := api.New(f)

	go func() {
		if err := api.Serve(l); err != nil {
			log.Fatalln("failed to start API server:", err)
		}
	}()

	log.Println("faucetd API started on", l.Addr().String())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	<-ctx.Done()
}
