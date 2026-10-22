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
- `STACK_SLOT=N` runs independent slots side by side on the same host. A slot number belongs to
  one workspace at a time on the host's Docker engine: `make verify-stack`, `make stack-up`, and
  `make stack-down` from another workspace refuse a slot that is up or coming up elsewhere and name
  the workspace holding it. Pick a different `STACK_SLOT`, or free it from that workspace.

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

Default scenario 36 arms the same proxy's expiry fixture for one cross-cell
VOD: every presigned GET of it signed before the arming second answers 403
"Request has expired", as S3 does past `X-Amz-Expires`. The cell-B edge then
has to re-resolve the resolution it cached on the first play and answer
Mist's source HEAD with 200. The fixture is cleared on scenario exit.

Default scenario 38 arms the proxy's stall fixture for one cross-cell VOD: the
first ranged media read stops after 64 KiB and holds the connection. The cell-B
relay has to resume from inside the stalled range before Mist's 5 second read
timeout, so edge B never logs `Timeout while retrieving` and every variant
plays the full duration.

Manual scenario 09 stages an edge release inside the stack. It packs edge A's
image-baked Helmsman binary and Mist tree as release artifacts under new
version labels and serves them from the runner. It then writes the release and
cell A's release target into Quartermaster's catalog tables. While a stream is
live, it asserts that `foghorn.node_components` reaches both target versions,
that the update ends idle with the node in normal mode, and that the stream
keeps its publisher, ingest generation and media. Cleanup removes the target
and restores the edge's image-seeded version labels.

The stack's edges reach Foghorn in plaintext by single-label service names.
Scenario 30 switches edge A's Helmsman to the production shape: TLS to a
dotted cell name, `foghorn.demo-media.stack.frameworks.network:18029`, verified
with the slot's stack CA. That name resolves to both cell-A Foghorn instances.
Foghorn's own external listener gets TLS certificates only from Navigator,
which the stack does not run, so each instance has an nginx front
(`foghorn-tls-a-*`). The front serves a stack-CA certificate for the cell
wildcard and forwards the stream to its instance. The scenario asserts
registration, failover when the holding instance and its front are killed,
and that the connection and a live stream survive. At the end it restores the
plaintext addresses.

## Requested post-rollout staging smoke

Staging is verified with the same scenarios, sweep and log check as the stack.
After the maintainer requests staging verification, run from an operator
checkout on the private network with the GitOps checkout beside it:

```bash
STACK_TARGET=staging make verify-stack
```

Nothing else is configured by hand. `scripts/stack/verify-target.sh` builds the
CLI from source and runs `frameworks --context staging cluster stack-target`,
which derives the run's target from the cluster manifest, its edge manifest,
the bootstrap overlay and their SOPS files: the public Bridge and per-cell
Foghorn endpoints, each cell's edge, the service-to-host-and-unit map for
journal reads, and how each database and ClickHouse is read over SSH (with the
owning service's credentials, read-only, never copied off the host). The run
then logs in as the bootstrap overlay's platform operator, mints a one-day API
token for its own test streams, serves the webhook receiver for the run from
the development host (Bosun on staging reaches it), and on exit deletes its
webhook endpoint, stops the receiver and revokes the token.

Only scenarios whose header declares `# staging: safe` run on staging. Every
scenario declares it; one that restarts, stops, pauses or kills a service,
injects network or storage faults, or reconfigures a host is
`# staging: unsafe, <what it does>`. `STACK_SCENARIOS=default` (or `manual`,
`all`) selects only the safe ones, and naming an unsafe scenario refuses the
run before anything touches the cluster. The fault scenarios run on the stack.

On staging the run's whole-window log check also fails on any service line
logged at error level, since no safe scenario causes one on purpose. The sweep
reads the doctor (`cluster doctor --deep`, including the Kafka quorum and host
crash checks), the analytics, API, artifact and DVR invariants, and lists both
edges' storage for files of artifacts deleted during the run.

Optional overrides: `STACK_TARGET_CLUSTER` (default `staging`),
`FRAMEWORKS_GITOPS_DIR` (default `../gitops`), `STACK_CLI_CONTEXT` (default
`staging`), `WEBHOOK_RECEIVER_URL` for another receiver, and
`STACK_ENDPOINTS_FILE`, sourced after the derived target. `STACK_SCENARIOS`
and `STACK_REPEAT` mean the same in both target modes. Evidence stays under
`.stack/staging-...`; the runner returns 1 for a failed assertion or log line
and 2 for a blocked check. A blocked check is not a green staging result. Do
not run this target before a requested rollout verification.

## Security boundary

- No public DNS record, IPv4 port forward, or unsolicited WAN IPv6 is allowed.
- Split DNS for `remotedev.dev.frameworks.network` must resolve only on the management VPN/LAN.
- SSH password authentication is disabled; developers do not share Unix accounts.
- Normal builds use Docker-group access and do not need passwordless sudo.
- A developer SOPS key can reveal deployment material. Distribute and revoke it as a privileged
  operator credential, not as a substitute for an SSH key.
