# RFC: MCP Video Streaming Consultant

## Status

Implemented (Phase 1)

## TL;DR

- Provide MCP tools/resources/prompts so customer-side LLMs can diagnose streaming issues.
- Phase 1 is implemented: knowledge sources + QoE tools + support history + prompts.
- Phases 2/3 (monitoring + RAG, server-side agent) are deferred.

## Current State (as of 2026-01-13)

- MCP server exists; tools for stream management, billing, and playback resolution.
- Customers lack AI-assisted debugging capabilities; support is reactive and slow.
- The LLM runs on the customer side; our MCP server only exposes tools/resources.

## Problem / Motivation

### Customer Pain Points

1. **Debugging is hard**: Customers struggle to diagnose stream quality issues without deep video expertise
2. **Domain knowledge gap**: Video streaming has many non-obvious gotchas (keyframe intervals, codec compatibility, ABR tuning)
3. **Support latency**: Human support takes time; customers want instant answers
4. **Reactive only**: We notify customers of issues only after they complain

### Opportunity

MCP enables AI agents to use our platform programmatically. By exposing debugging tools and domain knowledge, we can offer an AI consultant as a differentiated feature - customers get expert-level guidance instantly.

## Goals

- Direct debugging of streams (QoE metrics, diagnoses)
- Domain expertise (codecs, keyframes, ABR, latency optimization)
- Support history context (past conversations for continuity)
- Proactive warnings (e.g., GOP causing high latency)

## Non-Goals

- Running a server-side LLM in Phase 1
- Full RAG pipeline in Phase 1
- Replacing human support workflows

## Proposal

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│  Customer's AI Client (Claude Desktop / ollama / custom)    │
│  - Reasons about problems                                   │
│  - Makes decisions on what tools to use                     │
└─────────────────────────┬───────────────────────────────────┘
                          │ MCP Protocol (HTTP + SSE)
                          ▼
┌─────────────────────────────────────────────────────────────┐
│  FrameWorks MCP Server (api_gateway/internal/mcp/)          │
│                                                             │
│  Resources:                                                 │
│    knowledge://sources                                     │
│    support://conversations, support://conversations/{id}    │
│                                                             │
│  Tools:                                                     │
│    diagnose_rebuffering, diagnose_packet_loss,              │
│    diagnose_buffer_health, diagnose_routing,                │
│    search_support_history                                   │
│                                                             │
│  Prompts:                                                   │
│    video_consultant, diagnose_quality_issue                 │
│                                                             │
│  Notifications (SSE):                                       │
│    qoe/anomaly - pushed when metrics degrade                │
└─────────────────────────┬───────────────────────────────────┘
                          │
          ┌───────────────┼───────────────┐
          ▼               ▼               ▼
    ┌───────────┐   ┌───────────┐   ┌───────────┐
    │ Periscope │   │ Deckhand  │   │ Commodore │
    │ (QoE data)│   │ (support) │   │ (streams) │
    └───────────┘   └───────────┘   └───────────┘
```

### Phased Approach

| Phase       | Scope                                             | Effort    | Dependencies          |
| ----------- | ------------------------------------------------- | --------- | --------------------- |
| **Phase 1** | Knowledge resources + QoE tools + support history | ~1 week   | None                  |
| Phase 2     | Background monitor + RAG knowledge base           | ~10 weeks | pgvector, new service |
| Phase 3     | Server-side AI agent (ollama)                     | ~7 weeks  | GPU infrastructure    |

**This RFC focuses on Phase 1.** Phases 2 and 3 are documented for context but deferred.

### Phase 1: Knowledge + Tools + Support History

#### New MCP Resources

**Knowledge Sources**

| URI                   | Description                                               |
| --------------------- | --------------------------------------------------------- |
| `knowledge://sources` | Curated list of documentation sites with sitemaps/indexes |

**Implementation**: Returns JSON with authoritative doc site entry points. The LLM navigates to relevant pages itself.

```json
{
  "sources": [
    {
      "name": "FrameWorks Docs",
      "description": "Platform documentation for streamers, operators, and hybrid setups",
      "index": "https://docs.framework.network/",
      "sitemap": "https://docs.framework.network/sitemap.xml"
    },
    {
      "name": "MistServer Docs",
      "description": "MistServer configuration, protocols, and API reference",
      "index": "https://docs.mistserver.org/",
      "sitemap": "https://mistserver.org/sitemap.xml"
    },
    {
      "name": "FFmpeg Wiki",
      "description": "Encoding guides for H.264, HEVC, VP9, AV1, hardware acceleration",
      "index": "https://trac.ffmpeg.org/wiki/TitleIndex"
    },
    {
      "name": "OBS Wiki",
      "description": "OBS Studio setup, streaming configuration, troubleshooting",
      "index": "https://obsproject.com/wiki/"
    }
  ]
}
```

**Support History Resources**

| URI                            | Description                            |
| ------------------------------ | -------------------------------------- |
| `support://conversations`      | List of tenant's support conversations |
| `support://conversations/{id}` | Full conversation with messages        |

**Implementation**: Calls existing Deckhand gRPC client.

#### New MCP Tools

**QoE Diagnostic Tools**

| Tool                     | Purpose                                       | Data Source                                    |
| ------------------------ | --------------------------------------------- | ---------------------------------------------- |
| `diagnose_rebuffering`   | Analyze rebuffer events for a stream          | `GetRebufferingEvents`, `GetStreamHealth5m`    |
| `diagnose_packet_loss`   | Analyze packet loss patterns (protocol-aware) | `GetClientMetrics5m`, `GetStreamHealthMetrics` |
| `diagnose_buffer_health` | Trace buffer state transitions                | `GetBufferEvents`                              |
| `diagnose_routing`       | Analyze CDN routing decisions                 | `GetRoutingEvents`                             |
| `get_anomaly_report`     | Detect statistical anomalies                  | Multiple aggregated queries                    |

**Tool Response Pattern**:

```json
{
  "status": "warning",
  "metrics": {
    "rebuffer_count": 47,
    "avg_rebuffer_duration_ms": 2340,
    "time_range": "last_1h"
  },
  "analysis": "Elevated rebuffering detected. 47 events in 1h is 3x above your 7-day average.",
  "recommendations": [
    "Check encoder output bitrate - currently 6.2 Mbps, consider reducing to 4.5 Mbps",
    "Verify stable upload connection - packet loss at 2.3% (threshold: 0.5%)"
  ]
}
```

**Status Enum**: `healthy | warning | critical | no_data`

**Gap (Phase 1)**: `related_resources` was removed from responses. If we want inline knowledge guidance without full RAG, add a curated MCP knowledge pack and reintroduce this field later.

**Support Tools**

| Tool                     | Purpose                              |
| ------------------------ | ------------------------------------ |
| `search_support_history` | Search past conversations by keyword |

#### New MCP Prompts

| Prompt                   | Purpose                                        | Arguments              |
| ------------------------ | ---------------------------------------------- | ---------------------- |
| `video_consultant`       | Full expert persona with capabilities overview | None                   |
| `diagnose_quality_issue` | Guided troubleshooting workflow                | `stream_id`, `symptom` |

**video_consultant prompt** establishes the AI as a video streaming expert and explains available tools/resources.

### Implementation Details

#### File Structure

```
api_gateway/internal/mcp/
├── server.go                     # MODIFY - Register new resources/tools
├── resources/
│   ├── account.go               # existing
│   ├── analytics.go             # existing
│   ├── billing.go               # existing
│   ├── knowledge.go             # NEW - Domain knowledge
│   ├── support.go               # NEW - Support history
│   └── ...
├── tools/
│   ├── account.go               # existing
│   ├── qoe.go                   # NEW - Diagnostic tools
│   ├── support.go               # NEW - Support tools
│   └── ...
└── prompts/
    └── prompts.go               # MODIFY - Add consultant prompts
```

#### Effort Estimate

| Component                  | Effort      | Notes                                              |
| -------------------------- | ----------- | -------------------------------------------------- |
| Knowledge sources resource | 0.5 days    | Single resource returning curated doc site indexes |
| Support resources          | 1 day       | Wire to Deckhand client                            |
| QoE diagnostic tools (5)   | 2 days      | Query Periscope, format responses                  |
| Support tools              | 0.5 days    | Search/filter conversations                        |
| Prompts                    | 0.5 days    | Text content                                       |
| Integration + testing      | 0.5 days    | Wire into server.go                                |
| **Total**                  | **~5 days** |                                                    |

## Impact / Dependencies

- Services: api_gateway (MCP), api_analytics_query (Periscope), api_ticketing (Deckhand)
- Existing:
  - `pkg/clients/periscope/grpc_client.go` - QoE data access
  - `pkg/clients/deckhand/grpc_client.go` - Support history
- Phase 1 new dependencies: none
- Phases 2/3 will require new services and infrastructure (see Migration / Rollout)

## Alternatives Considered

- Status quo: human support only (slow, expensive)
- Build server-side LLM first (Phase 3) before shipping any tools
- Build RAG pipeline before shipping Phase 1 tools

## Risks & Mitigations

- Risk: inaccurate guidance if QoE data is incomplete. Mitigation: return raw metrics + explain confidence.
- Risk: customers expect full support replacement. Mitigation: position as diagnostic assistant, not support replacement.

## Migration / Rollout

### Phase 1 (Implemented)

- Knowledge sources + QoE tools + support history + prompts

### Phase 2 (Deferred)

**Concept**: Background monitor service with RAG knowledge base.

**Key additions**:

- `api_lookout/` - New Go service polling ClickHouse
- pgvector in PostgreSQL for knowledge embeddings
- `search_knowledge` MCP tool with semantic search
- Crawled content: platform docs, FFmpeg wiki, OBS guides, forums

**Why deferred**: Requires new infrastructure (pgvector), content curation pipeline, embedding model decision.

### Phase 3 (Deferred)

**Concept**: Server-side LLM (ollama with qwen2.5) that proactively diagnoses issues.

**Key additions**:

- `api_consultant/` - New service with ollama
- Server-side agent loop (detect -> diagnose -> recommend)
- AI-generated insights (not template-based)

**Why deferred**: Requires GPU infrastructure, significant complexity, unclear value over Phase 1 until validated.

## Success Metrics

| Metric                   | Target                | Measurement                           |
| ------------------------ | --------------------- | ------------------------------------- |
| MCP consultant adoption  | 20% of active tenants | Track `video_consultant` prompt usage |
| Diagnostic tool usage    | 50+ calls/day         | Track `diagnose_*` tool invocations   |
| Support ticket reduction | 15% decrease          | Compare pre/post ticket volume        |
| Customer satisfaction    | Positive feedback     | Qualitative from beta users           |

## Open Questions

1. **Deckhand client**: Does `pkg/clients/deckhand/` exist with `ListConversations`, `GetConversation` methods?
   - **Answer**: Yes, the client exists with these methods.
2. **Knowledge content**: Should we include external sources (FFmpeg wiki) in Phase 1 as static resources, or defer all external content to Phase 2 RAG?
   - **Answer**: Phase 1 uses a curated directory approach - `knowledge://sources` returns doc site sitemaps/indexes, and the customer's LLM navigates them directly. RAG with semantic search is deferred to Phase 2.

## References, Sources & Evidence

- [Source] https://modelcontextprotocol.io/specification/2025-11-25
- [Source] https://modelcontextprotocol.io/specification/2025-06-18/server/prompts
- [Evidence] ../api_gateway/internal/mcp/
- [Evidence] ./analytics-pipeline.md
- [Evidence] ../architecture/deckhand.md
