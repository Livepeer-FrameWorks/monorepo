# frameworks.infra

Ansible collection containing the FrameWorks infrastructure roles. Each role
installs and configures one service (Kafka, Yugabyte, ClickHouse, Postgres,
Zookeeper, Redis, Caddy, observability, docker-compose stacks, edge).

## Design contract

- Versions and artifact URLs are passed in as role variables by the FrameWorks
  CLI, resolved from `config/infrastructure.yaml` and the CI-generated release
  manifest. Roles never decide their own versions.
- Every role tags its task files so the CLI can run sub-phases: `install`,
  `configure`, `service`, `validate`, `init`.
- Restart-on-change uses the prometheus-community handler split: one `restart`
  handler with `daemon_reload: true` that registers `<svc>_restarted`, plus a
  `reload` handler guarded by `when: <svc>_restarted is not defined`.
- Cluster services (Kafka, Yugabyte, ClickHouse) use `serial: 1` plus a
  pre-task cluster-health gate for rolling restarts.

## Roles

One role per FrameWorks service: `postgres`, `yugabyte`, `kafka`,
`zookeeper`, `clickhouse`, `redis`, `caddy`, `privateer`, `prometheus_stack`,
`chatwoot`, `listmonk`, `compose_stack` (generic docker shape), `go_service`
(generic native shape), `mistserver`, `helmsman`, `edge` (meta-role that
composes mistserver+helmsman+caddy in native mode or delegates to
`compose_stack` in docker mode), plus `hello` (CLI wiring smoke).

Every role exposes the same tag surface: `install`, `configure`, `service`,
`validate`, `init`, `seed`, `migrate`, `restart`, `cleanup`. The CLI dispatches
to the right tag per command (see `website_docs/operators/ansible-provisioning`).
