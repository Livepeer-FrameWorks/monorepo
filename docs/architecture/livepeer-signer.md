# Livepeer wallet signing boundary

Platform 0.3.0 gateways keep a local keystore. The upstream go-livepeer remote signer is **not** a complete signer for our transcode path: it covers orchestrator-info, discovery, and Live-AI payment routes, while marketplace transcoding still creates ticket batches through the gateway's local `pm.Sender`. A gateway configured only with `remote_signer_url` cannot pay orchestrators and cannot reliably transcode.

Remote-signer-only gateways are therefore deferred until the go-livepeer fork gains remote ticket-batch signing. Production provisioning rejects `remote_signer_url` on `livepeer-gateway` and requires a local keystore.

## Current protection

- The gateway HTTP and CLI listeners bind loopback and are reached only through the configured reverse proxy or SSH.
- Transaction routes are absent unless an operator temporarily sets `enable_cli_tx_routes: "true"`; upstream mutation handlers are POST-only and take form bodies.
- The keystore is rendered only for Livepeer key-consuming services. The systemd unit uses a strict sandbox and mode-0600 key/password files.
- Purser owns routine TicketBroker deposit/reserve funding and enforces a durable daily cap. Gateways do not auto-deposit and receive no routine native ETH top-up.
- `frameworks livepeer` reads wallet state directly from Arbitrum. Exceptional reserve/unlock/withdraw operations SSH to the selected gateway and call its loopback CLI; the maintenance flag must be removed afterward.
- Public `/live/` ingest is restricted to POST/PUT, authenticates a Foghorn-signed job capability, binds the assigned edge node and source IP, and rejects client changes to the stored transcode specification.

## Planned remote signer

The `livepeer-signer` service definition remains available for development of the future boundary, but it is not wired to production transcode gateways. Before enabling keyless gateways, go-livepeer must add authenticated ticket-batch signing whose signer independently validates sender, ticket parameters, batch bounds, and policy. Only then may provisioning remove the gateway keystore and synthesize `remote_signer_url`.

When a signer service is provisioned for that future work, current validation already requires:

- native deployment;
- a loopback CLI listener;
- a local keystore, password, and Ethereum RPC;
- an explicit HTTP bind on the host WireGuard address unless an auth webhook is configured;
- `remote_signer_allow_no_auth` disabled; and
- Privateer coverage for the signer host.

## Key files

- `cli/cmd/cluster_provision.go` — custody, listener, mesh, and production validation
- `cli/pkg/provisioner/service_role.go` — native arguments, keystore rendering, and systemd sandbox inputs
- `cli/cmd/livepeer.go` — on-chain reads and SSH-to-loopback maintenance commands
- `api_billing/internal/handlers/livepeer_deposit.go` — Purser funding policy
- `pkg/livepeer/chain` — shared Arbitrum/TicketBroker read client
