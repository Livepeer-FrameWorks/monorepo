# RFC: Network-Level Security Capabilities

## Status

Draft

## TL;DR

- Add phased network-level security capabilities: connection metadata logging, TLS fingerprinting, DNS query logging, and anomaly detection.
- These techniques are only practical on sovereign infrastructure where operators control TLS termination and DNS resolution.
- Complements existing rate limiting; does not replace it.

## Current State

Rate limiting exists in `api_gateway/internal/middleware/ratelimit.go` as a token bucket per tenant. The Gateway also has authentication, public-operation allowlists, x402 settlement, and prepaid/suspension checks, but those are application/account gates rather than network-level security telemetry.

GeoIP (`pkg/geoip/geoip.go`) is used for viewer routing decisions, not for blocking or reputation scoring.

There is no JA3/JA4 TLS fingerprinting, no bot detection, no IP reputation services, and no connection-level security telemetry. No DNS query logging exists — Navigator (`api_dns/`) uses the Cloudflare API for DNS management with no self-hosted DNS infrastructure. No WebRTC connection forensics are available (no platform-managed TURN/STUN; see the nat-traversal RFC).

Evidence:

- `api_gateway/internal/middleware/ratelimit.go`
- `api_gateway/internal/middleware/auth_request.go`
- `pkg/geoip/geoip.go`
- `api_dns/`

## Problem / Motivation

As FrameWorks operators handle more traffic, they face security threats that rate limiting alone cannot address: stream key theft, unauthorized restreaming, bot-driven viewer inflation, and DDoS attacks. These threats require inspection at the network and connection level.

On managed cloud platforms, operators do not control TLS termination or DNS resolution, making these techniques infeasible. Sovereign infrastructure — where FrameWorks operators own their edge nodes and network stack — makes them possible.

## Goals

- Log connection metadata (source IP, TLS version, cipher suite, SNI, connection duration) for forensic analysis.
- Enable TLS fingerprinting (JA3/JA4) for client classification and bot detection.
- Provide DNS query logging when operators run self-hosted DNS.
- Build an anomaly detection pipeline over aggregated connection data.

## Non-Goals

- Real-time DDoS mitigation. Defer to upstream transit providers and network-level filtering.
- Building a full WAF. Application-layer request inspection is a separate concern.
- Replacing rate limiting. These capabilities complement it.
- Implementing all phases immediately. Each phase is independently valuable.

## Proposal

### Phase 1: Connection metadata logging

Log source IP, TLS version, cipher suite, SNI, and connection duration on edge nodes. Store in ClickHouse via the existing analytics pipeline (Kafka topic, Periscope Ingest consumer). Implementation: enrich Caddy access logs and add a Periscope Ingest consumer for the new event type.

### Phase 2: JA3/JA4 TLS fingerprinting

Capture TLS ClientHello fingerprints at the TLS termination point (Caddy or MistServer). The fingerprint identifies client software: a specific browser version, a bot framework, curl, a known streaming tool, etc. Store fingerprints alongside connection metadata in ClickHouse.

Enables detection patterns such as: "this stream key is being used from 3 different TLS fingerprints simultaneously" or "this fingerprint matches a known restreaming tool."

### Phase 3: DNS query logging

When Navigator moves to self-hosted DNS (PowerDNS — see the dns-anycast RFC), log all DNS queries. This creates a correlation layer: "this IP resolved our streaming domain at 14:02, then connected with a known bot fingerprint at 14:03." Tracking adversaries across DNS and connection layers provides stronger signal than either source alone.

### Phase 4: Anomaly detection pipeline

ClickHouse materialized views over connection, DNS, and TLS fingerprint data. Alert on:

- Unusual viewer geography for a given stream (sudden spike from an unexpected region).
- Stream key sharing (same key, multiple distinct TLS fingerprints or source IPs).
- Rapid reconnection patterns (connection churn indicative of scraping or probing).
- TLS fingerprint clustering (many connections from the same non-browser fingerprint).

## Impact / Dependencies

- **Phase 1-2:** Caddy configuration, Periscope Ingest (new consumer), ClickHouse schema (new table).
- **Phase 3:** Depends on the dns-anycast RFC. No work until self-hosted DNS is in place.
- **Phase 4:** Pure analytics layer over data collected in phases 1-3. No new infrastructure.
- Existing services are unaffected. All phases are additive.

## Alternatives Considered

- **Cloudflare Bot Management.** Vendor lock-in. Limited visibility into raw connection data. Not available on sovereign infrastructure without Cloudflare in the path.
- **Third-party SIEM (Splunk, Elastic, etc.).** Cost and data egress concerns. Operators on sovereign infrastructure may not want to export connection data to external services.
- **Application-layer-only detection.** Misses network-level signals. A sophisticated bot can mimic valid API requests but cannot easily forge a TLS fingerprint.

## Risks & Mitigations

- **Privacy implications of logging connection metadata.** Mitigation: apply the same H3 geo-bucketing used for viewer analytics. Comply with GDPR retention policies. Source IPs can be hashed or truncated based on operator configuration.
- **TLS fingerprinting can be evaded by sophisticated adversaries** (e.g., using a real browser via headless mode). Mitigation: fingerprinting is one signal among many. The anomaly detection pipeline correlates multiple signals for stronger detection.
- **False positives in anomaly detection.** Mitigation: alerts are informational, not enforcement. Operators review and act. Thresholds are configurable.
- **Connection logging impacts edge performance.** Mitigation: async log shipping via Kafka. Logging overhead is minimal compared to media delivery.

## Migration / Rollout

1. **Connection metadata logging.** Define ClickHouse schema, add Kafka topic, deploy Periscope Ingest consumer. No changes to existing services.
2. **TLS fingerprinting.** Caddy plugin or MistServer patch to extract ClientHello fingerprint. Append to connection metadata events.
3. **DNS query logging.** Blocked on dns-anycast RFC. When self-hosted DNS is deployed, add query logging to the DNS server configuration.
4. **Anomaly detection.** ClickHouse materialized views and alert rules. Can begin as soon as phase 1 data is flowing.

## Open Questions

- What ClickHouse table schema for connection metadata? Partition by day? By tenant?
- Should TLS fingerprinting be opt-in per operator, or enabled by default?
- How much does connection logging impact edge node performance under high concurrency?
- Should anomaly detection alerts route through Prometheus or through the platform's own notification system?

## References, Sources & Evidence

- [Evidence] `api_gateway/internal/middleware/ratelimit.go` (token bucket rate limiting)
- [Evidence] `pkg/geoip/geoip.go` (GeoIP for routing, not security)
- [Evidence] `api_dns/` (Cloudflare-based DNS, no self-hosted)
- [Reference] `docs/rfcs/dns-anycast.md` (prerequisite for Phase 3)
- [Reference] `docs/rfcs/nat-traversal.md` (related — WebRTC connection context)
- [Reference] JA3 TLS fingerprinting: https://github.com/salesforce/ja3
- [Reference] JA4 TLS fingerprinting: https://github.com/FoxIO-LLC/ja4
