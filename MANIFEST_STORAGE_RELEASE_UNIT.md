# Storage-Artifact-Catalog + Freeze/Thumbnail/Processing-Authority — Release-Unit Manifest

> Generated from a read-only working-tree snapshot (`git status --porcelain=v1`, `git diff --numstat`,
> `git ls-files --others --exclude-standard`). Every changed and untracked path is enumerated exactly
> once across the three sections. NO wildcards, NO elisions.

## Totals

| Section             | Meaning                                                    | Paths   |
| ------------------- | ---------------------------------------------------------- | ------- |
| 1 — IN RELEASE UNIT | storage + authority + required build/verify consumers      | **351** |
| 2 — SPLIT-BY-HUNK   | straddling files, partially included                       | **18**  |
| 3 — EXCLUDE         | separate branch (analytics/mesh/infra/marketing/doc-sweep) | **97**  |
| **Total**           |                                                            | **466** |

Working-tree snapshot: 335 tracked (modified/deleted) + 131 untracked = 466 paths (this manifest included). The
original snapshot was 449 + 1 self-count = 450; the canary-remediation work below added 16 storage/authority
files, so the live count is now **466** (`git status --porcelain=v1` is the authoritative live list).

**Correction applied 2026-07-27 (session 3): +1 to Section 1** (350 → 351). Enumerated bullet (authority/provenance
test coverage: F6 owner-vanish unresolved-tenant finalization):

- `api_balancing/internal/triggers/stream_end_source_inactive_test.go`

**Correction applied 2026-07-27 (session 5): +0 to totals.** The live-stream thumbnail cleanup RPC
(`DeleteStreamThumbnails`), the backend-aware purge routing, the resolver parent-lifecycle gate, and the identity
hardening all landed as EDITS to files already enumerated (`pkg/proto/shared.proto`, `pkg/proto/foghorn.proto`
and their generated `*.pb.go`, `api_balancing/internal/grpc/server.go`, `pkg/clients/foghorn/grpc_client.go`,
`api_control/internal/grpc/server.go`, the thumbnail/purge/cleanup Go files, the migration + baseline, and the
docs) — no new paths, so the total stays 466. `git status --porcelain=v1` remains the authoritative live list.

**Correction applied 2026-07-27 (session 2 — canary remediation): +15 to Section 1.** The crash-safe thumbnail
publication state machine and its supporting authority/resolver code were added AFTER the original snapshot and
were not enumerated. All 15 are storage + authority + build/verify consumers → **Section 1** (335 → 350):

- `api_assets/internal/handlers/assets.go` — Chandler active-version resolve + versioned serve
- `api_assets/internal/handlers/assets_test.go`
- `api_balancing/internal/control/thumbnail_publication.go` — publication state machine (claim/verify/publish CAS, recovery, cleanup)
- `api_balancing/internal/control/thumbnail_publication_realpg_test.go`
- `api_balancing/internal/control/thumbnail_completion_realpg_test.go`
- `api_balancing/internal/control/cluster_access.go` — `ClusterAccessibleForTenant` entitlement predicate
- `api_balancing/internal/control/cluster_access_test.go`
- `api_balancing/internal/control/chandler_base_test.go`
- `api_balancing/internal/grpc/server_test.go` — DVR/thumbnail cluster-resolve coverage
- `api_balancing/internal/handlers/thumbnail_resolve.go` — in-cell active-version resolver endpoint (:18008)
- `api_balancing/internal/jobs/thumbnail_recovery.go` — stuck-attempt recovery reconciler
- `api_balancing/internal/storage/cluster_resolver.go` — tenant storage-cluster routing
- `api_balancing/internal/triggers/tenant_provenance_test.go` — lifecycle fail-closed-on-unresolvable coverage
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/027_thumbnail_publication.sql` — publication tables + active pointer
- `pkg/topology/dependencies.go` — Foghorn↔Chandler in-cell resolver dependency

**Corrections applied 2026-07-27 (session 1 — all total-neutral except the +1 manifest self-count):**

1. **Top Assets is ANALYTICS, not storage** (the storage `library/[hash]/analytics` page does not render it).
   Every TopAssets path therefore leaves the unit or is split out: `GetTopAssets.gql`, `top_assets_test.go`,
   `analytics_conn_b_resolver_test.go` → EXCLUDE; the mixed storage+TopAssets handler files
   (`analytics_connections.go`, `periscope/grpc_client.go`+`interface.go`, `api_analytics_query/.../server.go`)
   → SPLIT-BY-HUNK (keep the artifact/node-copy/synced hunks, move the ListTopAssets hunk). This resolves
   straddle-risk #1.
2. **`charts/GeoView.svelte` PROMOTED into the unit** — it is imported by the included storage page
   `website_application/src/routes/library/[hash]/analytics/+page.svelte`. Resolves the "shared chart" risk.
3. This manifest file is enumerated in Section 1 (release-meta docs).

## Generated-artifact regeneration (do this AFTER splitting the source, never hand-split generated files)

- **Protos** — after splitting `pkg/proto/periscope.proto` (and any hunk of `shared.proto`/`commodore.proto`),
  run `make proto`. This regenerates every `pkg/proto/*/*.pb.go` + `*_grpc.pb.go` (commodore, foghorn,
  foghorn_federation, ipc, periscope, shared). `periscope/*.pb.go` straddles → it is only correct after the
  proto itself is split; do not cherry-pick lines out of the generated Go.
- **GraphQL** — after splitting `pkg/graphql/schema.graphql` and moving the storage `.gql` operations,
  run `make graphql-all` (NOT bare `make graphql`). This regenerates `api_gateway/graph/generated/generated.go`,
  `api_gateway/graph/model/models_gen.go`, `api_gateway/graph/schema.resolvers.go` stubs, and the
  `website_application/$houdini/*` store (gitignored). Then regenerate `app_mac/.../GeneratedQueries.swift`
  from the surviving `.gql` set.
- **Feature registry** (EXCLUDED side) — `scripts/registry/main.go` + `docs/platform-features.yaml` regenerate
  `website_application/src/lib/features/registry.json`, `feature-matrix.mdx`, `platform-capabilities.mdx` via
  `make generate-feature-registry`. Belongs to the OTHER branch; do not run it inside the storage cut.
- **Docs bundle** — `website_docs/public/llms-full.txt` is generated by `scripts/copy-agent-files.sh`; excluded,
  regenerate on the branch that owns the doc changes.

## Section 1 — IN RELEASE UNIT — storage + authority + required consumers (351 paths: 335 enumerated below + 16 in the 2026-07-27 session-2/3 addenda above)

### api_balancing — Foghorn: storage authority, freeze/thumbnail, creation saga, jobs, federation, state, s3 (134)

- `api_balancing/cmd/foghorn/main.go` — +124/-12
- `api_balancing/go.mod` — +1/-1
- `api_balancing/internal/artifactoutbox/outbox.go` — +139/-43
- `api_balancing/internal/artifactoutbox/outbox_test.go` — +55/-23
- `api_balancing/internal/artifacts/cleanup.go` — +82/-30
- `api_balancing/internal/artifacts/cleanup_test.go` — +76/-18
- `api_balancing/internal/control/artifact_playback_test.go` — +47/-1
- `api_balancing/internal/control/commodore_projection.go` — +48/-56
- `api_balancing/internal/control/commodore_projection_test.go` — new (+71)
- `api_balancing/internal/control/dvr_chapter_finalize_hook.go` — +237/-86
- `api_balancing/internal/control/dvr_chapters_repo.go` — +430/-47
- `api_balancing/internal/control/dvr_chapters_repo_test.go` — +4/-4
- `api_balancing/internal/control/dvr_chapters_state_test.go` — +253/-16
- `api_balancing/internal/control/dvr_finalize.go` — +521/-19
- `api_balancing/internal/control/dvr_finalize_test.go` — +344/-0
- `api_balancing/internal/control/dvr_segments_handlers.go` — +112/-6
- `api_balancing/internal/control/dvr_segments_handlers_coverage_test.go` — +238/-13
- `api_balancing/internal/control/dvr_segments_queries_test.go` — +19/-19
- `api_balancing/internal/control/dvr_segments_repo.go` — +64/-13
- `api_balancing/internal/control/dvr_segments_repo_test.go` — +17/-17
- `api_balancing/internal/control/finalize_hook_coverage_test.go` — +233/-68
- `api_balancing/internal/control/freeze_authz.go` — new (+113)
- `api_balancing/internal/control/freeze_dtsh_attempt_test.go` — new (+239)
- `api_balancing/internal/control/freeze_permission_handler_test.go` — new (+324)
- `api_balancing/internal/control/freeze_realpg_test.go` — new (+576)
- `api_balancing/internal/control/managed_streams_test.go` — +2/-2
- `api_balancing/internal/control/metrics.go` — +2/-1
- `api_balancing/internal/control/placement_test.go` — new (+344)
- `api_balancing/internal/control/playback.go` — +9/-56
- `api_balancing/internal/control/playback_test.go` — +0/-40
- `api_balancing/internal/control/relay_resolve.go` — +114/-37
- `api_balancing/internal/control/relay_resolve_branching_test.go` — +64/-11
- `api_balancing/internal/control/relay_resolve_test.go` — +114/-3
- `api_balancing/internal/control/relay_test.go` — +47/-42
- `api_balancing/internal/control/repos.go` — +883/-110
- `api_balancing/internal/control/repos_artifact_test.go` — +115/-19
- `api_balancing/internal/control/repos_more_test.go` — +142/-12
- `api_balancing/internal/control/resolve_content_happy_coverage_test.go` — +1/-1
- `api_balancing/internal/control/resolver_deep_coverage_test.go` — +3/-3
- `api_balancing/internal/control/resolver_test.go` — +1/-1
- `api_balancing/internal/control/server.go` — +2419/-1214
- `api_balancing/internal/control/server_control_stream_test.go` — +143/-0
- `api_balancing/internal/control/server_freeze_complete_test.go` — del (-185)
- `api_balancing/internal/control/server_mist_trigger_test.go` — +54/-9
- `api_balancing/internal/control/server_processing_result_test.go` — +292/-184
- `api_balancing/internal/control/server_sync_complete_test.go` — +750/-141
- `api_balancing/internal/control/server_thumbnail_readiness_test.go` — +93/-17
- `api_balancing/internal/control/server_thumbnail_test.go` — +51/-3
- `api_balancing/internal/control/staging_cleanup.go` — new (+106)
- `api_balancing/internal/control/storage_usage.go` — +126/-5
- `api_balancing/internal/control/storage_usage_test.go` — new (+97)
- `api_balancing/internal/control/testhelpers.go` — +3/-1
- `api_balancing/internal/control/testhelpers_artifact_test.go` — +90/-3
- `api_balancing/internal/federation/peer_manager_test.go` — +1/-1
- `api_balancing/internal/federation/server.go` — +193/-259
- `api_balancing/internal/federation/server_artifact_audit_test.go` — +14/-12
- `api_balancing/internal/federation/server_auth_test.go` — +9/-9
- `api_balancing/internal/federation/server_delete_storage_test.go` — +62/-12
- `api_balancing/internal/federation/server_forward_test.go` — +29/-21
- `api_balancing/internal/federation/server_mint_storage_test.go` — +40/-280
- `api_balancing/internal/federation/server_mutation_gate_test.go` — new (+88)
- `api_balancing/internal/federation/server_notify_origin_pull_test.go` — +4/-3
- `api_balancing/internal/federation/server_peerchannel_handlers_test.go` — +8/-6
- `api_balancing/internal/federation/server_prepare_test.go` — +124/-64
- `api_balancing/internal/federation/server_test.go` — +8/-6
- `api_balancing/internal/grpc/artifact_creation_commands.go` — new (+306)
- `api_balancing/internal/grpc/artifact_creation_commands_test.go` — new (+316)
- `api_balancing/internal/grpc/artifact_creation_status.go` — new (+231)
- `api_balancing/internal/grpc/artifact_creation_status_test.go` — new (+407)
- `api_balancing/internal/grpc/clip_dvr_vod_rpc_coverage_test.go` — +0/-154
- `api_balancing/internal/grpc/dvr_clip_lifecycle_coverage_test.go` — +13/-6
- `api_balancing/internal/grpc/dvr_start_reconcile_test.go` — new (+111)
- `api_balancing/internal/grpc/media_retention.go` — +47/-8
- `api_balancing/internal/grpc/media_retention_override_test.go` — +39/-3
- `api_balancing/internal/grpc/pure_helpers_test.go` — +0/-15
- `api_balancing/internal/grpc/rpc_handlers_coverage_test.go` — +71/-121
- `api_balancing/internal/grpc/server.go` — +1664/-905
- `api_balancing/internal/grpc/server_dvr_cluster_resolve_test.go` — new (+74)
- `api_balancing/internal/grpc/server_more_test.go` — +6/-14
- `api_balancing/internal/grpc/viewer_endpoint_happy_coverage_test.go` — +1/-1
- `api_balancing/internal/grpc/vod_complete_ambiguous_test.go` — new (+170)
- `api_balancing/internal/grpc/vod_complete_coverage_test.go` — +233/-9
- `api_balancing/internal/grpc/vod_pipeline.go` — +0/-143
- `api_balancing/internal/grpc/vod_status_test.go` — +60/-3
- `api_balancing/internal/handlers/balancing_commodore_coverage_test.go` — +1/-1
- `api_balancing/internal/handlers/handlers.go` — +4/-186
- `api_balancing/internal/handlers/stream_balancing_arms_coverage_test.go` — +1/-1
- `api_balancing/internal/jobs/aborting_vod_recovery.go` — new (+272)
- `api_balancing/internal/jobs/aborting_vod_recovery_test.go` — new (+183)
- `api_balancing/internal/jobs/artifact_reconciler.go` — +828/-119
- `api_balancing/internal/jobs/artifact_reconciler_test.go` — +562/-173
- `api_balancing/internal/jobs/chapter_finalization_queue.go` — +28/-23
- `api_balancing/internal/jobs/chapter_finalization_queue_coverage_test.go` — +22/-16
- `api_balancing/internal/jobs/completing_vod_recovery.go` — new (+403)
- `api_balancing/internal/jobs/completing_vod_recovery_test.go` — new (+234)
- `api_balancing/internal/jobs/creation_command_expiry.go` — new (+290)
- `api_balancing/internal/jobs/creation_command_expiry_test.go` — new (+314)
- `api_balancing/internal/jobs/dispatchers_coverage_test.go` — +7/-13
- `api_balancing/internal/jobs/dvr_starting_recovery.go` — new (+368)
- `api_balancing/internal/jobs/dvr_starting_recovery_test.go` — new (+221)
- `api_balancing/internal/jobs/job_router.go` — +23/-1
- `api_balancing/internal/jobs/job_router_test.go` — +25/-0
- `api_balancing/internal/jobs/processing_dispatcher.go` — +348/-156
- `api_balancing/internal/jobs/processing_dispatcher_hls_test.go` — +8/-0
- `api_balancing/internal/jobs/processing_dispatcher_recovery_test.go` — +28/-31
- `api_balancing/internal/jobs/processing_dispatcher_test.go` — +2/-0
- `api_balancing/internal/jobs/processing_dispatcher_tx_test.go` — new (+156)
- `api_balancing/internal/jobs/purge_deleted.go` — +135/-40
- `api_balancing/internal/jobs/purge_deleted_test.go` — +132/-32
- `api_balancing/internal/jobs/purge_ownership_realpg_test.go` — new (+81)
- `api_balancing/internal/jobs/reconcilers_coverage_test.go` — +49/-15
- `api_balancing/internal/jobs/retention.go` — +180/-82
- `api_balancing/internal/jobs/retention_test.go` — +49/-17
- `api_balancing/internal/jobs/staging_cleanup.go` — new (+219)
- `api_balancing/internal/jobs/staging_cleanup_test.go` — new (+96)
- `api_balancing/internal/jobs/stale_freeze_cleanup.go` — +97/-12
- `api_balancing/internal/jobs/stale_freeze_cleanup_realpg_test.go` — new (+118)
- `api_balancing/internal/jobs/stale_freeze_cleanup_test.go` — +29/-5
- `api_balancing/internal/jobs/testhelpers_test.go` — +32/-34
- `api_balancing/internal/state/cache.go` — +53/-7
- `api_balancing/internal/state/cache_artifact_test.go` — +194/-5
- `api_balancing/internal/state/redis_store.go` — +190/-38
- `api_balancing/internal/state/redis_store_test.go` — +161/-13
- `api_balancing/internal/state/rehydrate_coverage_test.go` — +22/-6
- `api_balancing/internal/state/stream_state.go` — +636/-79
- `api_balancing/internal/state/stream_state_artifact_test.go` — +189/-15
- `api_balancing/internal/state/stream_state_coverage_test.go` — +124/-8
- `api_balancing/internal/state/triple_write_test.go` — +320/-40
- `api_balancing/internal/storage/s3_client.go` — +71/-0
- `api_balancing/internal/storage/s3_client_coverage_test.go` — +4/-0
- `api_balancing/internal/storage/s3_client_parse_test.go` — +31/-0
- `api_balancing/internal/triggers/node_artifact_snapshot_test.go` — new (+111)
- `api_balancing/internal/triggers/processor.go` — +71/-11
- `api_balancing/internal/triggers/processor_test.go` — +9/-2

### api_control — Commodore: catalog authority, thumbnails, creation intents, playback access (17)

- `api_control/internal/grpc/artifact_creation_intents.go` — new (+947)
- `api_control/internal/grpc/artifact_creation_intents_test.go` — new (+550)
- `api_control/internal/grpc/artifact_thumbnails.go` — +293/-113
- `api_control/internal/grpc/artifact_thumbnails_test.go` — +372/-122
- `api_control/internal/grpc/catalog_tombstone_test.go` — new (+57)
- `api_control/internal/grpc/dvr_chapter_playback_test.go` — +13/-3
- `api_control/internal/grpc/dvr_chapters.go` — +1/-1
- `api_control/internal/grpc/media_list_handlers_test.go` — +139/-96
- `api_control/internal/grpc/media_list_test.go` — +0/-129
- `api_control/internal/grpc/media_retention.go` — +9/-2
- `api_control/internal/grpc/mint_chapter_playback_test.go` — new (+68)
- `api_control/internal/grpc/playback_access_control.go` — +37/-64
- `api_control/internal/grpc/playback_access_control_test.go` — +25/-0
- `api_control/internal/grpc/policy_bundle.go` — +2/-2
- `api_control/internal/grpc/register_artifact_test.go` — +11/-56
- `api_control/internal/grpc/server.go` — +560/-1094
- `api_control/internal/grpc/server_pure_helpers_test.go` — +0/-71

### api_sidecar — Helmsman: storage manager, freeze, processing, relay/DTSH (19)

- `api_sidecar/internal/control/client.go` — +27/-42
- `api_sidecar/internal/control/client_dvr_dispatch_test.go` — +35/-7
- `api_sidecar/internal/control/client_send_test.go` — +5/-85
- `api_sidecar/internal/control/dvr_manager.go` — +18/-9
- `api_sidecar/internal/handlers/cleanup.go` — +9/-4
- `api_sidecar/internal/handlers/cleanup_test.go` — +60/-0
- `api_sidecar/internal/handlers/handlers.go` — +5/-1
- `api_sidecar/internal/handlers/poller.go` — +257/-36
- `api_sidecar/internal/handlers/poller_conversion_test.go` — +185/-10
- `api_sidecar/internal/handlers/processing.go` — +32/-15
- `api_sidecar/internal/handlers/processing_chapter.go` — +8/-1
- `api_sidecar/internal/handlers/processing_clip.go` — +2/-1
- `api_sidecar/internal/handlers/storage_manager.go` — +288/-316
- `api_sidecar/internal/handlers/storage_manager_freeze_test.go` — +347/-70
- `api_sidecar/internal/relay/dtsh.go` — +13/-96
- `api_sidecar/internal/relay/metrics.go` — +0/-10
- `api_sidecar/internal/relay/relay_test.go` — +27/-20
- `api_sidecar/internal/relay/resolver.go` — +0/-2
- `api_sidecar/internal/storage/trigger_wal.go` — +4/-3

### api_analytics_ingest / api_analytics_query / api_firehose — storage projection pipeline (separate Go modules; flagged) (9)

- `api_analytics_ingest/README.md` — +2/-1 — [REQUIRED-FOR-PROJECTION] ingests artifact placement/lifecycle events feeding the storage projection
- `api_analytics_ingest/internal/handlers/artifact_events_dedup_test.go` — new (+97) — [REQUIRED-FOR-PROJECTION] ingests artifact placement/lifecycle events feeding the storage projection
- `api_analytics_ingest/internal/handlers/artifact_placement_test.go` — new (+134) — [REQUIRED-FOR-PROJECTION] ingests artifact placement/lifecycle events feeding the storage projection
- `api_analytics_ingest/internal/handlers/final_fact_parser.go` — +3/-2 — [REQUIRED-FOR-PROJECTION] ingests artifact placement/lifecycle events feeding the storage projection
- `api_analytics_ingest/internal/handlers/handlers.go` — +174/-133 — [REQUIRED-FOR-PROJECTION] ingests artifact placement/lifecycle events feeding the storage projection
- `api_analytics_ingest/internal/handlers/handlers_test.go` — +55/-72 — [REQUIRED-FOR-PROJECTION] ingests artifact placement/lifecycle events feeding the storage projection
- `api_analytics_ingest/internal/handlers/lifecycle_updated_at_test.go` — new (+24) — [REQUIRED-FOR-PROJECTION] ingests artifact placement/lifecycle events feeding the storage projection
- `api_analytics_query/internal/grpc/server_test.go` — +156/-12 — [REQUIRED-FOR-PROJECTION] covers the KEPT GetArtifactNodeCopies / synced-to-S3 storage-projection path (ListTopAssets cases move with the split)
- `api_firehose/internal/grpc/server.go` — +7/-4 — [REQUIRED-FOR-PROJECTION] forwards artifact events into the projection path

(`api_analytics_query/internal/grpc/server.go` → SPLIT-BY-HUNK; `top_assets_test.go` → EXCLUDE — see corrections.)

### api_gateway — GraphQL resolvers / catalogview / MCP / demo storage consumers (28)

- `api_gateway/graph/complexity.go` — +1/-10
- `api_gateway/graph/demo_schema_sweep_test.go` — +0/-1
- `api_gateway/graph/realpath_canned_test.go` — +1/-1
- `api_gateway/graph/schema.resolvers.go` — +18/-26 — [GENERATED-STUBS] regenerate via make graphql-all after schema.graphql split
- `api_gateway/internal/catalogview/tracks.go` — new (+44)
- `api_gateway/internal/catalogview/tracks_test.go` — new (+46)
- `api_gateway/internal/clients/clients.go` — +15/-5
- `api_gateway/internal/clients/clientstest/clientstest.go` — +5/-31
- `api_gateway/internal/clients/clientstest/clientstest_batch3_ext.go` — +0/-9
- `api_gateway/internal/clients/clientstest/clientstest_periscope_analytics.go` — +16/-0 — [STRADDLE] periscope analytics client stub required to compile storage resolvers
- `api_gateway/internal/demo/generators.go` — +3/-44 — [DEMO] storage-catalog demo generators
- `api_gateway/internal/demo/generators_test.go` — +0/-1
- `api_gateway/internal/mcp/resources/parity_test.go` — +0/-91
- `api_gateway/internal/mcp/resources/streams.go` — +1/-1
- `api_gateway/internal/mcp/resources/vod.go` — +80/-87
- `api_gateway/internal/mcp/resources/vod_test.go` — +79/-130
- `api_gateway/internal/mcp/server.go` — +1/-1
- `api_gateway/internal/resolvers/artifact_storage_state_test.go` — +9/-38
- `api_gateway/internal/resolvers/media_connection_options.go` — del (-29)
- `api_gateway/internal/resolvers/parity_exports.go` — +0/-6
- `api_gateway/internal/resolvers/storage_artifacts.go` — +165/-54
- `api_gateway/internal/resolvers/storage_artifacts_handlers_test.go` — +23/-0
- `api_gateway/internal/resolvers/streams.go` — +36/-376
- `api_gateway/internal/resolvers/streams_clips_dvr_resolver_test.go` — +2/-82
- `api_gateway/internal/resolvers/streams_connections_handlers_test.go` — +2/-80
- `api_gateway/internal/resolvers/vod.go` — +114/-189
- `api_gateway/internal/resolvers/vod_connection_handlers_test.go` — del (-65)
- `api_gateway/internal/resolvers/vod_resolver_handlers_test.go` — +29/-14

### api_tenants — proto consumer (1)

- `api_tenants/internal/grpc/server.go` — +10/-3 — [PROTO-CONSUMER] rebuilds against changed shared.proto

### app_mac — generated storage queries (1)

- `app_mac/Sources/Gateway/GeneratedQueries.swift` — +87/-232 — [GENERATED-STRADDLE] regenerated from the .gql set incl. excluded analytics ops — regenerate after the gql split, do not hand-edit

### cli — provisioner storage migrations + real-engine tests (single Go module — the whole touched module builds together) (12)

- `cli/cmd/cluster.go` — +23/-0
- `cli/cmd/cluster_logs.go` — +11/-3
- `cli/cmd/cluster_logs_test.go` — +5/-0
- `cli/pkg/dashcheck/dashcheck_test.go` — +20/-1
- `cli/pkg/provisioner/artifact_events_deduped_view_test.go` — new (+71)
- `cli/pkg/provisioner/artifact_playback_index_upgrade_test.go` — new (+72)
- `cli/pkg/provisioner/clickhouse_migration_catalog.go` — +4/-2
- `cli/pkg/provisioner/clickhouse_role_test.go` — +45/-0
- `cli/pkg/provisioner/creation_command_ack_lease_test.go` — new (+273)
- `cli/pkg/provisioner/creation_command_cas_test.go` — new (+105)
- `cli/pkg/provisioner/migrate.go` — +4/-1
- `cli/pkg/provisioner/schema_squash_postgres_test.go` — +25/-7

### pkg/proto — storage protos + generated stubs (regenerate, never hand-edit) (14)

- `pkg/proto/commodore.proto` — +103/-113
- `pkg/proto/commodore/commodore.pb.go` — +1405/-1700 — [GENERATED] regenerate via `make proto`, do not hand-edit
- `pkg/proto/commodore/commodore_grpc.pb.go` — +35/-349 — [GENERATED] regenerate via `make proto`, do not hand-edit
- `pkg/proto/foghorn.proto` — +0/-4
- `pkg/proto/foghorn/foghorn.pb.go` — +57/-67 — [GENERATED] regenerate via `make proto`, do not hand-edit
- `pkg/proto/foghorn/foghorn_grpc.pb.go` — +0/-80 — [GENERATED] regenerate via `make proto`, do not hand-edit
- `pkg/proto/foghorn_federation.proto` — +22/-31
- `pkg/proto/foghorn_federation/foghorn_federation.pb.go` — +46/-87 — [GENERATED] regenerate via `make proto`, do not hand-edit
- `pkg/proto/foghorn_federation/foghorn_federation_grpc.pb.go` — +14/-14 — [GENERATED] regenerate via `make proto`, do not hand-edit
- `pkg/proto/ipc.proto` — +139/-48
- `pkg/proto/ipc/ipc.pb.go` — +1024/-914 — [GENERATED] regenerate via `make proto`, do not hand-edit
- `pkg/proto/shared.proto` — +108/-68
- `pkg/proto/shared/shared.pb.go` — +735/-919 — [GENERATED] regenerate via `make proto`, do not hand-edit
- `pkg/proto/shared/shared_grpc.pb.go` — new (+178) — [GENERATED] regenerate via `make proto`, do not hand-edit

### pkg/clients — commodore / foghorn / quartermaster (6)

- `pkg/clients/commodore/grpc_client.go` — +17/-165
- `pkg/clients/commodore/interface.go` — +3/-12
- `pkg/clients/commodore/media_tracks.go` — new (+60)
- `pkg/clients/foghorn/grpc_client.go` — +0/-29
- `pkg/clients/quartermaster/grpc_client.go` — +34/-4
- `pkg/clients/quartermaster/list_official_pagination_test.go` — new (+83)

(`pkg/clients/periscope/grpc_client.go` + `interface.go` → SPLIT-BY-HUNK: keep node-copies/synced-count, move
TopAssets — see corrections.)

### pkg/redis (1)

- `pkg/redis/changelog.go` — +7/-0

### pkg/database — foghorn+commodore baseline/migrations, CH projection migrations, demo seed (53)

- `pkg/database/clickhouse.go` — +39/-6 — [REQUIRED-FOR-PROJECTION] CH driver used by the artifact projection
- `pkg/database/sql/clickhouse/migrations/periscope/v0.2.97/contract/001_artifact_state_current_placement.sql` — new (+12) — [REQUIRED-FOR-PROJECTION] CH migrations that gate the storage projection
- `pkg/database/sql/clickhouse/migrations/periscope/v0.2.97/expand/001_artifact_node_copy.sql` — new (+40) — [REQUIRED-FOR-PROJECTION] CH migrations that gate the storage projection
- `pkg/database/sql/clickhouse/migrations/periscope/v0.2.97/expand/002_artifact_events_event_id.sql` — new (+15) — [REQUIRED-FOR-PROJECTION] CH migrations that gate the storage projection
- `pkg/database/sql/clickhouse/migrations/periscope/v0.2.97/expand/003_artifact_state_current_local_copy.sql` — new (+11) — [REQUIRED-FOR-PROJECTION] CH migrations that gate the storage projection
- `pkg/database/sql/clickhouse/migrations/periscope/v0.2.97/expand/004_artifact_events_deduped_view.sql` — new (+24) — [REQUIRED-FOR-PROJECTION] CH migrations that gate the storage projection
- `pkg/database/sql/clickhouse/migrations/periscope/v0.2.97/postdeploy/001_artifact_state_current_ms_resolution.sql` — new (+15) — [REQUIRED-FOR-PROJECTION] CH migrations that gate the storage projection
- `pkg/database/sql/migrations/commodore/v0.2.33/expand/006_artifact_thumbnail_projection.sql` — +4/-4
- `pkg/database/sql/migrations/commodore/v0.2.97/contract/001_dvr_chapter_playback_index_realign.sql` — new (+25)
- `pkg/database/sql/migrations/commodore/v0.2.97/contract/002_creation_intent_ack_index_realign.sql` — new (+14)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/001_artifact_catalog_media_metadata.sql` — new (+26)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/002_artifact_catalog_lifecycle.sql` — new (+35)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/003_artifact_catalog_playback_ready.sql` — new (+17)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/004_dvr_chapter_playback_parent.sql` — new (+11)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/005_artifact_catalog_error_message.sql` — new (+17)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/006_artifact_catalog_tombstone.sql` — new (+23)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/008_artifact_creation_intents.sql` — new (+29)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/009_creation_intent_lease.sql` — new (+10)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/010_creation_intent_command_ack.sql` — new (+22)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/011_creation_intent_ack_backoff.sql` — new (+17)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/012_creation_intent_ack_lease.sql` — new (+12)
- `pkg/database/sql/migrations/commodore/v0.2.97/expand/013_creation_intent_ack_lease_token.sql` — new (+13)
- `pkg/database/sql/migrations/commodore/v0.2.97/postdeploy/001_creation_intent_constraints.sql` — new (+39)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/001_artifact_node_copy_version.sql` — new (+23)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/002_artifact_tracks.sql` — new (+9)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/003_artifact_catalog_projection.sql` — new (+68)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/004_outbox_retry_backoff.sql` — new (+10)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/005_catalog_projection_backoff.sql` — new (+14)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/006_node_artifact_report_watermark.sql` — new (+18)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/007_sync_attempt_identity.sql` — new (+12)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/008_dtsh_sync_attempt_identity.sql` — new (+15)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/009_dvr_start_dispatch_and_vod_processes.sql` — new (+16)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/010_vod_completion_descriptor.sql` — new (+10)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/011_artifact_creation_commands.sql` — new (+25)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/012_creation_command_expiry_index.sql` — new (+10)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/013_creation_command_consumed_at.sql` — new (+12)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/014_creation_command_terminal_gc_index.sql` — new (+15)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/015_sync_object_descriptor.sql` — new (+9)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/016_terminal_sync_identity_contract.sql` — new (+125)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/017_durable_backend_local_attribution.sql` — new (+10)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/019_active_object_key.sql` — new (+13)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/021_billing_attribution_cursor.sql` — new (+16)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/022_active_object_key_backfill_cursor.sql` — new (+14)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/023_freeze_publication_ledger.sql` — new (+22)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/024_freeze_publication_ledger_cursor.sql` — new (+12)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/025_chapter_finalize_node_binding.sql` — new (+6)
- `pkg/database/sql/migrations/foghorn/v0.2.97/expand/026_chapter_finalize_node_lifecycle_check.sql` — new (+17)
- `pkg/database/sql/migrations/foghorn/v0.2.97/postdeploy/001_terminal_sync_identity_validate.sql` — new (+14)
- `pkg/database/sql/migrations/foghorn/v0.2.97/postdeploy/002_durable_backend_local_backfill.sql` — new (+14)
- `pkg/database/sql/migrations/foghorn/v0.2.97/postdeploy/003_active_object_key_backfill.sql` — new (+24)
- `pkg/database/sql/migrations/foghorn/v0.2.97/postdeploy/004_chapter_finalize_node_validate.sql` — new (+10)
- `pkg/database/sql/schema/foghorn.sql` — +446/-12
- `pkg/database/sql/seeds/demo/clickhouse_demo_data.sql` — +171/-0 — [DEMO-SEED] artifact projection demo rows; optional for build, needed for end-to-end demo verify

### pkg/graphql — storage schema operations & fragments (10)

- `pkg/graphql/operations/fragments/ClipFields.gql` — +1/-2
- `pkg/graphql/operations/fragments/DVRRequestFields.gql` — +1/-2
- `pkg/graphql/operations/fragments/LiveUsageSummaryFields.gql` — +3/-3 — [STRADDLE] usage-summary fragment shared with analytics
- `pkg/graphql/operations/queries/GetArtifactNodeCopies.gql` — new (+21)
- `pkg/graphql/operations/queries/GetClipsConnection.gql` — del (-71)
- `pkg/graphql/operations/queries/GetDVRRequests.gql` — del (-69)
- `pkg/graphql/operations/queries/GetStorageArtifactsConnection.gql` — +33/-1
- `pkg/graphql/operations/queries/GetStreamsConnection.gql` — +2/-2
- `pkg/graphql/operations/queries/GetVodAssetsConnection.gql` — del (-68)
- `pkg/graphql/operations/queries/ListVodRetentionAssets.gql` — +3/-3

### website_application — storage routes (library, account/storage, streams/[id], stream-details) (11)

- `website_application/src/lib/components/charts/GeoView.svelte` — +246/-129 — [PROMOTED] imported by the included storage page `library/[hash]/analytics/+page.svelte`; travels with the unit
- `website_application/src/lib/components/stream-details/ArtefactsTabPanel.svelte` — +15/-6
- `website_application/src/lib/components/stream-details/OverviewTabPanel.svelte` — +5/-307
- `website_application/src/lib/components/stream-details/StreamStatusCard.svelte` — +1/-7
- `website_application/src/lib/graphql/services/explorerCatalog.ts` — +0/-24
- `website_application/src/lib/navigation.ts` — +21/-1 — [STRADDLE] nav registers both library/storage and analytics routes
- `website_application/src/routes/account/storage/+page.svelte` — +5/-2
- `website_application/src/routes/library/+page.svelte` — +453/-261
- `website_application/src/routes/library/[hash]/+page.svelte` — new (+443)
- `website_application/src/routes/library/[hash]/analytics/+page.svelte` — new (+515)
- `website_application/src/routes/streams/[id]/+page.svelte` — +12/-634

### docs — storage architecture docs, storage builder docs, storage RFC removal, release-meta (19)

- `MANIFEST_STORAGE_RELEASE_UNIT.md` — new — [RELEASE-META] this manifest; travels with the storage cut
- `docs/architecture/clickhouse-conventions.md` — new (+71)
- `docs/architecture/clips-dvr.md` — +122/-78
- `docs/architecture/creation-saga.md` — new (+94)
- `docs/architecture/decklog.md` — new (+95)
- `docs/architecture/dvr-continuous-archive.md` — +1/-1
- `docs/architecture/edge-storage-leases.md` — new (+132)
- `docs/architecture/federation.md` — +180/-16 — [STRADDLE] federation storage (mint/delete storage) vs mesh transport
- `docs/architecture/foghorn-ha.md` — +56/-23
- `docs/architecture/processing-pipeline.md` — new (+244)
- `docs/architecture/service-events.md` — +8/-3 — [STRADDLE] decklog backbone shared with analytics
- `docs/architecture/stream-replication-topology.md` — +8/-5
- `docs/architecture/thumbnails.md` — +32/-8
- `docs/architecture/trigger-durability.md` — +8/-8
- `docs/rfcs/storage-s3.md` — del (-103) — [DOC-LIFECYCLE] storage RFC removed — canonicalized into the new architecture docs
- `website_docs/src/content/docs/builders/clips.mdx` — +15/-17
- `website_docs/src/content/docs/builders/recordings.mdx` — +65/-60
- `website_docs/src/content/docs/builders/storage-and-retention.mdx` — +18/-18
- `website_docs/src/content/docs/builders/thumbnails-and-previews.mdx` — +7/-7

## Section 2 — SPLIT-BY-HUNK (straddling files, partial inclusion) (18 paths)

- `api_analytics_query/internal/grpc/server.go` — +208/-43
  - KEEP the GetArtifactNodeCopies / synced-to-S3 storage-projection handlers (the storage surface reads them).
    MOVE the `ListTopAssets` handler to the analytics branch (Top Assets is analytics). Go file — both halves must
    still compile; the storage half is [REQUIRED-FOR-PROJECTION].
- `api_gateway/internal/resolvers/analytics_connections.go` — +277/-29
  - KEEP the artifact/storage connection resolver hunks. MOVE the Top-Assets resolver hunk AND the pure-analytics
    connection hunks to the analytics branch. Confirm the kept half compiles against the split periscope client.
- `pkg/clients/periscope/grpc_client.go` — +24/-0
  - KEEP the node-copies / synced-count client methods (storage). MOVE the `TopAssets` client method to analytics.
- `pkg/clients/periscope/interface.go` — +2/-0
  - KEEP the node-copies / synced-count interface methods. MOVE the `TopAssets` interface method to analytics.
- `AGENTS.md` — +1/-0
  - KEEP only the single added index row `Artifact processing pipeline → docs/architecture/processing-pipeline.md`. Nothing else changed.
- `Makefile` — +26/-14
  - KEEP the `verify-schema` rewrite (SCHEMA_VERIFY_TESTS list + real-engine freeze/creation-command/chapter-auth/cleanup targets) — that is the storage gate. LEAVE the `verify-feature-registry` / `generate-feature-registry` (`go run . -check`) hunk on the feature-registry branch.
- `README.md` — +1/-1
  - KEEP only the Chandler service-description reword (thumbnails/sprites → 'poster frames and sprite previews'). Single row; storage/thumbnail terminology.
- `api_gateway/graph/generated/generated.go` — +3722/-3440
  - Generated from schema.graphql. Do NOT hand-split — regenerate via `make graphql-all` after the schema is split.
- `api_gateway/graph/model/models_gen.go` — +109/-69
  - Generated from schema.graphql. Do NOT hand-split — regenerate via `make graphql-all` after the schema is split.
- `pkg/clients/decklog/client.go` — +30/-148
  - KEEP the artifact/storage-event emit paths and the `Changelog.Key()`/`MaxLen()`-based atomic XADD usage. Verify no unrelated analytics-event client method rides along; move those hunks if present.
- `pkg/clients/decklog/client_more_test.go` — +0/-47
  - Moves with the client.go split — keep only storage-event-path cases.
- `pkg/clients/decklog/client_test.go` — +0/-135
  - Moves with the client.go split — keep only the test cases covering the storage-event emit paths kept above.
- `pkg/database/sql/clickhouse/periscope.sql` — +126/-12
  - KEEP the artifact projection tables/views (artifact_node_copy, artifact_state_current placement/local-copy, artifact_events dedup+event_id) that the `v0.2.97/periscope` CH migrations replay to. LEAVE unrelated analytics-only fact/view churn on the analytics branch. Baseline==replay invariant applies; verify via `make verify-schema-clickhouse`.
- `pkg/database/sql/schema/commodore.sql` — +225/-4
  - KEEP the artifact-catalog (media metadata, lifecycle, playback_ready, error_message, tombstone), DVR-chapter-playback-parent, and creation-intent tables/indexes — i.e. everything the `v0.2.97/commodore` migrations replay to. Baseline MUST equal baseline+migrations; regenerate/verify via `make verify-schema`, do not eyeball-split.
- `pkg/graphql/schema.graphql` — +127/-113
  - KEEP storage types/fields: StorageArtifacts connection, ArtifactNodeCopy(+copies query), catalog/lifecycle/playback-ready/tombstone fields, DVR/clip/vod retention. MOVE the pure-analytics additions (player-boot / session-QoE time series, and any TopAssets analytics-only wiring) to the analytics branch. Generated → regenerate via `make graphql-all`, never hand-split.
- `pkg/proto/periscope.proto` — +48/-5
  - KEEP storage: `ArtifactNodeCopy`, `GetArtifactNodeCopies*`, synced-to-S3 count fields, `has_local_copy`/reserved-26. MOVE analytics: `TopAsset`, `ListTopAssets*` — DECIDED: Top Assets is analytics (the storage library/[hash]/analytics page does not render it). Generated → regenerate via `make proto`, never hand-split.
- `pkg/proto/periscope/periscope.pb.go` — +1374/-977
  - Generated from periscope.proto. Do NOT hand-split — regenerate via `make proto` after the .proto is split.
- `pkg/proto/periscope/periscope_grpc.pb.go` — +83/-3
  - Generated from periscope.proto. Do NOT hand-split — regenerate via `make proto` after the .proto is split.

## Section 3 — EXCLUDE — separate branch (97 paths)

Grouped for readability; all are OUT of the storage cut.

### Mesh (api_mesh + mesh work) (3)

- `api_mesh/cmd/privateer/main.go` — +16/-0
- `api_mesh/internal/agent/agent.go` — +160/-22
- `api_mesh/internal/agent/agent_test.go` — +196/-0

### Ansible / infra (6)

- `ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/defaults/main.yml` — +8/-2
- `ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/meta/main.yml` — +4/-3
- `ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/molecule/default/molecule.yml` — +16/-2
- `ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install-debian.yml` — new (+110)
- `ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install.yml` — +10/-20
- `infrastructure/mistserver.conf` — +1/-1

### Ops / analytics dashboards (2)

- `pkg/grafana/dashboards/frameworks-ops.json` — +0/-9
- `pkg/metabase/specs/periscope_cards.yaml` — +2/-2

### Marketing / feature-registry (generator + source + generated) (4)

- `.prettierignore` — +1/-0
- `docs/platform-features.yaml` — +306/-10
- `scripts/registry/main.go` — +168/-15
- `website_application/src/lib/features/registry.json` — +427/-9

### Pure-analytics website pages (5)

- `website_application/src/routes/analytics/+page.svelte` — +136/-0
- `website_application/src/routes/analytics/audience/+page.svelte` — +557/-511
- `website_application/src/routes/analytics/player-experience/+page.svelte` — +519/-417
- `website_application/src/routes/analytics/qoe/+page.server.ts` — +2/-2
- `website_application/src/routes/analytics/usage/+page.svelte` — +7/-7

### Pure-analytics GraphQL operations (3)

- `pkg/graphql/operations/queries/GetPlayerBootTimeSeries.gql` — +1/-1
- `pkg/graphql/operations/queries/GetSessionQoeTimeSeries.gql` — +1/-1
- `pkg/graphql/operations/queries/GetTopAssets.gql` — new (+15) — Top Assets is analytics; the storage library/[hash]/analytics page does not render it

### Analytics Top-Assets handlers / tests (moved out; see corrections) (2)

- `api_analytics_query/internal/grpc/top_assets_test.go` — new (+50) — tests the ListTopAssets analytics handler
- `api_gateway/internal/resolvers/analytics_conn_b_resolver_test.go` — +11/-8 — covers the analytics/top-assets resolver path

### Non-storage architecture docs (22)

- `docs/architecture/agent-access.md` — +23/-19
- `docs/architecture/analytics-pipeline.md` — +22/-5
- `docs/architecture/bootstrap-desired-state.md` — +33/-11
- `docs/architecture/build-and-packaging.md` — +4/-0
- `docs/architecture/cluster-rollout.md` — new (+131)
- `docs/architecture/cross-cluster-billing.md` — +22/-0
- `docs/architecture/deckhand.md` — +4/-4
- `docs/architecture/demo-mode.md` — new (+110)
- `docs/architecture/finalized-fact-tables.md` — +1/-1
- `docs/architecture/livepeer-signer.md` — new (+60)
- `docs/architecture/meter-contracts.md` — +15/-0
- `docs/architecture/module-map.md` — new (+218)
- `docs/architecture/navigator-aliases.md` — new (+142)
- `docs/architecture/node-enrollment.md` — new (+94)
- `docs/architecture/os-tuning.md` — +4/-3
- `docs/architecture/payment-settlement.md` — +96/-8
- `docs/architecture/privateer-mesh.md` — new (+126)
- `docs/architecture/skipper.md` — +9/-3
- `docs/architecture/steward.md` — new (+64)
- `docs/architecture/tls.md` — +27/-1
- `docs/architecture/viewer-routing.md` — +1/-1
- `docs/architecture/webhook-routing.md` — +1/-1

### RFCs — the mechanical +4 header sweep + all other proposals (non build-required) (38)

- `docs/rfcs/RFC_vault_secrets_management.md` — +9/-3
- `docs/rfcs/bulk-operations.md` — +4/-0
- `docs/rfcs/capacity-planning.md` — +8/-2
- `docs/rfcs/cluster-os-update-drain.md` — +4/-0
- `docs/rfcs/complexity-aware-rate-limiting.md` — +4/-0
- `docs/rfcs/cross-cluster-durable-replication-v1.md` — new (+139)
- `docs/rfcs/database-layer-strategy.md` — +7/-5
- `docs/rfcs/dns-anycast.md` — +8/-3
- `docs/rfcs/ens-frameworks-subdomains.md` — +4/-0
- `docs/rfcs/ens-streaming-spec.md` — +4/-0
- `docs/rfcs/external-player-qoe.md` — new (+144)
- `docs/rfcs/federated-settlement-attribution.md` — new (+346)
- `docs/rfcs/federation-plane-pluggability.md` — new (+367)
- `docs/rfcs/flow-automation.md` — +4/-0
- `docs/rfcs/grpc-tls-mesh.md` — +4/-0
- `docs/rfcs/horizontal-scaling.md` — +4/-0
- `docs/rfcs/internal-ca-intermediate-rotation.md` — +9/-5
- `docs/rfcs/large-file-refactor.md` — +4/-0
- `docs/rfcs/live-commerce.md` — +25/-1
- `docs/rfcs/live-dvr-processing-plan.md` — +4/-0
- `docs/rfcs/lookout.md` — +4/-0
- `docs/rfcs/mesh-isolation.md` — +4/-0
- `docs/rfcs/nat-traversal.md` — +41/-2
- `docs/rfcs/network-security-capabilities.md` — +4/-0
- `docs/rfcs/node-drain.md` — +4/-0
- `docs/rfcs/parlor.md` — +15/-5
- `docs/rfcs/placement-policy-engine.md` — new (+212)
- `docs/rfcs/processing-orchestration.md` — +16/-0
- `docs/rfcs/referral-attribution-network-usage.md` — +24/-18
- `docs/rfcs/service-identity-and-cluster-binding.md` — +5/-1
- `docs/rfcs/stream-balances.md` — +4/-0
- `docs/rfcs/stream-replication.md` — +222/-49
- `docs/rfcs/token-authority.md` — +15/-4
- `docs/rfcs/vod-s3-optimization.md` — +4/-0
- `docs/rfcs/vod-upload-validation.md` — +4/-0
- `docs/rfcs/wireguard-ospf.md` — +4/-0
- `docs/rfcs/workload-cost-model.md` — new (+169)
- `docs/rfcs/xmpt-chattins.md` — +4/-0

### website_docs — marketing / operator / migrate / blog / generated (12)

- `website_docs/astro.config.mjs` — +5/-1
- `website_docs/public/llms-full.txt` — +384/-122
- `website_docs/scripts/check-graphql-examples.mjs` — +7/-2
- `website_docs/src/content/docs/blog/agent-native-access.mdx` — +1/-1
- `website_docs/src/content/docs/migrate/bunny-stream.mdx` — +1/-1
- `website_docs/src/content/docs/migrate/livepeer-studio.mdx` — +1/-1
- `website_docs/src/content/docs/operators/clickhouse-migrations.mdx` — +17/-8
- `website_docs/src/content/docs/operators/dns.mdx` — +2/-2
- `website_docs/src/content/docs/operators/running-upgrades.mdx` — +58/-4
- `website_docs/src/content/docs/platform/feature-matrix.mdx` — +11/-1
- `website_docs/src/content/docs/platform/platform-capabilities.mdx` — new (+138)
- `website_docs/src/content/docs/roadmap.mdx` — +74/-2

## Straddle / entanglement decisions (resolved — the settled cut rule for each entanglement)

1. **periscope TopAssets → ANALYTICS.** `ListTopAssets`/`TopAsset` and everything that reads them leave the unit
   (`GetTopAssets.gql`, `top_assets_test.go`, `analytics_conn_b_resolver_test.go` → EXCLUDE) or are hunk-split
   (`periscope.proto`, `periscope` client, `analytics_connections.go`, `api_analytics_query/.../server.go` — keep
   the artifact/node-copy/synced hunks, move ListTopAssets). The proto is regenerated after the source split, never
   half-cut.
2. **Two hand-split baselines (`commodore.sql`, `clickhouse/periscope.sql`) → keep the storage-projection hunks and
   VERIFY, never eyeball-split.** Keep the artifact-catalog / DVR-chapter-parent / creation-intent (commodore) and
   artifact-projection (periscope) tables/views the `v0.2.97` migrations replay to; drop pure-analytics fact/view
   churn. Gate: `make verify-schema` (+ `verify-schema-clickhouse`) must hold baseline==baseline+migrations.
   `foghorn.sql` is all-storage → kept whole, same gate.
3. **All generated proto/graphql outputs → REGENERATE, never hand-edit.** After splitting the source
   protos/`schema.graphql`/`.gql` set, run `make proto` / `make graphql-all` and commit the regenerated
   `*.pb.go`, `generated.go`, `models_gen.go`, `$houdini`, and `GeneratedQueries.swift` as-is. A partial cut of a
   generated file will not compile.
4. **`app_mac/.../GeneratedQueries.swift` → regenerate on the storage-branch `.gql` set.** Because the excluded
   `GetPlayerBootTimeSeries.gql` / `GetSessionQoeTimeSeries.gql` / `GetTopAssets.gql` are cut, regenerate the swift
   from the surviving operations so the mac build references no removed operation.
5. **`analytics_connections.go` → SPLIT-BY-HUNK** (moved to Section 2): keep artifact/storage connection resolvers,
   move Top-Assets + pure-analytics resolvers. Both halves must compile.
6. **decklog client split → keep the storage/freeze-ledger hunks; `pkg/redis/changelog.go` stays with them.** Keep
   the artifact/storage-event emit paths and the `Changelog.Key()/MaxLen()` atomic multi-key XADD usage (the
   freeze ledger consumes it); move any unrelated analytics-event client hunks. The redis change is NOT orphaned —
   it ships with the decklog storage hunks that use it.
7. **Cross-module build closure → each touched module ships all its touched packages together.** api*balancing,
   api_control, api_sidecar, api_gateway, api_analytics*_, api_firehose, api_tenants, cli are independent Go
   modules; a partial package cut won't build. This is why `cli/cmd/cluster_`+`cli/pkg/dashcheck`ride along
(same`cli` module as the provisioner storage migrations).
8. **api*analytics*\* / api_firehose / pkg/database CH files are load-bearing → KEPT [REQUIRED-FOR-PROJECTION].** The
   end-to-end storage surface (GetArtifactNodeCopies, synced-to-S3 counts) only builds+verifies with the
   artifact-events ingest → periscope projection → query path present; dropping them yields per-service compiles
   that fail the end-to-end storage verify.
9. **`charts/GeoView.svelte` → PROMOTED into the unit.** It IS imported by the included storage page
   `library/[hash]/analytics/+page.svelte`, so it travels with the unit (moved to Section 1). No stub needed.
10. **`navigation.ts` + `LiveUsageSummaryFields.gql` → KEPT in the unit; the split drops the analytics nav
    entries/fragment fields as part of removing the analytics routes.** Kept for buildability; when the analytics
    routes/pages are cut, the corresponding nav registrations and analytics-only fragment fields are removed in the
    same hunk so nothing dangles.
11. **Storage docs → INCLUDED for narrative completeness (no build impact); may move to a paired docs PR if the
    release process forbids doc churn in a code unit.** The 13 storage architecture docs + 4 builder docs + the
    `storage-s3.md` RFC deletion do not affect build.
12. **Makefile → SHIP ONLY the `verify-schema` hunk** (Section 2). A whole-file take drags the excluded
    feature-registry generator target into the storage branch.
