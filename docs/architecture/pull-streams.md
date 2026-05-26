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

The upstream URI is a **first-class origin candidate**, not a fallback string. Foghorn's `/source` ignores the `?fallback=` query param for pull streams (it's an attacker-controlled source-injection surface) and looks up the stored URI server-side from Commodore via `ResolvePullSourceByInternalName`.

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
5. Foghorn ResolveLivePlayback: pull-aware cold-start path drops the
   active-stream-presence filter; picks an eligible edge by capacity/geo.
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

Tenant self-service: GraphQL createStream(ingestMode: PULL, pullSource: {...})
                     → validates the source URI and eligible edge clusters
                     → encrypts source_uri
                     → same DB rows under the customer tenant.
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
- **Private-network sources require both a placement pin and a capable cluster.** A pull-source URI is classified `Public`, `Private`, or `Blocked` by `pkg/pullsource.Classify`. Private (RFC1918 / ULA / non-link-local multicast literal) requires explicit `allowed_cluster_ids` on the pull source; every listed cluster must be media-capable and have `allow_private_pull_sources=true`. The capability flag lives on `quartermaster.infrastructure_clusters.allow_private_pull_sources`, sourced from cluster.yaml and propagated through `api_tenants/internal/bootstrap` reconcile. CLI render validates pins against the rendered manifest's media clusters; the Commodore reconciler and runtime CRUD re-check against Quartermaster; Foghorn cold-start routing, `/source`, and STREAM_SOURCE enforce the same placement intersection. Public sources can leave `allowed_cluster_ids` empty to run on any media cluster, or set it to pin placement. Hostnames are `Public` by syntax — DNS resolution is the operator's responsibility per cluster, since the same hostname can resolve differently from each edge.
- **Upstream credentials** carried in-URI (e.g. `rtsp://user:pass@host`) are encrypted at rest in `commodore.stream_pull_sources.source_uri_enc` using a purpose-isolated FieldEncryptor (`pull-source-uri`).
- **Upstream availability** is the tenant's contract with their origin. If the source is unreachable, the Mist input exits and the next viewer triggers a fresh pull attempt.

## Supported URI Schemes

Validated and classified from the URI before being handed to Mist:

| Scheme     | Mist input module | Notes                                                                                                                                                         |
| ---------- | ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `https://` | HLS / TS / EBML   | matched by URI suffix (`.m3u8`, `.ts`, `.mkv`)                                                                                                                |
| `http://`  | HLS / TS / EBML   | accepted; prefer `https://`                                                                                                                                   |
| `rtsp://`  | RTSP              | basic-auth in URI; `rtsps://` not supported                                                                                                                   |
| `srt://`   | SRT               |                                                                                                                                                               |
| `rist://`  | RIST              | MPEG-TS over RIST                                                                                                                                             |
| `dtsc://`  | DTSC              | pull from another Mist node (self-hosted, etc.)                                                                                                               |
| `tsudp://` | TS over UDP       | unicast / multicast; RFC1918 + non-link-local multicast literals require explicit `allowed_cluster_ids` whose clusters have `allow_private_pull_sources=true` |

**RTMP pull is NOT supported** — there is no `MistInRTMP` pull module in the FrameWorks fork. RTMP stays push-only.

## Key Files

- `pkg/database/sql/schema/commodore.sql` — `streams.ingest_mode` column + `stream_pull_sources` table.
- `pkg/proto/commodore.proto` — `ResolvePlaybackIDResponse.ingest_mode`, `ResolvePullSourceByInternalName` RPC.
- `api_control/internal/grpc/server.go:ResolvePullSourceByInternalName` — Commodore-side decryption + lookup.
- `api_control/internal/bootstrap/pull_streams.go` — `ReconcilePullStreams` for declarative gitops seeding.
- `api_sidecar/internal/config/manager.go` — `pull` Mist template seed; `pull+` in `STREAM_PROCESS` filter.
- `api_balancing/internal/control/server.go` — `pull` template added to `composeConfigSeed`.
- `api_balancing/internal/triggers/processor.go:resolvePullSource` — validates pull source metadata for trigger-started paths.
- `api_balancing/internal/handlers/handlers.go:handleGetSource` — pull-aware `/source` (untrusted `?fallback=` ignored, upstream URI fetched server-side).
- `api_balancing/internal/control/playback.go:ResolveLivePlayback` — cold-start retry without active-stream filter for `pull+`.
- `api_balancing/internal/control/resolver.go` — kind-aware prefix (`live+` vs `pull+`) on `ResolvePlaybackID`.
- `cli/pkg/bootstrap/types.go` — `CommodoreSection`, `PullStream`, `PullStreamRendered`.
- `pkg/pullsource` — shared pull-source URI classification, host guard, and redaction.

## Gotchas

- **`getStreamConfig` strips at first `+`.** A literal `pull+$` SHM key is inert because Mist looks up SHM key `pull` (no plus) when starting `pull+abc`. Pull streams therefore seed the base `pull` config with `source = balance:<foghorn-base>`.
- **`?fallback=` at `/source` is untrusted for pull streams.** It used to mean "use this if no in-cluster node has the stream" — fine for `live+`'s `push://` literal, but a source-injection surface for pull. The handler ignores it for `pull+` and re-resolves from Commodore.
- **The upstream URI is an origin candidate, not a fallback string.** If two distant requesters arrive simultaneously before any node is active, both their edges may pull from upstream independently. Once one is active, subsequent requesters prefer the DTSC fanout.
- **No synthetic geo-CDN scoring.** The platform does not currently have measured latency or geolocation for arbitrary upstream hosts, so active DTSC fanout wins once available.
- **Foghorn local stream cache is trigger-owned.** `triggers.Processor.streamCache` fills via `PUSH_REWRITE` (push) or `PLAY_REWRITE` / `STREAM_SOURCE` (pull). The cold-start routing path in `ResolveLivePlayback` only chooses an eligible edge; the trigger path records tenant and stream context.
- **Source URI encryption purpose is `pull-source-uri`** (HKDF-isolated from `push-target-uri` and `playback-webhook-secret`). The bootstrap CLI must derive with the same purpose string.
- **Pull stream `stream_key` is a placeholder.** Push doesn't apply to pull, but the column is `NOT NULL`, so bootstrap inserts `pull-<playback_id>` as a stable filler.
