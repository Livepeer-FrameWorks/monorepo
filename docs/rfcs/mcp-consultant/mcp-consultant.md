# RFC: MCP Video Streaming Consultant (Skipper)

## Status

Implemented (Phase 1) | Phase 2 in design

## TL;DR

- Phase 1 (implemented): MCP tools/resources/prompts so customer-side LLMs can diagnose streaming issues (BYO LLM).
- Phase 2 (next): **Skipper** (`api_consultant`) MVP — chat + orchestration (**2A**) and grounding (**2B**: RAG + web search + confidence/citations). Premium, metered feature.
- Phase 3 (deferred): Productization and expansion (**3A** heartbeat/investigations, **3B** metering/billing/tier gating, **3C** extra surfaces like docs-embedded chat + API/SDK polish).
- Research: `./references/` contains industry analysis backing these decisions.

## Current State (as of 2026-02-05)

- MCP server exists with diagnostic tools, knowledge resources, support history, and expert prompts.
- Customers with MCP-capable clients (Claude Desktop, Cursor, etc.) can diagnose streaming issues via their own LLM.
- No server-side LLM. Customers without MCP clients cannot access AI diagnostics.
- No proactive monitoring or automated investigation.

## Problem / Motivation

### Customer Pain Points

1. **Debugging is hard**: Customers struggle to diagnose stream quality issues without deep video expertise
2. **Domain knowledge gap**: Video streaming has many non-obvious gotchas (keyframe intervals, codec compatibility, ABR tuning)
3. **Support latency**: Human support takes time; customers want instant answers
4. **Reactive only**: We notify customers of issues only after they complain
5. **MCP client required**: Only technically sophisticated customers who set up an MCP client can access AI diagnostics

### Opportunity

No streaming company offers cross-pipeline diagnosis (encoder → CDN → player) in a single conversational interface. NPAW NaLa and Conviva Nexa cover partial pipeline segments. FrameWorks controls the full pipeline — ingest, CDN routing, and player telemetry are all accessible via existing MCP tools. See `./references/market-gap.md`.

## Goals

- Direct debugging of streams (QoE metrics, diagnoses)
- Domain expertise (codecs, keyframes, ABR, latency optimization)
- Support history context (past conversations for continuity)
- Proactive warnings (e.g., GOP causing high latency)
- **AI diagnostics accessible to all customers** (not just MCP-equipped ones)
- **Automated investigation** when metrics degrade

## Non-Goals

- Replacing human support workflows
- Autonomous remediation (CDN switching, encoder changes) without human approval
- Running a 24/7 agent loop (heartbeat is periodic and context-aware)

## Proposal

### Architecture Overview

```
Phase 1 (Implemented):

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
│    knowledge://sources                                      │
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
└─────────────────────────┬───────────────────────────────────┘
                          │
          ┌───────────────┼───────────────┐
          ▼               ▼               ▼
    ┌───────────┐   ┌───────────┐   ┌───────────┐
    │ Periscope │   │ Deckhand  │   │ Commodore │
    │ (QoE data)│   │ (support) │   │ (streams) │
    └───────────┘   └───────────┘   └───────────┘


Phase 2 (Skipper):

┌──────────────────────────┐    ┌──────────────────────────┐
│  Dashboard Chat Widget   │    │  Docs-Embedded Chat      │
│  (authenticated, tenant) │    │  (unauthenticated,       │
│                          │    │   knowledge only)         │
└────────────┬─────────────┘    └────────────┬─────────────┘
             │                               │
             ▼                               ▼
┌─────────────────────────────────────────────────────────────┐
│  Skipper (api_consultant/)                                     │
│                                                             │
│  ┌─────────────┐  ┌──────────────┐  ┌───────────────────┐  │
│  │ Chat Handler │  │  Heartbeat   │  │  Metering/Billing │  │
│  │ (HTTP + SSE) │  │  Agent       │  │  (Kafka → Purser) │  │
│  └──────┬──────┘  └──────┬───────┘  └───────────────────┘  │
│         │                │                                  │
│         ▼                ▼                                  │
│  ┌─────────────────────────────┐                            │
│  │  Tool Orchestrator          │                            │
│  │  (chains MCP diagnostic     │                            │
│  │   tools via pkg/llm/)       │                            │
│  └──────────────┬──────────────┘                            │
│                 │                                           │
│  ┌──────────────▼──────────────┐                            │
│  │  pkg/llm/ Provider          │                            │
│  │  ollama | openrouter |      │                            │
│  │  anthropic | openai         │                            │
│  └──────────────┬──────────────┘                            │
└─────────────────┼───────────────────────────────────────────┘
                  │ gRPC
    ┌─────────────┼──────────────┬──────────────┐
    ▼             ▼              ▼              ▼
┌────────┐  ┌─────────┐  ┌──────────┐  ┌─────────────┐
│Periscope│  │Deckhand │  │Commodore │  │Lookout (opt)│
│(QoE)   │  │(support)│  │(streams) │  │(incidents)  │
└────────┘  └─────────┘  └──────────┘  └─────────────┘
```

### Phased Approach

| Phase        | Scope                                                                                 | Effort  | Dependencies         |
| ------------ | ------------------------------------------------------------------------------------- | ------- | -------------------- |
| **Phase 1**  | MCP consultant foundation (tools/resources/prompts; BYO LLM)                          | ~1 week | None                 |
| **Phase 2A** | Skipper chat + orchestration (api_consultant HTTP+SSE, persistence, dashboard widget) | TBD     | LLM API key          |
| **Phase 2B** | Grounding layer (pgvector RAG + web search + confidence/citations)                    | TBD     | LLM + search API key |
| **Phase 3A** | Smart heartbeat agent + investigations + notifications                                | TBD     | Phase 2              |
| **Phase 3B** | Metering/billing + tier gating + per-tenant rate limits                               | TBD     | Phase 2              |
| **Phase 3C** | Extra surfaces (docs-embedded chat + API/SDK polish)                                  | TBD     | Phase 2              |

---

## Phase 1: Knowledge + Tools + Support History (Implemented)

### MCP Resources

**Knowledge Sources**

| URI                   | Description                                               |
| --------------------- | --------------------------------------------------------- |
| `knowledge://sources` | Curated list of documentation sites with sitemaps/indexes |

Returns JSON with authoritative doc site entry points. The customer's LLM navigates to relevant pages itself.

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

Implementation: Calls existing Deckhand gRPC client.

### MCP Tools

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

**Support Tools**

| Tool                     | Purpose                              |
| ------------------------ | ------------------------------------ |
| `search_support_history` | Search past conversations by keyword |

### MCP Prompts

| Prompt                   | Purpose                                        | Arguments              |
| ------------------------ | ---------------------------------------------- | ---------------------- |
| `video_consultant`       | Full expert persona with capabilities overview | None                   |
| `diagnose_quality_issue` | Guided troubleshooting workflow                | `stream_id`, `symptom` |

### Phase 1 Implementation

```
api_gateway/internal/mcp/
├── server.go                     # Register new resources/tools
├── resources/
│   ├── knowledge.go              # Domain knowledge
│   ├── support.go                # Support history
│   └── ...
├── tools/
│   ├── qoe.go                    # Diagnostic tools
│   ├── support.go                # Support tools
│   └── ...
└── prompts/
    └── prompts.go                # Consultant prompts
```

---

## Phase 2: Skipper — AI Video Consultant

### Summary

**Skipper** is a server-side AI video consultant that makes diagnostic capabilities
accessible to all customers — not just those with MCP clients. It provides:

1. **Three-tier knowledge architecture** — RAG KB + live web search + LLM best guess (with confidence tagging)
2. **Chat interface** — In-app widget, docs-embedded widget, and API endpoint
3. **Smart heartbeat agent** — Periodic context-aware monitoring (OpenClaw pattern)
4. **Provider-agnostic backends** — LLM and search providers configurable by operator

Premium, metered feature billed per tenant via Purser.

See `./references/` for industry research backing these decisions.

### Three-Tier Knowledge Architecture

The core anti-hallucination design. Every answer is sourced and tagged with confidence.

```
User asks question
    ↓
1. search_knowledge (RAG — embedded KB)
    → Good results? → Answer, tag confidence: "verified", cite source
    → Insufficient? ↓
2. search_web (Brave / Tavily / SearXNG)
    → Good results? → Answer, tag confidence: "sourced", cite source
    → Insufficient? ↓
3. LLM training knowledge
    → Can answer? → Answer, tag confidence: "best_guess", warn user
    → Can't? ↓
4. "I don't have enough information to help with this."
    Tag confidence: "unknown", suggest where to look
```

**Confidence enum** — every response block is tagged so the frontend can adjust styling:

| Level        | Source                 | Frontend treatment                                           |
| ------------ | ---------------------- | ------------------------------------------------------------ |
| `verified`   | Embedded KB match      | Full confidence, cited, normal styling                       |
| `sourced`    | Live web search result | Cited with external link badge                               |
| `best_guess` | LLM training knowledge | Dimmed/warning — "Not from verified sources. Please verify." |
| `unknown`    | Can't answer           | "I don't have enough information."                           |

A single response can mix confidence levels per paragraph/section.

### 2A: Provider Libraries

**`pkg/llm/`** — Provider-agnostic LLM backend

```
pkg/llm/
├── provider.go         # Interface: Complete(messages, tools) → response
├── openai.go           # OpenAI-compatible (covers OpenRouter, OpenAI, many others)
├── anthropic.go        # Claude API
├── ollama.go           # Self-hosted ollama
└── config.go           # Env-based config
```

```
LLM_PROVIDER=openai             # openai | anthropic | ollama
LLM_MODEL=gpt-4o                # model name/ID
LLM_API_KEY=sk-...              # API key
LLM_API_URL=                    # custom base URL (OpenRouter, ollama, etc.)
```

**`pkg/search/`** — Provider-agnostic web search

```
pkg/search/
├── provider.go         # Interface: Search(query) → []Result
├── tavily.go           # Tavily API (AI-optimized, clean content extraction)
├── brave.go            # Brave Search API
├── searxng.go          # Self-hosted SearXNG (free, no per-query cost)
└── config.go           # Env-based config
```

```
SEARCH_PROVIDER=tavily          # tavily | brave | searxng
SEARCH_API_KEY=                 # required for tavily/brave
SEARCH_API_URL=                 # required for searxng (self-hosted URL)
```

### 2B: Knowledge Base (pgvector RAG)

pgvector in PostgreSQL. Fast retrieval (~50ms). Pre-indexed.

**Sources to crawl/embed:**

| Source                                   | Crawlable?                    | Method                        |
| ---------------------------------------- | ----------------------------- | ----------------------------- |
| FrameWorks docs (docs.framework.network) | Yes                           | Sitemap crawl                 |
| MistServer docs (docs.mistserver.org)    | Yes (Docusaurus, ~1000 pages) | Sitemap crawl                 |
| FFmpeg man pages (ffmpeg.org)            | Yes (10s crawl delay)         | Direct fetch                  |
| FFmpeg GitHub docs                       | Yes                           | Git clone                     |
| FrameWorks blog/encoding guides          | Yes (you write these)         | Sitemap crawl                 |
| Human-curated pages (FFmpeg wiki etc.)   | Blocked for bots              | Human retrieves via admin API |
| Operator-uploaded custom docs            | N/A                           | Upload API                    |

Note: FFmpeg wiki (trac.ffmpeg.org) blocks AI crawlers. Web search fallback covers
this gap — search engines have already indexed the wiki. Human curation of top 20-30
guides seeds the KB for faster retrieval of high-value content.

### 2C: Chat Interface

**`api_consultant/`** service exposing HTTP API. Three surfaces:

| Surface                            | Auth                         | Capabilities                               |
| ---------------------------------- | ---------------------------- | ------------------------------------------ |
| **In-app chat** (Svelte dashboard) | Authenticated, tenant-scoped | Full diagnostics + KB + web search         |
| **Docs-embedded chat**             | Unauthenticated              | KB + web search only (no diagnostic tools) |
| **API endpoint**                   | Authenticated, tenant-scoped | Full diagnostics + KB + web search         |

Chat flow:

1. Receive customer message (authenticated, tenant-scoped)
2. System prompt: video streaming expert persona + grounding rules + confidence tagging
3. LLM decides: diagnostic tools (tenant data), search_knowledge (KB), or search_web
4. Chain tools as needed, cite sources, tag confidence per section
5. Stream response tokens via SSE (real-time typing effect)
6. Collapsible details showing tool calls and raw data underneath final answer
7. Log token usage for metering
8. Store conversation in PostgreSQL

**Conversation memory**: Store chat history in PostgreSQL. Recency-based retrieval.

### 2D: Smart Heartbeat Agent

Context-aware periodic monitoring (OpenClaw pattern) — not a dumb poll.

```
every N minutes (configurable, default 30):
  1. Query platform: which tenants (with Skipper enabled) have active streams?
  2. Fetch health summaries via existing diagnostic tools
  3. LLM reviews context → decide: investigate, flag, or skip
  4. If investigate → chain tools → produce root cause report
  5. Deliver report via notification (email, MCP SSE, dashboard)
  6. Log token usage for billing
```

Also triggered by:

- Threshold alerts (metrics in "yellow range")
- Lookout incident events (when Lookout ships — soft dependency, not a blocker)

Most heartbeats should be silent (`HEARTBEAT_OK`).

### 2E: Billing / Metering

- **Gating**: Tenant subscription tier determines access
- **Metering**: Per-tenant tracking of LLM calls + tokens + search queries
- **Billing**: Usage reported to Purser via Kafka billing event pipeline
- **Rate limiting**: Per-tenant rate limits on chat messages and investigation triggers
- **Operator config**: Operators set tier access + provide their own LLM/search API keys

### Phase 2 Effort Estimate

| Component                                         | Effort           |
| ------------------------------------------------- | ---------------- |
| `pkg/llm/` (openai + anthropic + ollama)          | ~1 week          |
| `pkg/search/` (tavily + brave + searxng)          | ~3-4 days        |
| pgvector + embedding pipeline                     | ~1 week          |
| Crawl pipeline (sitemap + re-crawl)               | ~1 week          |
| `search_knowledge` + `search_web` tools           | ~3-4 days        |
| `api_consultant/` chat + tool orchestration + SSE | ~2 weeks         |
| Smart heartbeat + threshold triggers              | ~1.5 weeks       |
| Billing/metering integration                      | ~1 week          |
| Admin API (human curation + operator uploads)     | ~3-4 days        |
| Dashboard chat widget (Svelte)                    | ~1 week          |
| Docs-embedded chat widget                         | ~3-4 days        |
| Conversation persistence                          | ~2-3 days        |
| Notification delivery (email + MCP SSE)           | ~3-4 days        |
| System prompt + grounding rules                   | ~2 days          |
| Testing + integration                             | ~1 week          |
| **Total**                                         | **~11-13 weeks** |

### Phase 2 Infrastructure

- `api_consultant/` — New Go service
- pgvector extension in PostgreSQL
- ollama container in docker-compose.yml (optional, for self-hosted LLM)
- SearXNG container in docker-compose.yml (optional, for self-hosted search)
- LLM API key (required for MVP)
- Search API key (required for MVP unless self-hosting SearXNG)

---

## Phase 3: Enhancements (Deferred)

- Conversation embeddings for semantic retrieval of past sessions
- Self-hosted SearXNG + ollama for fully self-contained deployment
- FrameWorks forum integration (when forum has content)
- Auto-remediation suggestions with human approval gates
- Multi-language support

---

## Impact / Dependencies

- Services: api_gateway (MCP), Periscope, Deckhand, Commodore, Purser (billing)
- New service: `api_consultant/`
- New libraries: `pkg/llm/`, `pkg/search/`
- pgvector extension in PostgreSQL
- Soft dependency: Lookout (incidents) — Skipper works without it, integrates when available
- Existing:
  - `pkg/clients/periscope/grpc_client.go` - QoE data access
  - `pkg/clients/deckhand/grpc_client.go` - Support history
  - `pkg/config/env.go` - Config pattern for LLM provider selection

## Alternatives Considered

- Status quo: human support only (slow, expensive)
- Build server-side LLM first before shipping any MCP tools (Phase 1 validates demand first)
- Build RAG pipeline before chat interface (RAG is premature without a consumer)
- 24/7 autonomous agent loop (expensive, unproven — heartbeat is cheaper and proven by OpenClaw pattern)
- Customer-side LLM only (excludes customers without MCP clients — see `./references/mcp-production-patterns.md`)

## Risks & Mitigations

- Risk: inaccurate guidance if QoE data is incomplete. Mitigation: return raw metrics + explain confidence. Collapsible tool call details let users verify.
- Risk: customers expect full support replacement. Mitigation: position as diagnostic assistant, not support replacement.
- Risk: LLM token costs at scale. Mitigation: metered billing per tenant, rate limiting, operator-provided API keys.
- Risk: verification burden (industry data shows humans validate 69% of AI decisions). Mitigation: show evidence alongside recommendations, don't auto-remediate.

## Success Metrics

| Metric                   | Target                | Measurement                           |
| ------------------------ | --------------------- | ------------------------------------- |
| MCP consultant adoption  | 20% of active tenants | Track `video_consultant` prompt usage |
| Diagnostic tool usage    | 50+ calls/day         | Track `diagnose_*` tool invocations   |
| Skipper chat adoption    | 30% of active tenants | Track chat sessions per tenant        |
| Support ticket reduction | 15% decrease          | Compare pre/post ticket volume        |
| Investigation accuracy   | 80%+ useful           | Tenant feedback on heartbeat reports  |

## Open Questions

1. ~~**Deckhand client**: Does `pkg/clients/deckhand/` exist?~~ **Answer**: Yes.
2. ~~**Knowledge content**: Static resources or RAG?~~ **Answer**: Phase 1 curated directory. Phase 2 three-tier: RAG KB + web search + LLM best guess.
3. ~~**Hallucination risk**: How to prevent LLM from guessing?~~ **Answer**: Confidence enum (`verified`/`sourced`/`best_guess`/`unknown`). Frontend styles by trust level. Web search covers blocked sources (FFmpeg wiki).
4. **Model selection**: Which model for default? Needs benchmarking for tool-use quality.
5. **Chat UI design**: Full page vs sidebar widget vs floating bubble — needs design input.
6. **Heartbeat frequency**: Tenant-configurable or operator-fixed?
7. **Notification channels**: Email + MCP SSE baseline. Webhooks? Slack?
8. **Embedding model**: OpenAI ada-002? Local model via ollama?

## References, Sources & Evidence

- [Source] https://modelcontextprotocol.io/specification/2025-11-25
- [Source] https://modelcontextprotocol.io/specification/2025-06-18/server/prompts
- [Evidence] ../api_gateway/internal/mcp/
- [Evidence] ../../architecture/analytics-pipeline.md
- [Research] ./references/industry-aiops.md
- [Research] ./references/mcp-production-patterns.md
- [Research] ./references/video-streaming-ai.md
- [Research] ./references/qoe-and-remediation.md
- [Research] ./references/openclaw-heartbeat.md
- [Research] ./references/market-gap.md
- [Related RFC] ../lookout.md — Incident/alert service, soft dependency for Skipper triggers
