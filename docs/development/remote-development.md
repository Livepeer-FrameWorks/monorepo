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
is how every change is exercised end to end before it goes to staging: placement,
Livepeer live and VOD, recording, clips, cross-cell playback, the API and SDKs, node lifecycle,
reconnects, control-plane replicas and edge outages. It is too heavy for a laptop and is not a CI
job; run it on a slot:

```bash
scripts/remote-dev.sh sync stack-1
scripts/remote-dev.sh run stack-1 bash -lc 'STACK_SLOT=1 MIST_SOURCE_DIR=/srv/frameworks-dev/workspaces/$USER/mist-next make verify-stack'
```

- `STACK_SCENARIOS` selects `default`, `manual` (long outage and load scenarios), `all`, or numbers
  such as `01,04`.
- `STACK_REPEAT=N` runs each selected scenario N times, recording every attempt and its own log
  verdict. Use it to reproduce intermittent races; a failed attempt stays failed.
- `MIST_SOURCE_DIR=<checkout>` builds MistServer from a local fork instead of the pinned release.
  The pinned archive may lag the Mist version under test; always record the Mist commit for a
  release candidate run.
- `STACK_KEEP=1` leaves the slot running for inspection; `STACK_REUSE=1` runs scenarios against a
  slot that is already up. `make stack-down` removes it.
- `STACK_SLOT=N` runs independent slots side by side on the same host.

The manual Mist processing stress wrapper repeats the ten-clip scenario against
the real stack under bounded CPU load. It checks each repetition's logs as well
as the final log window:

```bash
scripts/remote-dev.sh run stack-1 bash -lc 'STACK_SLOT=1 STACK_REUSE=1 STACK_KEEP=1 MIST_STRESS_RUNS=4 scripts/stack/mist-stress.sh'
```

`MIST_STRESS_RUNS` defaults to 4 and `MIST_STRESS_CPU_WORKERS` to 2. Pass
`MIST_SOURCE_DIR` when bringing up a fresh slot; for `STACK_REUSE=1`, the
existing slot's Mist build is used. A failed stack run keeps its compose logs
under the slot's `.stack/` directory for diagnosis.

Scenario 27 uses real API traffic, Periscope reports, operator billing commands,
and billing PDF downloads. Its stack-only Go driver expires the disposable
tenant's returned prepaid phase at the last closed metering window, calls
Purser's production finalizer twice at the following UTC midnight, and checks
one statement and a full calendar month of advancement. Issued documents, usage timestamps, service
clocks, and other tenants' metering remain intact. `make verify-stack` builds
the driver with `make build-stack-billing-runner`; it is never part of Purser.

Manual scenario 29 stalls two response-header attempts for one clip's sprite
PUT while the transparent S3 proxy forwards sibling files to the real store.
It checks sibling completion, a successful retry within 30 seconds, thumbnail
publication, and playback. `make verify-stack-fixtures` checks proxy forwarding
and fault isolation. The fault is cleared on scenario exit.

## Requested post-rollout staging smoke

After the maintainer requests staging verification, run the same scenario
scripts with `STACK_TARGET=staging` from an operator checkout on the private
network. The target path does not provision or tear down staging. It runs the
selected scenarios against the deployed service endpoints and reads native
service journals through SSH. Keep endpoint credentials, the test tenant, and
the service map in private operator files:

```bash
STACK_TARGET=staging \
STACK_ENDPOINTS_FILE=/private/path/staging-stack-endpoints.sh \
STAGING_SERVICE_MAP_FILE=/private/path/staging-service-map.txt \
make verify-stack
```

The endpoint file exports the Bridge and every Foghorn replica URL,
`EDGE_A_RTMP`/`EDGE_B_RTMP`, the isolated test tenant's ID and API token,
database and ClickHouse connections, the webhook receiver, mailbox access,
and `STACK_CLI_CONTEXT`/`STACK_CLI_MANIFEST`. Set `STACK_MAIL_MODE=imap`,
`STACK_TEST_EMAIL_BASE`, and `STACK_MAIL_IMAP_HOST`/`USER`/`PASSWORD` when
staging sends verification mail through its real SMTP provider. The inbox
address receives unique plus-addressed test signups. Set `STACK_SMTP_PORT` to
the staging SMTP port; the signup outbox check briefly rejects that port on
Commodore's host and removes its own rules after the retry begins. The service
map contains
`scenario-service|ssh-host|systemd-unit` rows; repeat an alias across hosts
for a replicated service that a fault scenario must stop together. For example,
the Livepeer gateway alias maps to all three gateway hosts of its cell, while
`foghorn` and `foghorn-2` map to individual EU replicas. Include every service
whose logs the smoke must scan. The SSH user needs journal read access and
noninteractive `systemctl` access for the selected restart and outage checks.
Set `STACK_EDGE_A_LOCATION_PREFIX` to the EU edge's public scheme and
authority (including its trailing slash) so remote-replica checks recognize
the deployed playback redirect.

For billing scenario 27, export `STACK_BILLING_DATABASE_URL` for Purser's
tenant-scoped fixture writes, `STACK_BILLING_QM_ADDR`, `SERVICE_TOKEN`, the
deployed `SUPPLIER_*` identity, and Quartermaster client TLS settings
(`GRPC_TLS_CA_PATH` and `QUARTERMASTER_GRPC_TLS_SERVER_NAME`). The driver runs
on the operator runner against those same services. For manual scenario 29,
`STACK_THUMBNAIL_FAULT_URL` must reach a private fixture proxy used by the
isolated test tenant's storage; it must not reroute shared staging storage.
Without that fixture, 29 reports BLOCKED.

`STACK_SCENARIOS` and `STACK_REPEAT` have the same meaning in both target
modes. The staging runner keeps its assertion evidence under a private
`.stack/staging-...` directory and returns 1 for a failed assertion or log
signature and 2 for a blocked check. A missing endpoint, inaccessible journal,
or unavailable test fixture is not a green staging result. Do not run this
target before a requested rollout verification.

## Security boundary

- No public DNS record, IPv4 port forward, or unsolicited WAN IPv6 is allowed.
- Split DNS for `remotedev.dev.frameworks.network` must resolve only on the management VPN/LAN.
- SSH password authentication is disabled; developers do not share Unix accounts.
- Normal builds use Docker-group access and do not need passwordless sudo.
- A developer SOPS key can reveal deployment material. Distribute and revoke it as a privileged
  operator credential, not as a substitute for an SSH key.
