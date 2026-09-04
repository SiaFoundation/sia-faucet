# Changelog
## 0.1.0 (2026-09-04)

### Breaking Changes

- Initial Release

#### Replace walletd with an embedded wallet

`faucetd` now syncs the chain itself and stores wallet UTXOs locally. The `walletd.address`, `walletd.password`, and `walletd.wallet` flags were removed and `network` and `syncer` were added.

### Features

- Prune blocks older than one week from the consensus database

#### Add instant sync

When the consensus database does not exist, `faucetd` starts from the wallet address checkpoint reported by the explorer instead of syncing from genesis. Disable with `-instant=false`.