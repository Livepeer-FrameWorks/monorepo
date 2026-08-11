# RFC: Vault Secrets Management

## Status

Draft

## TL;DR

- Replace runtime flat-file secrets (`/etc/frameworks/*.env`) with Vault-backed retrieval.
- Keep local dev on env files.
- Provide optional cached secrets for Vault outages.
- Orthogonal to SOPS — gitops repo continues using SOPS/age for provisioning-time encryption. Vault addresses what happens at runtime on hosts.

## Owning services / modules

The shipped secrets path is owned by the CLI: SOPS/age decryption lives in `cli/pkg/sops`, and the provisioner renders secrets to hosts as part of the layered desired-state bootstrap (layer 5). `pkg/config` owns the runtime env-read layer that every service goes through (with some remaining direct `os.Getenv` call sites under `api_*`). A Vault deployment would run on central infrastructure, provisioned and unsealed by the CLI, with the client integration landing in `pkg/config`.

## Current State

SOPS-rendered static secrets are the shipped path, and there is no intent to supersede this RFC because of that — it remains the open design space for what comes next.

- The gitops repo encrypts secrets with SOPS/age; the CLI decrypts them (`cli/pkg/sops`) and renders them to `/etc/frameworks/*.env` on each host (`chmod 600`) as bootstrap layer 5. Services read them from process environment; local development uses `.env` / `.env.dev` files loaded by `pkg/config`.
- This path is acknowledged as leak-prone: secrets are statically rendered to disk on every host, rotation requires re-provisioning, and every service on a host can read the full env file.
- Runtime secret management needs further iterations. Vault is the leading explored option and will probably be adopted, but no Vault client or Vault provisioning exists in the repo today.
- Most services use `pkg/config` helpers for required/defaulted env reads, but some runtime paths still call `os.Getenv` directly. A Vault migration would need to centralize those reads or provide a process-env injection layer.

Evidence:

- `pkg/config/env.go`
- `config/env/`
- direct env reads under `api_*`

## Problem / Motivation

Flat `.env` files on hosts are not suitable for production runtime secrets management. Rotation requires re-provisioning, there is no audit trail for secret access, and every service on a host can read the full env file. Vault provides centralized runtime secret storage with per-service access control, rotation, and audit trails.

## Goals

- Centralized secrets for production.
- Local dev unchanged.
- Graceful fallback when Vault is unavailable.

## Non-Goals

- Full Vault automation or policy management in this RFC.
- Edge-node Vault access (edge should remain env-only).

## Proposal

- Add a Vault client in `pkg/config` with AppRole auth + caching, or run Vault Agent/template injection and keep service startup env-based.
- Services read secrets through a shared helper or injected process environment; env remains the fallback if Vault is not configured.
- CLI provisioner injects Vault AppRole creds for central/regional services.

## Impact / Dependencies

- `pkg/config` changes and direct `os.Getenv` call sites that read secrets.
- CLI provisioning (`cli/`)
- Deployment docs updates.

## Alternatives Considered

- Vault Agent sidecar (more ops complexity).
- Continue env-only (status quo).

## Risks & Mitigations

- Risk: Vault outage at startup. Mitigation: cached secrets.
- Risk: migration errors. Mitigation: env fallback — if Vault is unreachable, services read from env as before.

## Migration / Rollout

1. Deploy Vault on central infrastructure host, CLI provisions and unseals it.
2. Add Vault client + caching in `pkg/config` with env fallback.
3. Migrate all secrets for central/regional services.
4. Update deployment docs.

## Open Questions

- **Where should Vault live?** Proposed: on central infrastructure with HA storage. If YugabyteDB is the chosen platform database in the target deployment, it may be a candidate backend; otherwise this needs an operator decision.
- **Which secrets migrate?** All production runtime secrets should eventually move (`DATABASE_PASSWORD`, `CLICKHOUSE_PASSWORD`, `STRIPE_SECRET_KEY`, `JWT_SECRET`, `SERVICE_TOKEN`, `FIELD_ENCRYPTION_KEY`, SMTP, Cloudflare, etc.). Migration is not one-line because some services still read environment variables directly.

## References, Sources & Evidence

- `pkg/config`
- `config/env/`
- `website_docs/src/content/docs/operators/deployment-manual.mdx`
- `website_docs/src/content/docs/operators/external-services.mdx`
- `docs/rfcs/service-identity-and-cluster-binding.md` — replaces `SERVICE_TOKEN` as an auth mechanism; Vault stores the signing keys
