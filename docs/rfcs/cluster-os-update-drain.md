# RFC: cluster os update drain integration

**Status:** Draft (follow-up to OS-tuning ship)
**Owner:** Infra
**Depends on:** `docs/architecture/os-tuning.md`

## Problem

`frameworks cluster os update --apply` (v1) restarts affected services
and reboots hosts without any traffic-management step. For stateless
roles (most of the application tier) this is fine — a brief restart is
acceptable during a maintenance window. For roles with in-flight state
(edges streaming video, Postgres primary, Kafka brokers, MistServer
nodes with active ingest) restart without drain visibly disrupts users.

The v1 documentation tells operators to drain manually before invoking
`cluster os update`. This RFC sequences first-party drain integration so
the command becomes safer to run by default.

## Out of scope

- Cross-cluster orchestration (a single `cluster os update` invocation
  targets one cluster manifest; multi-cluster rollouts stay operator-driven).
- Live migration (drain ≠ move; we accept temporary unavailability of
  the drained host, just not viewer-visible disruption).

## Proposal

Add per-role `tasks/drain.yml` + `tasks/undrain.yml` hooks. The
`cluster_os_update.yml` playbook calls them before/after the upgrade
block, gated on `ansible.builtin.stat` of the file (missing file = no-op
drain). Roles that don't ship a drain hook are restarted in place with
no traffic management — same as v1 behavior.

Per-host role-set computation: build a `host → []role` map in the Go CLI
side (`cli/pkg/inventory/host_roles.go`, follow-up). Pass to the playbook
as `host_roles` extra-var. The playbook's drain block iterates that list
and conditionally includes each role's drain hook.

## Sequencing

Each drain hook ships only when its underlying primitive is verified to exist.

| Phase   | Role         | Drain mechanism                                                                                                                                                                                 | Verification gate                                                                                       |
| ------- | ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| Phase 1 | `edge`       | Existing edge node lifecycle (`frameworks cluster nodes drain <host>` or equivalent). Re-use the existing path — do NOT cut a separate Foghorn deregister/re-enroll, that risks identity churn. | Verify the actual command name + that it's safe to invoke from inside Ansible.                          |
| Phase 2 | `postgres`   | Refuse upgrade unless `pg_is_in_recovery()` returns true (replica) or operator passes `--allow-primary`. No traffic management — just a safety gate.                                            | Verify the role's `tasks/` already has a `postgres` query primitive we can reuse.                       |
| Phase 3 | `kafka`      | Preferred-leader election off the broker via `kafka-leader-election.sh`. Undrain re-triggers.                                                                                                   | Verify the helper script ships in our `kafka` role; if Cruise Control is in scope, prefer that.         |
| Phase 4 | `mistserver` | MistAPI: set per-stream "do not accept new", wait `drain_timeout` for active outputs to fall to zero, force-close after.                                                                        | Verify the MistAPI surface against the version we ship; the API has changed across MistServer versions. |

The phases are independent — each can ship when its verification passes,
without blocking the others.

## Other roles

The remaining ~15 roles (redis, clickhouse, prometheus_stack, etc.)
stay with v1 behavior: restart in place with no drain. Acceptable for:

- Stateless app tier (chandler, foghorn, peer, etc.)
- ClickHouse (single-node deploys today)
- Redis (single-master)
- Prometheus stack (metrics scrape gap is tolerable)

If any of these grow drain requirements, file a follow-up alongside the
operational reason.

## Failure handling

- Drain hook failure halts the per-host run (`any_errors_fatal` default).
  Operator runs `--skip-host` and investigates.
- Undrain hook failure halts the fleet run regardless of
  `--continue-on-error`. A node left drained is a worse state than a
  halt — the operator needs to be paged.

## Operator-facing surface

```
frameworks cluster os update --apply               # v1: no drain
frameworks cluster os update --apply --no-drain    # explicit opt-out (post-v1)
frameworks cluster os update --apply --drain-timeout=120s
```

`--no-drain` exists from phase 1 onward so the v1 path remains accessible.

## Migration

No backfill needed. Existing nodes pick up drain on the next `cluster os
update --apply` run after the relevant phase ships.
