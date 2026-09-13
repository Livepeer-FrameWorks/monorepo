# Local two-cell media fixture

`MIST_IMAGE=<full-feature-image> make verify-two-cell-media` exercises publication
and cross-cell playback. `MEDIA_LIFECYCLE_SKIP_BUILD=1 MIST_IMAGE=<image> make
verify-media-lifecycle` extends the running fixture with clipping, DVR, VOD and
analytics checks. The image must include `MistProcAV` and `MistProcThumbs`.

Each edge proxy shares its Mist container's network namespace and listens on
port 8082. Advertise that origin as `EDGE_PUBLIC_URL`: media goes to Mist's 8080
listener, while `/internal/artifact/` goes to Helmsman's authenticated relay on 18007. Advertising Mist's media-only listener breaks peer artifact reads.
Sharing the namespace keeps the same hostname usable for DTSC on port 4200.
Use Compose to restart Mist so its dependent proxy is restarted too. After a
direct `docker restart` of Mist, restart the matching edge proxy explicitly:
the proxy otherwise retains the old network namespace and port 8082 is absent
from the restarted edge.

Mist B and Helmsman B share the recordings volume. `edge-storage-init-b` creates
the writable recordings root as the service user before Helmsman starts; it
does not clear recordings or change ownership recursively.

Allow sufficient Docker disk headroom for image builds, recording writes and
PostgreSQL/Kafka logs. Database recovery or an HTTP redirect alone is not a
successful media test: the lifecycle proof fetches real transport-stream bytes.
Existing images, volumes and recordings are not disposable build cache.

After recreating Foghorn containers, refresh Commodore's local connections too.
Docker may reassign old container IPs to another cell, while an existing gRPC
connection can retain its previous resolved address. A coordinated local
rollout avoids testing through connections from the previous topology; do not
change catalog ownership or S3 settings to compensate for a stale connection.

The fixture's `mistserver` and `mistserver-b` advertisements are Docker-network
names, not automatically browser-reachable host addresses. A browser proof needs
reachable edge proxy ports and names that also remain valid for peer DTSC pulls;
rewriting only a returned playback URL does not test the advertised topology.
Build the webapp with the same `VITE_APP_URL` base used by the browser, and put its
compiled runtime behind the same-origin `/auth` and `/graphql` proxy. A standalone
Node server without those proxy routes is not the complete app deployment.

On a Mac, host sleep can change Docker's wall-clock/monotonic relationship
while long-lived media processes retain their original clock anchor. Do not
classify clipping seek failures from that state as release regressions. Validate
with a fresh, awake media runtime; coordinate any restart with active recordings
and restart namespace-sharing proxies together with Mist. Keeping the machine
awake prevents idle sleep, not an explicit sleep or closed lid.

`MIST_CONTRACT_IMAGE=<image> make verify-mist-finite-source` checks bounded live
MKV extraction in an isolated fresh controller. `make verify-mist-hls-realtime`
with the same variable compares direct HLS input to processing-mode input on
synthetic media. Neither test relies on the running development stack's clock.
