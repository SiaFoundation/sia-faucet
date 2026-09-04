---
default: major
---

# Replace walletd with an embedded wallet

`faucetd` now syncs the chain itself and stores wallet UTXOs locally. The `walletd.address`, `walletd.password`, and `walletd.wallet` flags were removed and `network` and `syncer` were added.
