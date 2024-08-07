package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"go.sia.tech/faucet/api"
	"go.sia.tech/faucet/consensus"
	"go.sia.tech/faucet/faucet"
	"go.sia.tech/faucet/internal/persist/sqlite"
	"go.sia.tech/faucet/wallet"
	"go.sia.tech/siad/modules"
	mconsensus "go.sia.tech/siad/modules/consensus"
	"go.sia.tech/siad/modules/gateway"
	"go.sia.tech/siad/modules/transactionpool"
	"go.sia.tech/siad/types"
)

var (
	// global flags
	httpAddr    string
	gatewayAddr string
	dir         string
	bootstrap   bool

	// faucet flags
	maxSCPerDayStr    string
	maxRequestsPerDay int
	processInterval   int64
)

var (
	rootCmd = &cobra.Command{
		Use:   "faucetd",
		Short: "Sia faucet daemon",
		Run: func(cmd *cobra.Command, args []string) {
			interval := time.Duration(processInterval) * time.Second
			if interval < 0 {
				log.Fatalln("--interval must be positive")
			}

			walletRecoveryPhrase := os.Getenv("FAUCETD_WALLET_SEED")
			if walletRecoveryPhrase == "" {
				log.Fatalln("FAUCETD_WALLET_SEED not set")
			}

			walletKey, err := wallet.KeyFromPhrase(walletRecoveryPhrase)
			if err != nil {
				log.Fatalln("failed to parse wallet seed:", err)
			}

			hastings, err := types.ParseCurrency(maxSCPerDayStr)
			if err != nil {
				log.Fatalln("failed to parse --max:", err)
			}
			var maxSCPerDay types.Currency
			_, err = fmt.Sscan(hastings, &maxSCPerDay)
			if err != nil {
				log.Println("failed to conver to currency:", err)
			}

			// start the necessary siad modules
			g, err := gateway.New(gatewayAddr, bootstrap, filepath.Join(dir, "gateway"))
			if err != nil {
				log.Fatalln("failed to create gateway:", err)
			}
			defer g.Close()
			log.Println("gateway started on:", g.Address())

			go func() {
				for _, peer := range peers {
					g.Connect(modules.NetAddress(peer))
				}
			}()

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

			tip := cm.Tip()
			log.Printf("Synced to %v (%v)", tip.Index.ID, tip.Index.Height)

			tp, err := transactionpool.New(cs, g, filepath.Join(dir, "tpool"))
			if err != nil {
				log.Fatalln("failed to create transaction pool:", err)
			}
			defer tp.Close()

			db, err := sqlite.OpenDatabase(filepath.Join(dir, "faucetd.db"))
			if err != nil {
				log.Fatalln("failed to open database:", err)
			}
			defer db.Close()

			// initialize the wallet
			ws := sqlite.NewWalletStore(db)
			w, err := wallet.NewSingleAddressWallet(walletKey, cm, tp, ws)
			if err != nil {
				log.Fatalln("failed to create wallet:", err)
			}
			defer w.Close()

			log.Println("Wallet Address:", w.Address())

			// initialize the faucet
			fs := sqlite.NewFaucetStore(db)
			log.Printf("faucet started with interval %s", interval)
			f, err := faucet.New(cm, tp, w, fs, maxRequestsPerDay, maxSCPerDay, interval, log.Default())
			if err != nil {
				log.Fatalln("failed to create faucet:", err)
			}
			defer f.Close()

			// start the listener
			l, err := net.Listen("tcp", httpAddr)
			if err != nil {
				log.Fatalln("failed to listen on http address:", err)
			}
			defer l.Close()
			log.Println("faucetd API started on", l.Addr().String())

			// start the API handler in a separate goroutine
			api := api.New(f)
			go func() {
				if err := api.Serve(l); err != nil {
					log.Println("failed to start API server:", err)
				}
			}()

			ch := make(chan os.Signal, 1)
			signal.Notify(ch, os.Interrupt)
			<-ch
		},
	}

	seedCmd = &cobra.Command{
		Use:   "seed",
		Short: "generates a new wallet seed for use with the faucet. Should not be used while the daemon is running.",
		Run: func(cmd *cobra.Command, args []string) {
			log.Println(wallet.NewSeedPhrase())
		},
	}

	// distributeCmd is a helper that can be used to create a bunch of UTXOs
	// in the faucet wallet. Should not be used while the daemon is running.
	distributeCmd = &cobra.Command{
		Use:   "distribute [count] [amount]",
		Short: "redistributes wallet funds; creating [count] UTXOs of [amount] SC. Should not be used while the daemon is running.",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			hastings, err := types.ParseCurrency(args[1])
			if err != nil {
				log.Fatalln("failed to parse --max:", err)
			}
			var utxoValue types.Currency
			_, err = fmt.Sscan(hastings, &utxoValue)
			if err != nil {
				log.Println("failed to conver to currency:", err)
			} else if utxoValue.IsZero() {
				log.Fatalln("amount must be > 0")
			}

			count, err := strconv.Atoi(args[0])
			if err != nil {
				log.Fatalln("failed to parse count:", err)
			} else if count <= 0 {
				log.Fatalln("count must be > 0")
			}

			walletRecoveryPhrase := os.Getenv("FAUCETD_WALLET_SEED")
			if walletRecoveryPhrase == "" {
				log.Fatalln("FAUCETD_WALLET_SEED not set")
			}

			walletKey, err := wallet.KeyFromPhrase(walletRecoveryPhrase)
			if err != nil {
				log.Fatalln("failed to parse wallet seed:", err)
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

			tip := cm.Tip()
			log.Printf("Synced to %v (%v)", tip.Index.ID, tip.Index.Height)
			time.Sleep(time.Minute)
			if len(g.Peers()) == 0 {
				log.Fatalln("no peers connected")
			}
			log.Printf("Gateway connected to %v peers", len(g.Peers()))

			tp, err := transactionpool.New(cs, g, filepath.Join(dir, "tpool"))
			if err != nil {
				log.Fatalln("failed to create transaction pool:", err)
			}
			defer tp.Close()

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
			log.Printf("Creating %v UTXOs of %v SC each", count, utxoValue.HumanString())

			distributeTxn := types.Transaction{
				MinerFees: []types.Currency{types.SiacoinPrecision},
			}
			for i := 0; i < count; i++ {
				distributeTxn.SiacoinOutputs = append(distributeTxn.SiacoinOutputs, types.SiacoinOutput{
					Value:      utxoValue,
					UnlockHash: w.Address(),
				})
			}
			fundAmount := types.SiacoinPrecision.Add(utxoValue.Mul64(uint64(count)))
			toSign, release, err := w.FundTransaction(&distributeTxn, fundAmount)
			if err != nil {
				log.Fatalln("failed to fund transaction:", err)
			}
			defer release()

			if err := w.SignTransaction(&distributeTxn, toSign, types.FullCoveredFields); err != nil {
				log.Fatalln("failed to sign transaction:", err)
			} else if err := tp.AcceptTransactionSet([]types.Transaction{distributeTxn}); err != nil {
				log.Fatalln("failed to broadcast transaction:", err)
			}
			log.Println("Broadcast transaction", distributeTxn.ID())
			log.Println("Waiting for transaction to be confirmed...")
			// wait for the transaction to be confirmed
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					log.Fatalln("timed out waiting for transaction to be confirmed")
				case <-time.After(time.Second):
					ok, err := tp.TransactionConfirmed(distributeTxn.ID())
					if err != nil {
						log.Fatalln("failed to check transaction status:", err)
					} else if ok {
						log.Println("Transaction confirmed")
						return
					}
				}
			}
		},
	}
)

func init() {
	log.SetFlags(0)

	rootCmd.PersistentFlags().StringVarP(&dir, "dir", "d", "", "directory to store data in")
	rootCmd.PersistentFlags().StringVar(&gatewayAddr, "gateway", ":0", "gateway address to listen on")
	rootCmd.PersistentFlags().StringVar(&httpAddr, "http", ":8080", "HTTP address to listen on")
	rootCmd.PersistentFlags().BoolVar(&bootstrap, "bootstrap", true, "bootstrap blockchain")

	rootCmd.Flags().StringVar(&maxSCPerDayStr, "max-sc", "100SC", "max amount of SC per IP/address per day")
	rootCmd.Flags().IntVar(&maxRequestsPerDay, "max-requests", 5, "max number of requests per IP/address per day")
	rootCmd.Flags().Int64Var(&processInterval, "interval", 120, "interval, in seconds, to process requests")

	rootCmd.AddCommand(distributeCmd, seedCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalln(err)
	}
}
