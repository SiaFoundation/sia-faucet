---
default: minor
---

# Add instant sync

When the consensus database does not exist, `faucetd` starts from the wallet address checkpoint reported by the explorer instead of syncing from genesis. Disable with `-instant=false`.
