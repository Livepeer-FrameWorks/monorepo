# Skipper Architecture

Skipper is the AI video consultant service. It provides RAG-grounded, tool-augmented chat for streaming troubleshooting and configuration guidance.

## Overview

- **Service:** `api_consultant/` (Go)
- **Ports:** 18018 (HTTP), 19007 (gRPC)
- **Database:** PostgreSQL with pgvector extension
- **RFC:** `docs/rfcs/mcp-consultant/mcp-consultant.md`

## Subsystems

### Chat Orchestrator

Handles `POST /api/skipper/chat`. Receives a user message, loads conversation history, calls an LLM with tool definitions, executes tool calls in a loop (max 5 rounds), and streams the response via SSE.

### Knowledge Base (RAG)

pgvector-backed vector store for documentation retrieval:

- **Crawler** — fetches sitemaps, extracts text from HTML pages
- **Embedder** — chunks documents (~500 tokens, 50 overlap), generates embeddings
- **Store** — cosine similarity search over `skipper_knowledge` table

### Tool System

LLM can invoke these tools during orchestration. Local tools run inside the Skipper process; Gateway tools are forwarded via MCP client with the caller's JWT.

**Local tools** (defined in Skipper spoke):

| Tool               | Source                             | Purpose                          |
| ------------------ | ---------------------------------- | -------------------------------- |
| `search_knowledge` | pgvector store                     | RAG retrieval from embedded docs |
| `search_web`       | pkg/search/ (Tavily/Brave/SearXNG) | Live web search                  |

**Gateway tools** (proxied via MCP client):

| Tool                        | Purpose                                              |
| --------------------------- | ---------------------------------------------------- |
| `execute_query`             | Run arbitrary GraphQL queries/mutations against API  |
| `introspect_schema`         | Discover available GraphQL types and fields          |
| `generate_query`            | Generate GraphQL from template catalog               |
| `diagnose_rebuffering`      | Rebuffering root-cause analysis                      |
| `diagnose_buffer_health`    | Buffer underrun diagnostics                          |
| `diagnose_packet_loss`      | Packet loss detection                                |
| `diagnose_routing`          | Viewer routing analysis                              |
| `get_stream_health_summary` | Overall stream health report                         |
| `get_anomaly_report`        | Anomaly detection across metrics                     |
| `create_stream`, etc.       | Full stream/clip/VOD/billing CRUD (see agent-access) |

The `execute_query` tool gives Skipper (and external MCP agents) full GraphQL access — listing streams, checking billing, fetching analytics — with authorization enforced by existing resolvers.

### Confidence Tagging

Every response section is tagged: `verified`, `sourced`, `best_guess`, or `unknown`. Sources are cited with URLs when available.

### Docs Mode

When `?mode=docs` is passed, Skipper restricts tool use to a read-only whitelist (knowledge search, schema introspection, stream reads, diagnostics). `execute_query` is blocked — docs-mode users can view generated queries via `generate_query` but cannot execute them. This powers the documentation site chat widget.

### Conversation Persistence

Multi-turn conversations stored in `skipper_conversations` / `skipper_messages` tables. All queries scoped by `tenant_id` and `user_id`.

## Dependencies

| Dependency            | Purpose                                              |
| --------------------- | ---------------------------------------------------- |
| PostgreSQL (pgvector) | Vector store, conversations, usage tracking          |
| pkg/llm/              | LLM provider abstraction (OpenAI, Anthropic, Ollama) |
| pkg/search/           | Web search provider abstraction                      |
| Periscope (gRPC)      | Stream diagnostics                                   |
| Commodore (gRPC)      | Tenant/stream context                                |
| Deckhand (gRPC)       | Support ticket context                               |

## Environment Variables

| Variable                            | Purpose                                                                      | Default              |
| ----------------------------------- | ---------------------------------------------------------------------------- | -------------------- |
| `LLM_PROVIDER`                      | LLM backend: openai, anthropic, ollama                                       | —                    |
| `LLM_MODEL`                         | Model identifier                                                             | —                    |
| `LLM_API_KEY`                       | API credentials                                                              | —                    |
| `LLM_API_URL`                       | Custom endpoint (OpenRouter, local Ollama)                                   | Provider default     |
| `LLM_MAX_TOKENS`                    | Max output tokens per response                                               | `4096`               |
| `EMBEDDING_PROVIDER`                | Embedding backend: openai, ollama                                            | `LLM_PROVIDER`       |
| `EMBEDDING_MODEL`                   | Embedding model                                                              | `LLM_MODEL`          |
| `EMBEDDING_API_KEY`                 | Embedding API credentials                                                    | `LLM_API_KEY`        |
| `EMBEDDING_API_URL`                 | Embedding endpoint                                                           | `LLM_API_URL`        |
| `UTILITY_LLM_PROVIDER`              | Cheap LLM for background tasks (contextual retrieval, query rewriting, HyDE) | `LLM_PROVIDER`       |
| `UTILITY_LLM_MODEL`                 | Utility LLM model                                                            | `LLM_MODEL`          |
| `UTILITY_LLM_API_KEY`               | Utility LLM credentials                                                      | `LLM_API_KEY`        |
| `UTILITY_LLM_API_URL`               | Utility LLM endpoint                                                         | `LLM_API_URL`        |
| `RERANKER_PROVIDER`                 | Cross-encoder reranker: cohere, jina, or generic                             | — (keyword fallback) |
| `RERANKER_MODEL`                    | Reranker model (e.g. `rerank-v3.5`, `jina-reranker-v2-base-multilingual`)    | —                    |
| `RERANKER_API_KEY`                  | Reranker API credentials                                                     | `LLM_API_KEY`        |
| `RERANKER_API_URL`                  | Reranker endpoint (required for generic provider)                            | Provider default     |
| `SKIPPER_ENABLE_HYDE`               | Enable Hypothetical Document Embeddings for search_knowledge                 | `false`              |
| `SEARCH_PROVIDER`                   | Search backend: tavily, brave, searxng                                       | —                    |
| `SEARCH_API_KEY`                    | Search API credentials                                                       | —                    |
| `SEARCH_API_URL`                    | Custom search endpoint                                                       | Provider default     |
| `SITEMAPS`                          | Comma-separated sitemap URLs                                                 | —                    |
| `SKIPPER_SITEMAPS_DIR`              | Directory of source files (re-read each cycle)                               | —                    |
| `CRAWL_INTERVAL`                    | Refresh interval for crawling                                                | `24h`                |
| `CHUNK_TOKEN_LIMIT`                 | Max BPE tokens per chunk                                                     | `500`                |
| `CHUNK_TOKEN_OVERLAP`               | Overlap tokens between chunks                                                | `50`                 |
| `SKIPPER_ENABLE_RENDERING`          | Enable headless Chrome for JS-rendered pages                                 | `false`              |
| `SKIPPER_CONTEXTUAL_RETRIEVAL`      | Use utility LLM to prepend context before embedding                          | `false`              |
| `SKIPPER_LINK_DISCOVERY`            | Discover and crawl same-domain links                                         | `false`              |
| `SKIPPER_SEARCH_LIMIT`              | Default result limit for `search_knowledge`                                  | `8`                  |
| `SKIPPER_MAX_HISTORY_MESSAGES`      | Max conversation messages loaded per request                                 | `20`                 |
| `SKIPPER_WEB_UI`                    | Enable embedded web UI at `/`                                                | `true`               |
| `SKIPPER_REQUIRED_TIER_LEVEL`       | Minimum subscription tier                                                    | `3`                  |
| `SKIPPER_CHAT_RATE_LIMIT_PER_HOUR`  | Rate limit per tenant                                                        | `0` (unlimited)      |
| `SKIPPER_CHAT_RATE_LIMIT_OVERRIDES` | Per-tenant overrides (`tenant_id:limit,...`)                                 | —                    |
| `SKIPPER_ADMIN_TENANT_ID`           | Tenant ID for global/platform knowledge                                      | —                    |
| `SKIPPER_API_KEY`                   | API key for admin WebUI authentication (see Web UI)                          | — (network-trust)    |
| `GATEWAY_PUBLIC_URL`                | API Gateway base URL (MCP endpoint derived as `$URL/mcp`)                    | —                    |

## Web UI

Skipper includes an embedded web UI served at `/` when `SKIPPER_WEB_UI` is enabled (the default). The UI is compiled into the Go binary via `go:embed` — no external files or build steps required.

Features: conversation sidebar, SSE-streamed chat, markdown rendering, confidence badges, citations, dark/light mode. The UI reads configuration from `<meta>` tags injected at serve time.

**Authentication:** When `SKIPPER_API_KEY` is set, the admin WebUI requires authentication. Users enter the key once and receive an HMAC-signed session cookie (24h, httponly). When the key is not set, the WebUI uses network-trust (no client-side auth) and logs a warning on startup.

Set `SKIPPER_WEB_UI=false` to run in headless API-only mode.

## Knowledge Pipeline

### Ingestion

```
┌─────────────────────── Ingestion ───────────────────────┐
│                                                          │
│  Sitemaps / Direct Pages / Uploads                       │
│         │                                                │
│         ▼                                                │
│  URL Validation (SSRF check, DNS, private CIDR block)    │
│         │                                                │
│         ▼                                                │
│  robots.txt (SkipperBot/1.0 user-agent)                  │
│         │                                                │
│         ▼                                                │
│  Page Cache (TTL / ETag / Content Hash)                  │
│         │ (skip if unchanged)                            │
│         ▼                                                │
│  HTTP Fetch ──► SPA Detection ──► Headless Chrome (Rod)  │
│         │      (score ≥ 4)        stealth mode, blocks   │
│         │                         images/fonts/CSS       │
│         ▼                                                │
│  Content Extraction (Readability → Markdown)             │
│         │              fallback: DOM walker              │
│         ▼                                                │
│  Content Hash (SHA-256, skip embed if unchanged)         │
│         │                                                │
│         ▼                                                │
│  Chunking (~500 tokens, 50 overlap, heading-aware)       │
│         │                                                │
│         ▼                                                │
│  [Contextual Retrieval] ──► Embedding                    │
│   (opt-in: utility LLM      (batched, up to 2048/call)  │
│    prepends 1-2 sentence                                 │
│    context per chunk)                                    │
│         │                                                │
│         ▼                                                │
│  pgvector (atomic delete + insert per source)            │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

**SPA detection** uses a scoring heuristic: SPA mount points (`#root`, `#app`, `#__next`) +3, `<noscript>` +2, framework markers (`data-reactroot`, `ng-app`, `data-v-`) +3, high script-to-text ratio +2, low body text density +2. Score ≥ 4 or < 10 extracted words triggers headless Chrome via Rod with stealth mode enabled and non-essential resources blocked. A HEAD check compares `Content-Length` against cached `raw_size` before launching Chrome — if identical, rendering is skipped entirely.

**Chunking** splits on newline-separated blocks, preserves Markdown headings as prefixes, and applies overlap between adjacent chunks. Chunks < 20 tokens or with > 50% short words (navigation menus) are dropped. Exact duplicates are filtered after normalization.

**Contextual retrieval** (`SKIPPER_CONTEXTUAL_RETRIEVAL=true`) calls a utility LLM with the document title + first 300 words + chunk previews, and prepends the LLM-generated context to each chunk before embedding (not stored for retrieval display).

### Retrieval

**Pre-retrieval** (every message, fast path):

```
User message → hybrid search → cross-encoder rerank → deduplicate → inject context
```

- **Hybrid search** = BM25 full-text search + embed query → semantic/vector search, then merge via Reciprocal Rank Fusion (RRF).
- **Cross-encoder rerank** = reads each (query, candidate chunk) pair to score relevance. Falls back to a weighted heuristic (0.7 × vector similarity + 0.3 × keyword overlap) when no reranker is configured.

No query rewriting or HyDE on this path — keeps latency low since it runs every message.

**search_knowledge tool** (explicit, can afford more latency):

```
User query → query rewrite (utility LLM) → rewritten query

Hybrid search with HyDE:
  rewritten query → HyDE (utility LLM) → embed → semantic/vector search
  rewritten query → full-text search (BM25)

  merge (RRF) → cross-encoder rerank → deduplicate → return
```

**Query rewriting** (requires `UTILITY_LLM_*` config) transforms conversational queries into search-optimized queries before embedding ("my stream keeps dying" → "stream disconnection troubleshooting"). Applied to `search_knowledge` and `search_web` tool calls but skipped for pre-retrieval to keep latency low.

**HyDE** — Hypothetical Document Embeddings (`SKIPPER_ENABLE_HYDE=true`) generates a hypothetical answer via the utility LLM, then embeds that answer instead of the question. The resulting vector is closer in embedding space to real documentation chunks. Only used for `search_knowledge`; skipped for pre-retrieval (latency) and web search (search engines handle questions natively).

**Deduplication** caps any single source URL at 2 chunks in the final result set.

The LLM can also explicitly call `search_knowledge` with a `tenant_scope` parameter (`tenant`, `global`, or `all`) to target specific knowledge partitions. The tool over-fetches 3× the requested limit, reranks, then deduplicates to the final count.

Responses include citations and a confidence score based on whether the answer was matched in the knowledge base, from a web search, or from inference.

## Standalone Mode (Planned)

Skipper's internal dependencies are decoupled behind interfaces (Phase 1) so all platform components — gRPC clients, Kafka, billing — gracefully degrade to nil. Standalone mode will consolidate into the existing binary: when `JWT_SECRET` is absent, Skipper runs with API key auth, auto-migration, and no platform wiring. See `PLAN_SKIPPER_APPLIANCE.md` for details.
