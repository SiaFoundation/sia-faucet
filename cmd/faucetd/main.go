package main

import (
	"context"
	"os"
	"os/signal"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/wallet"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"lukechampine.com/flagg"
)

var (
	// global flags
	dir        string
	httpAddr   string
	syncerAddr string
	network    string
	instant    bool
	logLevel   = zap.NewAtomicLevelAt(zap.InfoLevel)

	// faucet flags
	maxSCPerDay       = types.Siacoins(100)
	maxRequestsPerDay = 5
)

func main() {
	rootCmd := flagg.Root
	rootCmd.StringVar(&dir, "dir", "", "directory to store data in")
	rootCmd.StringVar(&httpAddr, "http", ":8080", "HTTP address to listen on")
	rootCmd.StringVar(&syncerAddr, "syncer", ":9981", "address to listen on for peer connections")
	rootCmd.StringVar(&network, "network", "zen", "network to connect to (mainnet or zen)")
	rootCmd.BoolVar(&instant, "instant", true, "sync from a recent checkpoint when the consensus database does not exist")
	rootCmd.TextVar(&logLevel, "log", logLevel, "log level")
	rootCmd.TextVar(&maxSCPerDay, "max.sc", maxSCPerDay, "max amount of SC per IP/address per day")
	rootCmd.IntVar(&maxRequestsPerDay, "max.requests", maxRequestsPerDay, "max number of requests per IP/address per day")

	cmd := flagg.Parse(flagg.Tree{
		Cmd: rootCmd,
	})

	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.RFC3339TimeEncoder
	cfg.EncodeDuration = zapcore.StringDurationEncoder
	cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder

	cfg.StacktraceKey = ""
	cfg.CallerKey = ""
	core := zapcore.NewCore(zapcore.NewConsoleEncoder(cfg), zapcore.Lock(os.Stderr), logLevel)
	log := zap.New(core)
	defer log.Sync()

	switch cmd {
	case rootCmd:
		switch {
		case maxSCPerDay.IsZero():
			log.Fatal("max.sc must be nonzero")
		case maxRequestsPerDay <= 0:
			log.Fatal("max.requests must be positive")
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		var seed [32]byte
		if err := wallet.SeedFromPhrase(&seed, os.Getenv("FAUCETD_RECOVERY_PHRASE")); err != nil {
			log.Fatal("failed to derive seed from recovery phrase", zap.Error(err))
		}
		signingKey := wallet.KeyFromSeed(&seed, 0)

		if err := run(ctx, signingKey, log); err != nil {
			log.Fatal("faucetd failed", zap.Error(err))
		}
	default:
		log.Fatal("unexpected command")
	}
}
