# Livepeer Signer - Keystore-Backed Remote ETH Signer

The Livepeer Signer is our go-livepeer fork running in remote-signer mode
(`-remoteSigner`, port 18016). It is the only process that holds the Livepeer
gateway wallet's ETH keystore; gateways sign nothing themselves and instead call the
signer over HTTP for every transaction and ticket signature.

## Why the gateway holds no key custody

Livepeer gateways are horizontally scaled, regionally paired, and rolled frequently
(canary + region-stagger update strategy). Placing the ETH key on every gateway host
would multiply the custody surface by the fleet size and couple key handling to
routine rollouts. Instead:

- One signer per gateway wallet holds the keystore; gateways get
  `remote_signer_url` (`LIVEPEER_REMOTE_SIGNER_URL`) plus the public
  `eth_acct_addr` and no keystore material.
- One physical gateway pool can serve multiple media clusters (Quartermaster
  synthesizes `public_host` per cluster in `DiscoverServices`); the signer stays a
  singleton control-plane concern regardless of gateway topology.
- Purser's deposit monitor credits tenant deposits by reading `wallet_address` from
  the gateway's service-registry metadata — it needs the address, never the key.

## Provisioning

The CLI provisioner deploys the signer like any native/Docker service from the
release manifest (`ghcr.io/livepeer-frameworks/go-livepeer`), with signer-specific
handling in `serviceNativeVars` / `livepeerNativeArgs`:

- **Keystore materialization**: `LIVEPEER_ETH_KEYSTORE_B64` (SOPS-sourced) is
  decoded to `{state_dir}/keystore/key.json` mode 0600;
  `LIVEPEER_ETH_KEYSTORE_PASSWORD` becomes `{state_dir}/eth-password` mode 0600 and
  is passed as `-ethPassword`. The env vars are deleted after file rendering so the
  unit environment never carries key material. An operator-managed
  `keystore_path` (e.g. `/etc/frameworks/livepeer-signer-keystore`) is also
  supported.
- **Required external env**: an ETH RPC (`eth_url`, typically
  `ARBITRUM_RPC_ENDPOINT` / `LIVEPEER_ETH_URL` from shared env; an RPC pool is
  distributed across instances by host index).
- **Rollout**: `livepeer-signer` is a singleton in the update-strategy registry
  (`max_unavailable=1`); servicedef `18016`, health `/status` over HTTP.
- Not in the dev compose — dev runs without on-chain settlement.

## Key Files

- `pkg/servicedefs/servicedefs.go` - service definition (port 18016, `/status`)
- `cli/pkg/provisioner/service_role.go` - keystore/password file rendering, `-remoteSigner` args
- `cli/cmd/cluster_provision.go` - env normalization (`normalizeLivepeerEnvVars`), RPC pool
- `cli/examples/cluster-dev.yaml` - reference manifest entries for signer + gateway
- `docs/architecture/bootstrap-desired-state.md` - gateway `wallet_address` registry requirement

## Gotchas

- The signer and gateway are the same binary; only the flag set differs. Upgrading
  one via release manifests upgrades the artifact both roles resolve.
- A gateway configured with both a keystore and `remote_signer_url` defeats the
  custody split — production gateways must carry no keystore env at all.
- `validateGatewayMeshCoverage` requires Privateer on every gateway host (the auth
  webhook resolves `foghorn.internal`); the signer has no such mesh requirement of
  its own beyond normal service TLS.
