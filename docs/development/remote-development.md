# Remote development

The shared `remotedev` VM is for CPU- and memory-heavy compilation, unit tests, and integration
tests that are inconvenient on a laptop. It complements local development; it is not the staging
cluster and is not a deployment target.

The encrypted SSH endpoint lives in the GitOps repository at
`development/hosts.enc.yaml`. Access requires the management VPN, a per-developer key-only SSH
account, the GitOps checkout, and its SOPS age key. Privateer must not run on developer machines.

## First use

```bash
export FRAMEWORKS_GITOPS_DIR=../gitops
scripts/remote-dev.sh sync issue-123
scripts/remote-dev.sh run issue-123 pnpm runtime set node 24 -g
scripts/remote-dev.sh doctor
scripts/remote-dev.sh run issue-123 make test-cli
```

The Node runtime is installed once in the developer's isolated cache. `doctor` rejects a runtime
outside the Node 24 range required by the repository.

Use a distinct slot for each agent conversation or branch. The wrapper validates slot names and
maps each one to `/srv/frameworks-dev/workspaces/<user>/<slot>/monorepo`. Sync removes stale files
inside that slot only. It transfers unpublished commit objects first, checks out the exact local
HEAD, and then mirrors tracked and untracked working-tree changes. Repository ignore rules keep
local dependencies and secrets out of the transfer. Shared Go, pnpm, and Cargo caches live outside
the slots.

Two simultaneous `run` jobs are the enforced limit. A third exits with status 75 so an agent can
retry later. Interactive shells are not counted; do not use them to bypass the limit for heavy
work. Prefer the smallest Make target that exercises the change.

## Common commands

```bash
# Refresh an existing slot with local tracked/untracked changes
scripts/remote-dev.sh sync issue-123

# Run an argv-safe command without an interactive shell
scripts/remote-dev.sh run issue-123 make test-foghorn

# Open a login shell in the slot
scripts/remote-dev.sh shell issue-123

# Print the endpoint and remote path for editor setup
scripts/remote-dev.sh path issue-123
scripts/remote-dev.sh ssh-config
```

For VS Code Remote - SSH, add the printed SSH stanza to `~/.ssh/config`, connect to
`frameworks-remotedev`, and open the path returned by `path`. Do not run a publicly exposed
code-server: the SSH tunnel is the editor transport.

## Security boundary

- No public DNS record, IPv4 port forward, or unsolicited WAN IPv6 is allowed.
- Split DNS for `remotedev.dev.frameworks.network` must resolve only on the management VPN/LAN.
- SSH password authentication is disabled; developers do not share Unix accounts.
- Normal builds use Docker-group access and do not need passwordless sudo.
- A developer SOPS key can reveal deployment material. Distribute and revoke it as a privileged
  operator credential, not as a substitute for an SSH key.
