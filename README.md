# faucetd

`faucetd` is a simple faucet daemon for the Sia network. It is primarily used
for the Zen testnet.

## Building

`faucetd` uses [mattn/sqlite3](https://github.com/mattn/go-sqlite3#go-sqlite3)
for its database. This requires setting  `CGO_ENABLED=1` and installing a gcc
toolchain. 

```go
CGO_ENABLED=1 go build -o bin/ ./cmd/faucetd
```

## Usage

```sh
FAUCETD_WALLET_SEED=... faucetd -d ~/faucted --max-sc 100SC --max-requests 5
```

## Docker

There is also a docker image available at `ghcr.io/siafoundation/faucetd`