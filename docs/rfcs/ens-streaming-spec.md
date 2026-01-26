# RFC: Livepeer ENS Streaming Text Records

## Status
Draft

## TL;DR
- Define ENS text record spec for Livepeer ecosystem streaming.
- Fields: playbackId, gateway URL, HLS URL, WHEP URL.
- Enable portable streaming identity across gateways.

## Current State (as of 2026-01-26)
- No standard exists for streaming endpoints in ENS text records.
- Streamers use platform-specific URLs.
- Switching platforms requires rebuilding audience and updating all links.

Evidence:
- [Source] ENS text record spec (ENSIP-5): https://docs.ens.domains/ensip/5
- [Evidence] No existing `com.livepeer.*` text record standards.

## Problem / Motivation
Streaming identity is siloed per-platform:
- Platform lock-in (audience tied to platform URL, not creator)
- Fragmented identity across gateways
- No universal "stream address" for web3

## Goals
- Define text record keys for streaming playback.
- Enable NPM player integration via ENS.
- Support external players with raw URLs.
- Support both HLS and WebRTC (WHEP) protocols.

## Non-Goals
- Defining ingest endpoints (security risk - streamKey must remain secret).
- Specifying offchain subdomain implementation (gateway-specific).

## Proposal

### Text Record Keys

| Key | Required | Description | Example Value |
|-----|----------|-------------|---------------|
| `com.livepeer.playbackId` | Yes | Playback identifier for API calls | `abc123def456` |
| `com.livepeer.gateway` | Yes | GraphQL gateway URL | `https://api.frameworks.network/graphql` |
| `com.livepeer.stream` | Yes | HLS playback URL | `https://play.frameworks.network/play/abc123def456/index.m3u8` |
| `com.livepeer.whep` | Recommended | WebRTC playback URL (low-latency) | `https://play.frameworks.network/play/abc123def456.webrtc` |

### Field Semantics

**`com.livepeer.playbackId`** (Required)
- The stream's playback identifier (16-character string).
- Used by SDK to call `resolveViewerEndpoint(contentId: playbackId)`.
- Not the streamKey (which is secret for ingest).

**`com.livepeer.gateway`** (Required)
- GraphQL gateway endpoint URL.
- NPM player calls this to resolve geo-routed playback endpoints.
- Format: Full URL including `/graphql` path.

**`com.livepeer.stream`** (Required)
- Direct HLS playback URL.
- For external players (hls.js, Video.js) that don't use the SDK.
- Foghorn returns 307 redirect to nearest edge node.

**`com.livepeer.whep`** (Recommended)
- WebRTC playback URL for low-latency viewing.
- For WebRTC-capable players.
- Foghorn returns 307 redirect to nearest edge node.

### URL Patterns

Based on Foghorn load balancer (production: `play.frameworks.network`):

| Protocol | URL Pattern |
|----------|-------------|
| HLS | `/play/{playbackId}/index.m3u8` |
| WHEP | `/play/{playbackId}.webrtc` |
| DASH | `/play/{playbackId}.mpd` |
| JSON (all protocols) | `/play/{playbackId}` |

All playback URLs return HTTP 307 redirects to the optimal edge node.

### Consumer Integration Flows

**1. NPM Player**
```tsx
<Player ens="streamer.eth" />

// Internal flow:
// 1. Resolve ENS text records
// 2. Read com.livepeer.playbackId + com.livepeer.gateway
// 3. Call resolveViewerEndpoint(contentId: playbackId) on gateway
// 4. Get geo-routed endpoints with all protocols
// 5. Select best protocol and play
```

**2. External HLS Player (hls.js)**
```javascript
// Resolve ENS using ethers.js/viem
const records = await resolveENS("streamer.eth");
const hlsUrl = records["com.livepeer.stream"];

// Play directly
const hls = new Hls();
hls.loadSource(hlsUrl);  // 307 redirects to edge
hls.attachMedia(videoElement);
```

**3. External WebRTC Player**
```javascript
const records = await resolveENS("streamer.eth");
const whepUrl = records["com.livepeer.whep"];

// WHEP negotiation
const pc = new RTCPeerConnection();
// ... WHEP handshake with whepUrl
```

### Security Considerations

**No ingest URLs**: This spec excludes streamKey and WHIP ingest endpoints. The streamKey is a secret used for RTMP/WHIP publishing. Exposing it would allow anyone to push to the stream.

**playbackId is public**: The playbackId is designed to be shared publicly. It only grants viewing access, not publishing.

**Domain validation**: Players SHOULD validate playback URLs belong to known Livepeer gateway domains to prevent redirect attacks.

### Portable Identity

1. Streamer owns `alice.eth` or receives `alice.frameworks.eth` subdomain.
2. Gateway populates text records with playbackId and URLs.
3. Audience uses ENS name as stream address.
4. Streamer switches gateways → updates text records → audience unaffected.

## Impact / Dependencies

| Component | Change Required |
|-----------|-----------------|
| NPM Player | Add ENS resolution, read text records, call gateway |
| Gateway API | Populate ENS text records when streams created |
| ENS | None (uses existing text record infrastructure) |

## Alternatives Considered

**Only playbackId + gateway (no raw URLs)**: Rejected because external players need direct URLs without calling our API. Raw URLs support decentralization and simple embedding.

**Only raw URLs (no playbackId)**: Rejected because SDK integration benefits from calling `resolveViewerEndpoint` for geo-routing, fallbacks, and metadata.

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| External player adoption | Raw URLs work with any HLS/WHEP player today |
| Stale URLs after gateway change | User updates ENS records; 307 redirects minimize stale cache issues |
| Wrong domain in URL | Players can allowlist known gateway domains |

## Migration / Rollout

1. FrameWorks implements text record population for streams.
2. FrameWorks NPM player adds ENS resolution.
3. Document spec publicly.
4. Invite other Livepeer gateways to adopt.

## Open Questions
- Should DASH URL be included as a fourth field, or is HLS + WHEP sufficient?
- Should there be a `com.livepeer.gateway` registry of known gateways for domain validation?

## References, Sources & Evidence
- [Reference] ENS Text Records (ENSIP-5): https://docs.ens.domains/ensip/5
- [Reference] ENS Offchain Resolution (ENSIP-16): https://docs.ens.domains/ensip/16
- [Source] Foghorn README: `api_balancing/README.md`
- [Source] Stream data model: `pkg/models/streams.go`
- [Source] Playback resolution: `api_balancing/internal/control/playback.go`
