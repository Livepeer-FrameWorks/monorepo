Feature: Frameworks CLI — Unified Operator Tool

Vision

- Single binary that manages the entire FrameWorks stack: Edge nodes, control‑plane (Quartermaster/Foghorn/Decklog/Gateway), analytics, billing, incidents, and infra (VPC/provisioned
tiers).
- Works in varied network realities (edge cannot reach Quartermaster); supports local, SSH and optional Gateway-only flows.
- Safe, repeatable, auditable operations; minimal host dependencies; strong defaults with opt‑in advanced modes.

Core Capabilities

- Edge lifecycle: provision, enroll, operate, update, verify, and tune host OS for performance.
- Control‑plane ops: registry admin, health, discovery, routing maintenance (drain/undrain).
- Infra provisioning: create/update/destroy central-tier compute and VPC/network per enterprise customer.
- Build and deployment: pull signed prebuilt images by default; optional workspace mode to clone/build from source (Mist/Helmsman/etc).
- Observability: health and status via Quartermaster/Gateway; local diagnostics and logs.
- Connectivity-aware execution: run locally or via SSH on hosts with required network access.

Contexts & Execution

- Contexts: dev/staging/prod profiles (endpoints, auth, policies).
- Executors:
    - local: run commands on current host (edge ops, compose, OS checks).
    - ssh user@host: execute on remote hosts that can reach private services (e.g., Quartermaster).
- Output: human/readable by default; --json for scripting; --verbose for diagnostics.

Auth Model

- JWT (Gateway login): tenant-facing, read ops, and user flows.
- SERVICE_TOKEN: provider/service admin operations (e.g., bootstrap token mint).
- Per-command policy: prefer JWT; fallback to SERVICE_TOKEN when allowed; reject if neither fits the op.

Edge Lifecycle (isolated services, Compose)

- Preflight: DNS A/AAAA for EDGE_DOMAIN, ports 80/443 reachable, time sync; Docker/compose presence; disk space; /dev/shm viability; OS sysctls (rmem/wmem, somaxconn, ip_local_port_range),
ulimits (nofile).
- Tune: print/apply recommended sysctl/limits; root gated with --write.
- Init: generate .edge.env, docker-compose.edge.yml, and Caddyfile templates with sane defaults (Mist shm_size=1g; nofile=1,048,576).
- Enroll: start stack (Caddy+Mist+Helmsman); Helmsman enrolls via Foghorn; waits for ConfigSeed; verify HTTPS (Caddy).
- Status: local container health, cert expiry, DNS correctness; remote health via Quartermaster/Gateway if reachable.
- Update: compose pull && up -d (MVP); later add drain/undrain (Foghorn) + idle gating.
- Certs: Caddy auto ACME (HTTP‑01) with persistent volumes; CLI checks expiry and can force reload.
- Logs: tail per service; filter/time-range.

Control‑Plane Ops

- Quartermaster (provider):
    - Tokens: create/list/revoke bootstrap tokens (edge/service) with TTL, constraints.
    - Health: list service instance health (global/filtered).
    - Discovery: list service instances by type/cluster (health-aware later).
    - Nodes/clusters/services: list/update (admin only).
- Foghorn:
    - Drain/undrain nodes (fast‑follow), route visualization, connection counts.
- Gateway/GraphQL:
    - login for JWT; read-only discovery/health for tenant admins.

Interfaces (Web Applications)

- Website (marketing site) and WebApp (operator/tenant UI) are part of the platform scope.
  - Dev convenience (CLI runs locally):
    - web:dev up|down — run the SvelteKit app (website_application) with Node/npm.
    - marketing:dev up|down — run the marketing website.
  - Future deploy (optional):
    - Build and publish static assets or container images for the web interfaces.
    - Integrate with infra provisioning for hosting (CDN/object storage/static hosting) when central tier is enabled.
  - Non‑goal for MVP: automated production deployments from the CLI (track as fast‑follow if desired).

Decision (scope & CLI fit)

- Include web interfaces in CLI as developer helpers first (local dev up/down), not as production deploys.
- Treat interfaces as an "Interfaces" catalog group, selectable like other services in plan mode later.
- Default central profiles exclude interfaces; offer an optional profile variant including interfaces once hosting is wired.
- Production delivery remains CI/CD‑driven; CLI may prepare infra + artifacts, but does not replace release pipelines.

Infra Provisioning (central tier, enterprise)

- Plan/apply/destroy infrastructure (fast‑follow):
    - VPC, subnets, NAT, routing, security groups.
    - Central-tier services (Quartermaster/Foghorn/Decklog/Periscope/Gateway) on managed instances or k8s.
- Execution drivers: local, SSH to control-plane host, or CI runner (for auditability).
- Config as code: Terraform (preferred); param overlays per tenant/account.

Build & Deploy Modes

- Default (prod): pull signed, version‑pinned images (tags/digests) for Mist/Helmsman/Caddy/etc; optional cosign verification; channels (stable/canary).
- Workspace mode (advanced):
    - Clone monorepo and optional Mist repo; local dev compose; developer tooling.
    - Build Helmsman (Go), Mist (WITH_AV toggle, CPU tuning), and override compose to use local tags.
    - Licensing/capability warnings when enabling AV/codec features.
- Offline package (future): bundle images + compose into a tarball for airgapped sites, with docker load support.

Host Tuning Targets

- Shared memory: enforce --shm-size=1g for Mist container (configurable).
- Ulimits: nofile ≥ 1,048,576 at system and per service (Mist/Helmsman).
- Network buffers: increase rmem_max/wmem_max, udp_{r,w}mem_min, somaxconn, widen ip_local_port_range.
- Optional: fq + bbr if supported; swap off or tuned swappiness.
- CLI applies via sysctl/limits files (with --write), or prints exact commands.

Security & Compliance

- Image provenance: tag/digest pinning; optional cosign verification and SBOM extraction.
- Secrets: JWT and tokens stored 0600; redact in logs; consider OS keychain later.
- RBAC via control-plane; CLI does not bypass policy, respects context and tokens.

Developer UX

- Config precedence: flags > env > ~/.frameworks/config > .edge.env.
- Completions: installable for bash/zsh/fish.
- Dry-run/yes: --dry-run, --yes, interactive confirmations.
- Diagnostics: edge doctor aggregates checks; hint-based remediation.

Phased Delivery

- MVP
    - Edge: preflight, tune, init, enroll, status, update, logs, cert checks (Caddy), compose templates, .edge.env.example.
    - QM: tokens create/list/revoke (SERVICE_TOKEN/JWT), health/discovery reads.
    - Auth/contexts: Gateway login, SERVICE_TOKEN detection; local + SSH executors for commands needing Quartermaster access.
- Fast‑Follow
    - Foghorn drain/undrain endpoints + edge update with drain→idle→update→undrain.
    - Health-aware filters in discovery; GraphQL exposure (already partially done).
    - Workspace mode (clone/build) with Mist WITH_AV support; developer dev up.
    - Optional: cosign verification and SBOM export checks.
- Later
    - Infra provisioning (Terraform): infra plan/apply/destroy with context overlays.
    - Incidents service integration: subscribe to service health events; create silences; status page hooks.
    - Offline package builder for airgapped installs.
    - Keychain integration for secret storage; SSH/k8s executors generalized; policy constraints.

Non‑Goals (Now)

- Full agent running on edge (not required; compose/CLI are sufficient).
- Automated DNS (Cloudflare) and DNS‑01 (will come later for provider‑managed zones).
- Replacing Prometheus/Alertmanager for metrics/alerts (Quartermaster tracks liveness; Prometheus remains the deep observability system).

This captures the end-state vision and phased path: one CLI for tenants and providers, supporting local/SSH execution, simple and safe edge rollouts by default, and powerful workspace
builds and infra provisioning as your needs grow.


Implementation Status (MVP Tracking)

- Done
  - CLI skeleton: root, menu, grouped subcommands (edge, services, context, login, version)
  - Contexts: config load/save; init/list/use/show/set-url; localhost defaults for all services
  - Interactive menu: structured top-level menus and submenus (stubs call through to grouped commands)
  - Context check: reachability (HTTP /health and gRPC health) with timeout and JSON output
  - Edge templates: generate `.edge.env`, `docker-compose.edge.yml`, and `Caddyfile` from current context
  - Edge preflight: DNS (optional), Docker/compose presence, Linux sysctls, /dev/shm, ulimit, ports 80/443
  - Edge tune: dry‑run preview files or apply to system paths with --write
  - Services planner: per‑service fragments + `plan.yaml`; `services plan --interactive` for checkbox-like selection
  - Services ops: `services up|down|status|logs` merging fragments; `--only` filtering
  - Quartermaster wired: `services health` and `services discover` call QM endpoints
  - Basic SSH executor: `--ssh user@host` for services and edge flows (executes `docker compose` remotely)
  - Auth: `frameworks login` stores JWT and/or SERVICE_TOKEN in current context (Gateway /auth login)
  - Edge cert: show TLS expiry and reload Caddy
  - Edge doctor: combined host checks + compose status + HTTPS health
  - Provider bootstrap tokens: create/list/revoke via Quartermaster admin endpoints; CLI commands under `admin bootstrap-tokens`

- In Progress
  - (none for MVP)

- Next Up (post‑MVP)
  - Workspace mode: `workspace init` (clone repos), `workspace dev up|down`; build helmsman (Go), later Mist WITH_AV
  - Web interfaces dev helpers: `web:dev up|down` for webapp and marketing sites (optional shortcut)

Service Templates & Selection (Design)

- Catalog: YAML describing each service (role, image, ports, health, dependencies); grouped by tier.
- Templates: one compose fragment per service plus shared networks/volumes; merge into a host‑specific compose.
- Profiles: central‑all, control‑core, routing‑only, analytics‑suite, billing‑only; defaults tuned for single‑host first runs.
- Selection UX:
  - Interactive: `frameworks services plan` presents checkbox‑like grouped selection and profile presets; outputs compose + `.env`.
  - Non‑interactive: `--include svc1,svc2` / `--exclude svcX` flags to generate the same artifacts.
- Operate: `frameworks services up|down|status|logs [--only svc1,svc2] [--ssh user@host]` executes docker compose with the generated plan.
- Future: `frameworks services move <svc> --to <host>` (drain → start elsewhere → flip discovery → stop original) once APIs exist.


Command Surface (MVP)

- edge
  - preflight | tune | init | enroll | status | update | logs | cert | doctor
- services
  - plan [--interactive|--profile] | up | down | status [--only <svc>] | logs [--only <svc>] | health [--type <name>|--service-id <id>] | discover --type <name>
- context
  - init | list | use <name> | show [name] | set-url <service> <url> | check [--ssh user@host]
- auth
  - login
- workspace
  - init | dev up | dev down | build helmsman (build mist fast-follow)
- admin (provider)
  - tokens create|list|revoke
  - bootstrap-tokens create|list|revoke [--kind edge_node|service] [--tenant-id <id>] [--cluster-id <id>] [--ttl <dur>]

Notes on Structure

- Keep this file as the single source of truth for scope and tracking.
- When implementing a bundle of features, update the “Implementation Status” and “Command Surface” sections, without altering prior sections.
