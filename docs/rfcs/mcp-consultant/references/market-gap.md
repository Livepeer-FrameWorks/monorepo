# Market Gap: Cross-Pipeline Video Streaming Diagnosis

Research synthesized Feb 2026.

## The Gap Nobody Fills

No product found — from any vendor — that takes a viewer-reported symptom and
automatically traces it through the full streaming pipeline to identify root cause:

```
Viewer symptom ("buffering in Germany on 1080p")
    ↓
Ingest analysis (encoder settings, bitrate, protocol, packet loss)
    ↓
CDN analysis (routing decisions, edge health, peering congestion)
    ↓
Player analysis (buffer state, ABR behavior, device capabilities)
    ↓
Root cause + recommendation
```

### What Exists Today (Partial Coverage)

| Vendor              | Coverage                           | Gap                                   |
| ------------------- | ---------------------------------- | ------------------------------------- |
| **NPAW NaLa**       | QoE anomalies + NL interface       | No ingest/encoder diagnosis           |
| **Conviva Nexa**    | Viewer-side analytics + NL queries | No CDN routing or encoder analysis    |
| **Touchstream**     | Full pipeline monitoring           | ML-based, no conversational interface |
| **Mux Data**        | Deep QoE metrics + CMCD            | Dashboard-only, no AI assistant       |
| **Datadog Bits AI** | General infra investigation        | No video streaming domain expertise   |

### Why This Gap Exists

1. **Fragmented data ownership**: Encoder metrics, CDN metrics, and player metrics
   typically live in different systems owned by different teams/vendors.
2. **Domain expertise required**: Correlating "GOP interval of 8s" with "high latency
   for interactive streams" requires deep video streaming knowledge that general
   observability tools lack.
3. **Cross-system tool chaining**: An LLM needs to call multiple diagnostic tools
   sequentially, reason about intermediate results, and form hypotheses. This requires
   an agent pattern, not just a dashboard.

### Why Skipper Can Fill It

FrameWorks controls the full pipeline — ingest, CDN routing, and player telemetry
are all accessible via existing MCP tools:

| Pipeline Stage   | Existing MCP Tool                                | Data Source                 |
| ---------------- | ------------------------------------------------ | --------------------------- |
| Ingest/encoder   | `diagnose_packet_loss`                           | Periscope (ClickHouse)      |
| CDN routing      | `diagnose_routing`                               | Periscope (routing events)  |
| Player QoE       | `diagnose_rebuffering`, `diagnose_buffer_health` | Periscope (QoE metrics)     |
| Anomaly overview | `get_anomaly_report`                             | Multiple aggregated queries |

Because FrameWorks owns all the data sources, Skipper can chain these tools in a single
investigation — something no external vendor can do without deep integration.

## Competitive Positioning

### vs NPAW NaLa

- NaLa is viewer-side focused. Skipper covers the full pipeline.
- NaLa requires separate setup. Skipper is built into the platform.
- NaLa's knowledge base is generic. Skipper has direct API access to tenant data.

### vs Conviva Nexa

- Nexa is analytics-focused (querying dashboards via NL). Skipper is diagnostic-focused
  (investigating and explaining root causes).
- Conviva is a third-party analytics overlay. Skipper is native to the platform.

### vs General AIOps (Datadog, New Relic)

- General AIOps tools don't understand video streaming semantics (GOP, ABR, codec
  compatibility, keyframe intervals).
- Skipper's system prompt encodes deep domain expertise.
- Skipper's tools are purpose-built for streaming diagnostics.

## The Differentiation Statement

> Skipper is the first AI video consultant that can trace a viewer's quality issue
> through the entire streaming pipeline — from encoder to CDN to player — and explain
> the root cause in plain language. Unlike external analytics overlays, Skipper has
> native access to all platform telemetry and can investigate proactively via its
> smart heartbeat agent.
