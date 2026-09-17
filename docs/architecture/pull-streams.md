# Pull-Input Streams — MistServer pulls from a configured upstream URI

A pull stream's media bytes come from an external URL (HLS, RTSP, SRT, DTSC, MPEG-TS, EBML/Matroska) instead of an encoder pushing into FrameWorks. The platform pulls on demand when the first viewer connects, fans out via in-cluster DTSC, and stops cleanly when the last viewer leaves.

## Architecture

```
viewer ─→ Gateway ─→ Commodore (which Foghorn?) ─→ Foghorn (which node?)
                                                       │
                                                       ▼
viewer connects ─→ Mist on chosen node opens source = balance:<foghorn-base>
                                                       │
                                                       ▼
                                  MistInBalancer asks Foghorn /source
                                                       │
                          ┌────────────────────────────┴────────────────────────────┐
                          ▼                                                          ▼
	       active in-cluster DTSC node                 configured upstream URI
	       /source may return dtsc://node:4200          /source may return upstream directly
                                                                           │
                                                                           ▼
                                                        Mist's HLS/RTSP/SRT/...
                                                        input pulls from upstream
```

The upstream URI is a **first-class origin candidate**, not a fallback string. Foghorn's `/source` ignores the `?fallback=` query param for pull streams (it's an attacker-controlled source-injection surface) and opens the stored URI from the verified cell-sealed media authority. During mixed-version rollout only, an unmarked projection may use Commodore `ResolvePullSourceByInternalName` as its connected fallback.

## Service Responsibilities

| Service                          | Role                                                                                                                                                                                                                                  | Data     |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| Commodore (api_control)          | Owns `commodore.streams.ingest_mode` discriminator + `commodore.stream_pull_sources` (encrypted upstream URI, enabled). Exposes `ResolvePullSourceByInternalName` to Foghorn. Bootstrap reconciler seeds operator-owned pull streams. | Postgres |
| Foghorn (api_balancing)          | Picks origin at `/source` (active in-cluster DTSC node vs. configured upstream URI); cold-start viewer routing drops the active-stream-presence requirement so the first viewer can land somewhere.                                   | —        |
| Helmsman / Sidecar (api_sidecar) | Seeds Mist with the base `pull` stream config (`source = balance:<foghorn-base>`) and `pull+` in `STREAM_PROCESS` for transcode/record/thumb parity with `live+`.                                                                     | —        |
| MistServer                       | Built-in `MistInHLS`/`MistInRTSP`/`MistInSRT`/`MistInDTSC`/`MistInTS`/`MistInEBML` pull-input modules; `MistInBalancer` chooses between the in-cluster DTSC fanout and the upstream URI.                                              | —        |

## Data Flows

### Cold-start viewer (first request on an inactive pull stream)

```
1. Viewer hits /play/<playback_id> on Gateway.
2. Commodore → ResolveViewerEndpoint picks a Foghorn cluster.
3. Foghorn → Commodore.ResolvePlaybackID (ingest_mode=pull).
4. Foghorn resolver appends pull+ prefix → pull+<internal>.
5. Foghorn viewer placement: a configured input needs no existing live copy,
   so a cluster the signed source entitles to dial it is already a feasible
   destination; policy and capacity then pick the edge.
6. Viewer routed to chosen edge.
7. Mist on that edge starts `pull+<internal>` from the base `pull` config's
   `balance:<foghorn-base>` source.
8. Mist input_balancer calls /source on Foghorn.
9. Foghorn /source detects pull+, ignores ?fallback=, calls
    Commodore.ResolvePullSourceByInternalName, gets the upstream URI back.
10. No in-cluster node has the stream yet -> /source returns the upstream URI.
11. Mist starts the matching input module (HLS/RTSP/...) and begins pulling.
12. Subsequent viewers landing on other edges hit /source. Once a DTSC origin is
    active, Foghorn prefers the in-cluster fanout until there is real upstream
    geo/latency data worth comparing.
```

### Tenant or operator creates a pull stream

```
Operator (gitops):  bootstrap.yaml → commodore.pull_streams: [...]
                    → cli render → rendered.yaml
                    → commodore bootstrap → ReconcilePullStreams
                    → INSERT commodore.streams (ingest_mode='pull')
                    → INSERT commodore.stream_pull_sources (source_uri_enc, enabled, ...)
                    → ReconcileStreamSourceLocations writes source_location as the
                      stream's own ingest rules (actor system:bootstrap)

Tenant self-service: GraphQL createStream(ingestMode: PULL, pullSource: {...},
                                          sourceLocation: {...})
                     → validates the source URI, eligible edge clusters, owned nodes
                       and private-source consent
                     → encrypts source_uri
                     → same DB rows under the customer tenant, plus the stream's
                       own ingest rules in the same transaction.
```

## Cost Semantics

Pull streams meter exactly like push live streams once viewers attach. Idle pull streams cost nothing.

| Situation                            | Billed?                                                                                                                        |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------ |
| Stream configured, **no viewers**    | Nothing. No viewer minutes, no egress, no transcoding, no recording storage, no upstream bandwidth.                            |
| First viewer connects                | Mist starts the pull on one edge. Viewer minutes + egress meter normally. Upstream pull is on us, not the customer separately. |
| Multiple viewers, fanout established | Standard viewer/egress metering. In-cluster DTSC fanout is preferred once active.                                              |
| Recording (DVR) enabled              | Records exactly like a recorded push stream. Storage billed identically.                                                       |
| Transcoding ladder applied           | Transcoded minutes meter identically to push.                                                                                  |
| Last viewer leaves                   | Mist drops the input after the standard grace period. No cost while idle.                                                      |

`always_on: true` is not yet wired for pull streams — the column exists on `commodore.streams` (today used by `mist_native` streams, which always run regardless of viewer demand) but Foghorn's reconciler does not yet keep `pull+` always-on streams active without a viewer. When wired, an always-on pull stream will accrue processing/recording/thumbnail/health/upstream-bandwidth costs even with zero viewers; the API surface documents this explicitly.

## Tenant Responsibilities

- **Licence and rights** for the upstream content are the tenant's responsibility. The platform validates that the URI matches a supported Mist input pattern and rejects localhost/private literal hosts where appropriate — it does not validate redistribution rights.
- **Where the source runs is the stream's source location.** A stream's source location (ANY or RESTRICTED to clusters, optional per-cluster nodes, optional avoided nodes) is stored as the stream's own ingest placement rules; see [source location](media-placement-policy.md#source-location). Once the signed authority is ready, Foghorn evaluates those rules for the exact node before it dials the upstream at `STREAM_SOURCE`, `/source` and federation source feasibility, so a LAN camera restricted to one node is never dialed from another node of the same cluster; a denied node relays from a permitted origin instead. Public sources can leave the location ANY to run on any entitled media cluster.
- **Private-network sources require a restricted location on consenting clusters.** A pull-source URI is classified `Public`, `Private`, or `Blocked` by `pkg/pullsource.Classify`. For a Private source (RFC1918 / ULA / non-link-local multicast literal), Commodore requires the effective ingest policy (tenant rules or the stream's own) to confine it to clusters that have `allow_private_pull_sources=true`. The capability flag lives on `quartermaster.infrastructure_clusters.allow_private_pull_sources`, sourced from cluster.yaml, propagated through `api_tenants/internal/bootstrap` reconcile, and exposed to tenants as `ClusterAccess.allowPrivatePullSources`. CLI render and connected CRUD validate the configuration; Commodore then snapshots the effective grant/capability into signed tenant authority. Foghorn `/source` and `STREAM_SOURCE` enforce the local signed intersection without a Quartermaster request once the authority is ready. Hostnames are `Public` by syntax — DNS resolution is the operator's responsibility per cluster, since the same hostname can resolve differently from each edge.
- **Upstream credentials** carried in-URI (e.g. `rtsp://user:pass@host`) are encrypted at rest in `commodore.stream_pull_sources.source_uri_enc` using a purpose-isolated FieldEncryptor (`pull-source-uri`). Delivery to an eligible media cell is separately encrypted in that cell's X25519-sealed authority section.
- **Upstream availability** is the tenant's contract with their origin. If the source is unreachable, the Mist input exits and the next viewer triggers a fresh pull attempt.

## Supported URI Schemes

Validated and classified from the URI before being handed to Mist:

| Scheme     | Mist input module | Notes                                                                                                                                                    |
| ---------- | ----------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `https://` | HLS / TS / EBML   | matched by URI suffix (`.m3u8`, `.ts`, `.mkv`)                                                                                                           |
| `http://`  | HLS / TS / EBML   | accepted; prefer `https://`                                                                                                                              |
| `rtsp://`  | RTSP              | basic-auth in URI; `rtsps://` not supported                                                                                                              |
| `srt://`   | SRT               |                                                                                                                                                          |
| `rist://`  | RIST              | MPEG-TS over RIST                                                                                                                                        |
| `dtsc://`  | DTSC              | pull from another Mist node (self-hosted, etc.)                                                                                                          |
| `tsudp://` | TS over UDP       | unicast / multicast; RFC1918 + non-link-local multicast literals require a source location restricted to clusters with `allow_private_pull_sources=true` |

**RTMP pull is NOT supported** — there is no `MistInRTMP` pull module in the FrameWorks fork. RTMP stays push-only.

## Key Files

- `pkg/database/sql/schema/commodore.sql` — `streams.ingest_mode` column + `stream_pull_sources` table.
- `pkg/proto/commodore.proto` — `ResolvePlaybackIDResponse.ingest_mode`, `ResolvePullSourceByInternalName` RPC.
- `api_control/internal/grpc/server.go:ResolvePullSourceByInternalName` — connected-mode Commodore decryption + lookup; signed authority compilation uses the same owner data.
- `api_control/internal/bootstrap/pull_streams.go` — `ReconcilePullStreams` for declarative gitops seeding; `source_location.go` writes the declared source locations.
- `api_control/internal/placementpolicy/source_location.go` — source location ↔ ingest rules, private-source bound check.
- `api_control/internal/grpc/stream_source_location.go` — source location validation and write on stream create/update.
- `api_balancing/internal/control/source_dial_placement.go` — per-node ingest-policy verdict for dialing a configured source.
- `api_sidecar/internal/config/manager.go` — `pull` Mist template seed; `pull+` in `STREAM_PROCESS` filter.
- `api_balancing/internal/control/server.go` — `pull` template added to `composeConfigSeed`.
- `api_balancing/internal/triggers/processor.go:resolvePullSource` — validates the local signed pull source and placement for trigger-started paths, with connected fallback only before cutover.
- `api_balancing/internal/handlers/handlers.go:handleGetSource` — pull-aware `/source` (untrusted `?fallback=` ignored, upstream URI fetched server-side).
- `api_balancing/internal/federation/placement_configured_paths.go` — viewer placement for `pull+`: signed consent to dial the configured input makes a cluster feasible with no live copy yet, and an existing origin copy is offered as a relay source.
- `api_balancing/internal/control/resolver.go` — kind-aware prefix (`live+` vs `pull+`) on `ResolvePlaybackID`.
- `cli/pkg/bootstrap/types.go` — `CommodoreSection`, `PullStream`, `PullStreamRendered`.
- `pkg/pullsource` — shared pull-source URI classification, host guard, and redaction.

## Gotchas

- **`getStreamConfig` strips at first `+`.** A literal `pull+$` SHM key is inert because Mist looks up SHM key `pull` (no plus) when starting `pull+abc`. Pull streams therefore seed the base `pull` config with `source = balance:<foghorn-base>`.
- **`?fallback=` at `/source` is untrusted for pull streams.** It used to mean "use this if no in-cluster node has the stream" — fine for `live+`'s `push://` literal, but a source-injection surface for pull. The handler ignores it for `pull+` and resolves the upstream from verified local authority (or connected Commodore only before that authority is marked ready).
- **The upstream URI is an origin candidate, not a fallback string.** If two distant requesters arrive simultaneously before any node is active, both their edges may pull from upstream independently. Once one is active, subsequent requesters prefer the DTSC fanout.
- **No synthetic geo-CDN scoring.** The platform does not currently have measured latency or geolocation for arbitrary upstream hosts, so active DTSC fanout wins once available.
- **The process cache is not authority.** `triggers.Processor.streamCache` remains a connected-mode/runtime optimization. Ready signed object+tenant projections live in the Foghorn database and survive process restart; trigger and viewer paths use those first.
- **Source URI encryption purpose is `pull-source-uri`** (HKDF-isolated from `push-target-uri` and `playback-webhook-secret`). The bootstrap CLI must derive with the same purpose string.
- **Pull stream `stream_key` is a placeholder.** Push doesn't apply to pull, but the column is `NOT NULL`, so bootstrap inserts `pull-<playback_id>` as a stable filler.
