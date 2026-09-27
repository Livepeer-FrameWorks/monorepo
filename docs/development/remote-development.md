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

## Production-shaped stack before staging

`make verify-stack` brings up two media cells with two Foghorn replicas each, the edge bundle
(Caddy, MistServer and Helmsman), offchain Livepeer, a webhook receiver and Mailpit, runs the
scenarios in `scripts/stack/scenarios/`, and checks every container log for defect signatures. It
is how a change is exercised end to end before a release candidate goes to staging: placement,
Livepeer live and VOD, recording, clips, cross-cell playback, the API and SDKs, node lifecycle,
reconnects, control-plane replicas and edge outages. It is too heavy for a laptop and is not a CI
job; run it on a slot:

```bash
scripts/remote-dev.sh sync stack-1
scripts/remote-dev.sh run stack-1 bash -lc 'STACK_SLOT=1 make verify-stack'
```

- `STACK_SCENARIOS` selects `default`, `manual` (long outage and load scenarios), `all`, or numbers
  such as `01,04`.
- `MIST_SOURCE_DIR=<checkout>` builds MistServer from a local fork instead of the pinned release.
- `STACK_KEEP=1` leaves the slot running for inspection; `STACK_REUSE=1` runs scenarios against a
  slot that is already up. `make stack-down` removes it.
- `STACK_SLOT=N` runs independent slots side by side on the same host.

## Security boundary

- No public DNS record, IPv4 port forward, or unsolicited WAN IPv6 is allowed.
- Split DNS for `remotedev.dev.frameworks.network` must resolve only on the management VPN/LAN.
- SSH password authentication is disabled; developers do not share Unix accounts.
- Normal builds use Docker-group access and do not need passwordless sudo.
- A developer SOPS key can reveal deployment material. Distribute and revoke it as a privileged
  operator credential, not as a substitute for an SSH key.
