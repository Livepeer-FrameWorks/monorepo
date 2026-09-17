# Lookout (Incidents)

Lookout (`api_incidents`, binary `lookout`) turns Alertmanager notifications into incidents,
lets platform operators and tenants act on them, notifies operator channels, and hands tenant
incidents to Skipper for investigation. Alertmanager keeps grouping, deduplication, inhibition,
and silences; Lookout owns everything after one grouped notification arrives.

```
vmalert → Alertmanager ─┬─ Watchdog ──────────────→ external heartbeat
                        ├─ severity=critical ─────→ email (direct fallback)
                        └─ every group ───────────→ Lookout POST /v1/alertmanager
                                                        │
                    Postgres `lookout` ◄────────────────┤ incidents, alerts, timeline, outbox
                                                        ├─ outbox → email / Slack / Discord  (platform scope)
                                                        ├─ outbox → Kafka lookout.incidents  (tenant scope) → Skipper
                                                        └─ Decklog incident_updated → Signalman CHANNEL_SYSTEM (tenant scope)
```

## Service

| Item         | Value                                                                               |
| ------------ | ----------------------------------------------------------------------------------- |
| Module       | `frameworks/api_incidents`, entrypoint `cmd/lookout`                                |
| HTTP         | `LOOKOUT_PORT` (18022): `/health`, `/metrics`, `POST /v1/alertmanager`              |
| gRPC         | `LOOKOUT_GRPC_PORT` (19008): `lookout.LookoutService`, gRPC health, reflection      |
| DNS          | `lookout.internal`; clients read `LOOKOUT_GRPC_ADDR`                                |
| Placement    | Stateless replicas in the aggregator region                                         |
| Database     | PostgreSQL database `lookout`, baseline `pkg/database/sql/schema/lookout.sql`       |
| Kafka        | Aggregator cluster: produces `lookout.incidents`, consumes `service_events`         |
| Dependencies | Quartermaster (cluster owner lookup, bootstrap), Decklog (aggregator region), Kafka |
| Registration | `qmbootstrap` registers service type `lookout` with its gRPC port                   |

Startup requires `DATABASE_URL`, `SERVICE_TOKEN`, `QUARTERMASTER_GRPC_ADDR`, `DECKLOG_GRPC_ADDR`,
`KAFKA_BROKERS`, and `LOOKOUT_ALERTMANAGER_TOKEN`. It also reads `JWT_SECRET` (forwarded user
JWTs), `KAFKA_CLUSTER_ID`, `CLUSTER_ID`, `NODE_ID`, `REGION`, `LOOKOUT_HOST` (advertised host),
and the shared gRPC TLS keys (`GRPC_TLS_CERT_PATH`, `GRPC_TLS_KEY_PATH`, `GRPC_TLS_CA_PATH`,
`GRPC_ALLOW_INSECURE`).

These values are read on every use, so an env-file reload (`server.RegisterEnvFileReload`)
takes effect without a restart:

| Key                                                                               | Use                                                                                    |
| --------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| `LOOKOUT_ALERTMANAGER_TOKEN`                                                      | Bearer token Alertmanager must present; empty rejects every call                       |
| `LOOKOUT_NOTIFY_EMAIL_TO`                                                         | Comma-separated operator recipients; enables the email channel                         |
| `LOOKOUT_SLACK_WEBHOOK_URL`                                                       | Slack incoming webhook; enables the Slack channel                                      |
| `LOOKOUT_DISCORD_WEBHOOK_URL`                                                     | Discord webhook; enables the Discord channel                                           |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `FROM_EMAIL`, `FROM_NAME` | Shared SMTP settings used through `pkg/email`                                          |
| `WEBAPP_PUBLIC_URL`                                                               | Notifications link to `<WEBAPP_PUBLIC_URL>/admin/incidents/<id>`; unset omits the link |

## Alertmanager routing

The `prometheus_stack` role renders `alertmanager.yml` with root receiver `lookout` and
`group_by: [alertname, region, cluster]`. Child routes, in order:

1. `alertname="Watchdog"` → `heartbeat` webhook (`ALERTMANAGER_HEARTBEAT_URL`, `send_resolved: false`,
   1-minute group interval and repeat). The `Watchdog` rule in `pkg/grafana/rules/frameworks.yml`
   is `vector(1)` and always fires, so the external heartbeat alarms when pings stop. It never
   reaches Lookout or email.
2. `severity="critical"` → `email-fallback` with `continue: true`, so critical alerts still page
   when Lookout is down.
3. A matcher-less route → `lookout` webhook (`ALERTMANAGER_LOOKOUT_URL`, rendered by the CLI as
   `http://lookout.internal:18022/v1/alertmanager`), bearer `LOOKOUT_ALERTMANAGER_TOKEN`,
   `send_resolved: true`. Alertmanager uses the parent receiver only when no child matches, which
   is why this trailing route exists.

Grouping by cluster keeps every notification inside one cluster, so one group never mixes tenants.

## Ingestion

`POST /v1/alertmanager` compares the bearer token in constant time, reads at most 8 MiB, and
validates the payload: `groupKey`, at least one alert, and per alert a `fingerprint`, a status of
`firing` or `resolved`, and `startsAt`. Malformed or invalid payloads return 400 and storage
failures return 500; Alertmanager retries 5xx responses and does not retry 4xx. A successful call
returns `{"outcome": ..., "incident_id": ...}` with outcome `created`, `updated`, `resolved`,
`unchanged`, or `ignored`.

**Group facts.** `alertname`, `cluster`, and `region` come from `groupLabels`, falling back to
`commonLabels`. The title is `commonAnnotations.summary` (else the alertname); the summary is
`commonAnnotations.description` (else the title). Alert timestamps are truncated to microseconds
so repeats compare equal to stored values.

**Scope.** Quartermaster decides who owns a cluster: one with an `owner_tenant_id` that is not
`is_platform_official` gives tenant scope for that tenant; anything else, including `NotFound`, is
platform scope. An alert without a `cluster` label is platform scope. Lookout keeps the answer per
cluster in `lookout.cluster_scopes`, shared by every replica, and that row is the only scope
ingestion uses. There is no in-process cache.

- **Ingestion.** When the cluster has no verified row, Lookout asks Quartermaster `GetCluster`
  before the ingest transaction and applies the answer (below). If Quartermaster cannot answer, an
  unverified platform row is created, so an outage alert still opens an incident, and a tenant is
  never shown an incident on an owner nobody confirmed. The ingest transaction then reads the row
  with `FOR SHARE` and holds that lock until it commits. A verified row that differs from an
  existing open incident moves it in the same transaction. A cluster with a verified row needs no
  Quartermaster call, so incidents keep their owner through a Quartermaster outage.
- **Applying an owner** takes two transactions.
  1. The first stores the answer. Writing the row waits for every ingestion holding its share lock.
     A later ingestion reads the new owner, or on YugabyteDB fails with a serialization conflict
     and retries.
  2. The second locks the row `FOR UPDATE` and the cluster's open incidents (`firing` or
     `acknowledged`), moves each one whose scope or tenant differs from the stored scope, and sets
     `applied_revision` to the row's `revision`. It starts after the waiting ingestions committed,
     so it sees their incidents. YugabyteDB runs with read committed disabled, where a single
     transaction would keep a snapshot from before those commits and miss them.

  So across replicas an incident is either moved or written with the new owner. If the process
  stops between the two transactions, `applied_revision < revision` marks the move as pending. The
  next event, startup pass or notification for the cluster finishes it. The row keeps
  Quartermaster's cluster `updated_at`, and an answer older than the stored one is not written, so
  reconciliations that finish out of order cannot restore a previous owner. A `NotFound` answer has
  no `updated_at` and only replaces an unverified row, because Quartermaster does not delete
  clusters.

- **Moving an incident** (`RescopeIncident`, filtered on the stored tenant):
  - The tenant it moved to gets the `kafka` publication so Skipper can investigate; the incident's
    opening-event row is re-armed for that tenant.
  - An unsettled `kafka` row of the tenant it moved away from is deleted.
  - Both tenants receive a realtime `scope_changed` change.
  - Operator notifications already queued or sent stay.
  - Resolved incidents never move: their history stays with the tenant that owned the cluster when
    they happened.

**Ownership changes.** A verified row only changes when Quartermaster reports a change:

- Quartermaster writes `cluster_created` and `cluster_updated` into its transactional outbox in the
  same transaction as the change. Both write paths do so: the `CreateCluster` and `UpdateCluster`
  RPCs (owner, platform-official flag), marketplace updates and control-cell reassignment, and the
  bootstrap reconcile (`bootstrap.ReconcileClusters`), which can change `is_platform_official` and
  cluster class. A bootstrap replay that changes nothing emits nothing. Events are delivered
  through the aggregator region's Decklog to the aggregator's local `service_events` topic.
- Every Lookout replica joins the consumer group `lookout-cluster-ownership` on that topic (earliest
  offset for a new group) and reuses `KAFKA_BROKERS` and `KAFKA_CLUSTER_ID`; `/health` includes a
  `kafka` check that pings its brokers. For `cluster_created` and `cluster_updated`, Lookout asks
  Quartermaster for the current owner and applies it. Creation matters because an alert can arrive
  before its cluster is registered and open a platform incident. The event only names the cluster
  (`data.cluster_id`, else `resource_id`). Other event types are skipped. A redelivered event
  changes nothing.
- When Quartermaster cannot answer, the handler retries the same record with backoff (1s doubling
  to 30s) until it can. `pkg/kafka` does not redeliver a failed record to a running consumer, so the
  retry happens in the handler, and no offset past the record is committed. A malformed event, or a
  cluster event without a cluster ID, is skipped and counted as `invalid`.
- At startup every replica reconciles once in the background, while HTTP and gRPC already serve. It
  applies the current owner to every cluster that has open incidents. A cluster Quartermaster
  cannot answer for does not stop the others; the pass repeats with the same backoff until every
  cluster is done. This covers changes made while no Lookout consumed events and events that aged
  out of Kafka retention. There is no periodic reconcile.

`lookout.cluster_scopes` queries, `ListOpenIncidentClusterIDs` and `LockOpenClusterIncidents` read
across tenants: the scope row is what decides an alert's tenant, and reconciliation runs without a
caller. The moves themselves use the stored tenant as their predicate.

**State machine** (one transaction per notification, keyed by Alertmanager `groupKey`):

- No open incident for the group: if any firing alert is new, a new incident opens with every
  firing alert in the notification. A firing alert is not new when an already resolved incident in
  the same group (and tenant) contains the same fingerprint with the same `startsAt`. This keeps a
  manually resolved incident closed while Alertmanager repeats its alerts, while a new fingerprint
  or a resolved→firing transition (new `startsAt`) opens a new incident. Notifications with only
  resolved or repeated alerts are `ignored`.
- Open incident (`firing` or `acknowledged`): each alert is upserted by fingerprint. A firing alert
  that is new to the incident, was resolved, or has a new `startsAt` adds `alert_firing`; a firing
  alert that resolves adds `alert_resolved`. Resolved alerts the incident never contained are
  skipped. Severity rises to the highest firing severity (`critical` > `warning` > `info`) and
  `last_alert_at` is updated.
- When no alert in the incident is firing, the incident becomes `resolved` with resolution `auto`.
- Acknowledgement does not stop ingestion; an acknowledged incident still collects alerts and
  auto-resolves.

The partial unique index on `group_key` for unresolved incidents serializes concurrent first
notifications: the losing insert returns no row and the transaction retries (up to three
attempts), locking and updating the winner's incident instead. Silenced or inhibited alerts are
not sent by Alertmanager, so an incident whose alerts are all silenced stays open until someone
resolves it.

## Data model

| Table                         | Contents                                                                                                                                                                                                              |
| ----------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lookout.cluster_scopes`      | One row per alerting cluster: scope, tenant, `verified`, Quartermaster `source_updated_at` (NULL for `NotFound`), `revision`/`applied_revision` (a pending incident move); an unverified row is always platform scope |
| `lookout.incidents`           | Scope (`platform`/`tenant`, tenant required exactly for tenant scope), cluster, region, group key, alertname, severity, status, resolution, title, summary, alert/ack/assign/resolve timestamps and actors            |
| `lookout.incident_alerts`     | Latest state per `(incident_id, fingerprint)`: status, labels/annotations JSONB, `starts_at`, `ends_at` (NULL while firing), generator URL                                                                            |
| `lookout.incident_events`     | Timeline: `alert_firing`, `alert_resolved`, `acknowledged`, `assigned`, `note`, `resolved`, `investigation_attached`, `notified`; actor, JSONB body                                                                   |
| `lookout.notification_outbox` | One row per `(event_id, channel)`: payload snapshot, attempts, `next_attempt_at`, `claimed_at`, `lease_token`, `last_error`, `delivered_at`, `failed_at` (at most one of the two settles a row)                       |

Check constraints tie `resolution` and `resolved_at` to `status = 'resolved'`.
`uq_incidents_open_group_key` allows one unresolved incident per group key.
`uq_incident_events_investigation_report` allows one `investigation_attached` event per
`(incident, report_id)`. Timeline `created_at` defaults to `clock_timestamp()` so events written in
one transaction keep insertion order. Tenant reads and writes filter `tenant_id`; the
open-incident lookup during ingestion is keyed by group key alone because the unique index is the
authority, and cluster scopes and ownership reconciliation read across tenants (see Scope).

## gRPC API

`pkg/proto/lookout.proto`, client `pkg/clients/lookout`:

| RPC                   | Behaviour                                                                                                                             |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `ListIncidents`       | Scope/status/cluster filters, newest first, forward cursor pagination (default 50, max 200); `last`/`before` return `InvalidArgument` |
| `GetIncident`         | Incident, alerts, and timeline with typed kind-specific fields                                                                        |
| `AcknowledgeIncident` | `firing` → `acknowledged`; repeat is a no-op; resolved → `FailedPrecondition`                                                         |
| `AssignIncident`      | Sets or clears the assignee (UUID); resolved → `FailedPrecondition`; same assignee is a no-op                                         |
| `ResolveIncident`     | Manual resolution; repeat is a no-op                                                                                                  |
| `AddIncidentNote`     | Non-empty note up to 10000 bytes; allowed on resolved incidents                                                                       |
| `AttachInvestigation` | Service-token callers only; links a Skipper report to a tenant incident; repeat is a no-op                                            |

Authorization (`grpcserver.accessFromContext`, `incidents.Service`):

- Platform operators (`ctxkeys.IsPlatformOperator`) read and act on every scope, and may filter by
  scope or tenant.
- A caller with a tenant context (user JWT, or service token with `x-tenant-id`) sees only that
  tenant's tenant-scope incidents. Another tenant's incident and any platform incident return
  `NotFound`; requesting platform scope in `ListIncidents` returns `PermissionDenied`.
- A service-token call without tenant context is an internal caller and is unrestricted.
- `AttachInvestigation` is listed in the interceptor's `ServiceOnlyMethods` and the handler also
  requires `IsServiceCall`; the request's `tenant_id` must own the incident and must match any
  tenant metadata on the call.

Every mutation loads the incident under the caller's access, locks it, and writes the state change
and its timeline event in one transaction. Actor IDs are recorded when they are UUIDs.

## Delivery outbox

Transitions enqueue outbox rows in the same transaction:

| Incident scope | Event                             | Channels                                                                                   |
| -------------- | --------------------------------- | ------------------------------------------------------------------------------------------ |
| Platform       | opened, resolved (auto or manual) | `critical`: email, Slack, Discord; other severities: Slack, Discord (configured ones only) |
| Tenant         | opened, or rescoped to a tenant   | `kafka` (`lookout.incidents`), keyed by the incident's opening event                       |

Tenant incidents never notify operator channels. A `pkg/outbox` worker drains rows (batch 10,
poll 5s, lease 5 minutes, backoff 5s doubling to 10 minutes). Each claim carries a fresh
`lease_token`; completion and failure statements require that token, so a worker whose lease
expired cannot settle a row another replica re-claimed. Completing an operator-channel row also
inserts a `notified` timeline event (`body.channel`) in the same transaction.

A row is settled once: `delivered_at` on success, or `failed_at` when the failed attempt that
reaches 20 attempts is recorded (`maxDeliveryAttempts`; with the backoff above the last attempt runs
about two hours after the first). A failed row keeps its `last_error`, is never claimed again,
increments `lookout_deliveries_total{result="failed"}`, and is logged at error level. This bounds a
destination that rejects the delivery permanently, such as a webhook answering 4xx.

Every Lookout replica runs a retention loop hourly (first sweep at startup): delivered rows older
than 7 days and failed rows older than 30 days are deleted in batches of 500, at most 20 batches per
state per sweep. Deleted rows are counted in `lookout_outbox_rows_deleted_total{state}`. Pending
rows are never deleted by retention.

- **Slack:** incoming webhook with Block Kit header, summary (mrkdwn-escaped), fields, and an
  "Open incident" button when `WEBAPP_PUBLIC_URL` is set.
- **Discord:** webhook with one embed (title, description, fields, colour, timestamp, link) and
  `allowed_mentions.parse: []`, so alert text cannot ping users or roles.
- **Email:** HTML-escaped message sent separately to each `LOOKOUT_NOTIFY_EMAIL_TO` recipient; a
  failure for any recipient retries the row.
- **Kafka:** record keyed by incident ID with JSON `{incident_id, tenant_id, cluster_id, severity,
summary}` and headers `tenant_id`, `event_type=incident_opened`, `source=lookout`. The topic
  exists only on the aggregator cluster and is not mirrored.

Webhook transport errors are unwrapped from `*url.Error`, so webhook URLs never reach logs or
`last_error`. A row whose channel secret was removed after it was queued is settled as skipped.

## Realtime

Every committed change is sent after commit, best effort, as Decklog `ServiceEvent`s
(`resource_type=incident`) with the `IncidentEvent` payload (IPC field 30: incident, tenant,
cluster, status, severity, title, change, `updated_at_ms`) to two audiences:

- Tenants: `event_type=incident_updated` with the envelope tenant set to each tenant that can see
  the change (for a rescope, the tenant the incident left and the tenant it moved to). Signalman
  maps it to `EVENT_TYPE_INCIDENT_UPDATED` on `CHANNEL_SYSTEM` and delivers it with
  `BroadcastToTenant`. A platform-scope incident has no tenant event. Signalman drops a tenantless
  `incident_updated` instead of broadcasting it, because tenantless system events reach every
  subscriber. Periscope Ingest records the event through the generic service-event audit path.
- Platform operators: one `event_type=platform_incident_updated` per incident and change, for
  every scope, with no envelope tenant; the payload tenant is the owning tenant, or empty for a
  platform-scope incident. `pkg/serviceevents.PlatformScoped` lists the type, so Decklog accepts it
  without a tenant and Periscope Ingest skips its audit row. Signalman maps it to
  `EVENT_TYPE_INCIDENT_UPDATED` on `CHANNEL_PLATFORM` and delivers it with `BroadcastPlatform`.

`CHANNEL_PLATFORM` is the operator audience. Signalman accepts a subscription to it only on a
service-token stream without a tenant, replies to any other request with a `permission_denied`
error and leaves the channel out of the confirmation, and never delivers its events through
`CHANNEL_ALL` or to tenant streams, including streams of the system tenant.

## Skipper

Skipper starts `LookoutTrigger` when both `KAFKA_BROKERS` and `LOOKOUT_GRPC_ADDR` are configured
(consumer group `skipper-lookout`). Messages without `tenant_id` or `incident_id` are dropped, so
only tenant incidents are investigated. After a successful investigation Skipper calls
`AttachInvestigation(incident_id, tenant_id, report_id)`, retrying after 1s, 2s, and 4s unless the
error is `InvalidArgument`, `NotFound`, `PermissionDenied`, `Unauthenticated`, or
`FailedPrecondition`; it then logs and returns without error, so Kafka redelivery never reruns the
investigation.

## Metrics

| Metric                                                                 | Labels                        | Values                                                                                                                                                 |
| ---------------------------------------------------------------------- | ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `lookout_alertmanager_webhooks_total`                                  | `result`                      | `unauthorized`, `invalid`, `error`, or the ingest outcome                                                                                              |
| `lookout_incident_transitions_total`                                   | `scope`, `transition`         | `opened`, `scope_changed`, `alert_firing`, `alert_resolved`, `auto_resolved`, `acknowledged`, `assigned`, `note`, `resolved`, `investigation_attached` |
| `lookout_scope_lookups_total`                                          | `result`                      | Quartermaster owner lookups: `tenant`, `platform`, `not_found`, `error`                                                                                |
| `lookout_ownership_reconciles_total`                                   | `source`, `result`            | source `event` or `startup`; result `rescoped`, `unchanged`, `unverified`, `error`, `invalid` (events only)                                            |
| `lookout_kafka_consumer_lag`                                           | `topic`, `partition`          | Uncommitted records of the `lookout-cluster-ownership` group                                                                                           |
| `lookout_deliveries_total`                                             | `channel`, `result`           | per attempt `delivered`, `error`, `skipped`; `failed` once when a row reaches the attempt limit                                                        |
| `lookout_outbox_rows_deleted_total`                                    | `state`                       | `delivered`, `failed` (rows removed by retention)                                                                                                      |
| `lookout_realtime_events_total`                                        | `result`                      | `sent`, `error`                                                                                                                                        |
| `lookout_grpc_requests_total`, `lookout_grpc_request_duration_seconds` | `method`, `status` / `method` | Per-RPC counts and latency, including auth rejections                                                                                                  |

## Deployment

The `lookout` database is created with its owner and runtime roles, and its baseline applied, by
`frameworks cluster provision` on a new cluster and by the service-database step of
`cluster migrate --phase expand` / `cluster release apply` on an existing one (see
`docs/standards/schema-migrations.md`, "New service databases"). The database is new in v0.3.8 and
has no migrations. The pre-deploy gate refuses `cluster upgrade lookout` while the database is
missing or empty. The release catalog maps service `lookout` to database `lookout`, and
`pkg/database/capabilities.go` probes the incident columns and the outbox leasing and settlement
columns. Operator setup and verification: `website_docs/src/content/docs/operators/alerting.mdx`.

## Verification

- `make test-lookout`: unit tests (payload validation, Quartermaster owner mapping and failures,
  HTTP auth and status codes, gRPC authorization mapping, Slack/Discord payloads, fake SMTP
  delivery, outbox retries and fencing, retention batch bounds, ownership event filtering and
  retry, realtime tenant and operator audiences).
- `make verify-lookout-db`: real PostgreSQL (query catalog prepare, ingestion state machine,
  tenant filters, actions, rescope on verified ownership and ownership removal, a stored owner
  surviving an outage, an older Quartermaster answer rejected, an ingest and a reconciliation of
  the same cluster racing on two service instances, an interrupted owner change finished by the
  next notification, gRPC authorization, outbox token fencing,
  terminal failure after the attempt limit, retention, `cluster_updated` moving open incidents only
  and idempotently, `cluster_created` moving an incident opened before registration, startup
  reconcile after an unavailable lookup).
- `make verify-yugabyte-service SERVICE=lookout` (`verify-yugabyte-lookout-contracts`): the same
  query catalog, state machine, actions, rescope, fencing, terminal-failure, retention, and
  ownership contracts on YugabyteDB. `make verify-yugabyte-database DATABASE=lookout` adds the baseline,
  runtime-capability, and tagged-upgrade proof.

## Client surfaces

The Gateway calls the gRPC API above with the caller's tenant and user; access rules are the ones in
gRPC API.

- GraphQL: `incidentsConnection`, `incident`, `acknowledgeIncident`, `assignIncident`,
  `resolveIncident`, `addIncidentNote`, and the realtime `liveIncidentUpdates` subscription and
  `TenantEvent.incidentUpdated` firehose field.
- Realtime audience: `liveIncidentUpdates` checks the platform operator grant when the subscription
  starts, with the same authz check that gates `platform.incidents`, and records a
  `platform_admin_read` audit entry. A verified operator is served from the Gateway's tenantless
  `CHANNEL_PLATFORM` upstream stream, which every operator on that Gateway replica shares, and
  receives every incident change. The operator still counts toward their tenant's subscription
  cap. Any other caller, including a member of the system tenant without the grant, is served from
  their tenant's `CHANNEL_SYSTEM` stream and receives only events whose envelope and payload name
  that tenant. The firehose never subscribes to `CHANNEL_PLATFORM`.
- MCP tools: `list_incidents`, `get_incident`, `acknowledge_incident`, `assign_incident`,
  `resolve_incident`, `add_incident_note`.
- Webapp: tenant incidents at `/infrastructure/incidents` and operator incidents at
  `/admin/incidents`, each with a detail page. The pages load through GraphQL queries.
  - Tenant pages, operator pages, and the firing-incident badges in the notification bell,
    notification panel, and sidebar follow `liveIncidentUpdates`. They share one subscription on
    the app's GraphQL WebSocket (`watchIncidentUpdates` in `src/lib/stores/incidents.svelte.ts`).
    Events arrive in 300 ms batches. For an operator the subscription carries every incident, so
    the tenant pages and badges an operator has open also refetch for other tenants' incidents
    that match their filters; the refetched data stays tenant-scoped.
  - The list updates a loaded row in place for `acknowledged`, `resolved`, `assigned`, `note`, and
    `investigation_attached` when the row still matches the filters and the event is not older
    than the row. Every other change refetches the loaded rows with the current filters. The
    other changes are alert changes, which also move the firing count and last alert time, and
    `opened` or `scope_changed`, which add or remove rows. An event for an incident that is not loaded
    refetches when it matches the filters, or when a status filter is set and more pages exist,
    since the incident may have left an unloaded page.
  - The detail page reloads on any change to its incident, because every change adds a timeline
    entry the event does not carry. If a reload no longer finds the incident, the page says it is
    no longer available instead of showing the old data.
  - The badge count refetches after every batch. After the WebSocket reconnects, each consumer
    refetches once, because events sent while it was down are lost.
  - The operator list (`platform.incidents`) and detail page apply the same rules. Events carry no
    scope or tenant, so with a scope or tenant filter set an event for an unloaded incident that
    matches the status and cluster filters refetches. Tenant and cluster filters apply on submit,
    and refetches use the applied values. On the operator detail page a `scope_changed` reload
    still finds the incident, so the page keeps showing it.
