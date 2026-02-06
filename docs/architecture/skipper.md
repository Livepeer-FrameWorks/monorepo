# Skipper Architecture

Skipper is the AI video consultant service. It provides RAG-grounded, tool-augmented chat for streaming troubleshooting and configuration guidance.

## Overview

- **Service:** `api_consultant/` (Go)
- **Ports:** 18018 (HTTP), 19007 (gRPC)
- **Database:** PostgreSQL with pgvector extension
- **RFC:** `docs/rfcs/mcp-consultant/mcp-consultant.md`
- **PLAN:** `PLAN_SKIPPER.md`

## Subsystems

### Chat Orchestrator

Handles `POST /api/skipper/chat`. Receives a user message, loads conversation history, calls an LLM with tool definitions, executes tool calls in a loop (max 5 rounds), and streams the response via SSE.

### Knowledge Base (RAG)

pgvector-backed vector store for documentation retrieval:

- **Crawler** — fetches sitemaps, extracts text from HTML pages
- **Embedder** — chunks documents (~500 tokens, 50 overlap), generates embeddings
- **Store** — cosine similarity search over `skipper_knowledge` table

### Tool System

LLM can invoke these tools during orchestration:

| Tool                   | Source                             | Purpose                          |
| ---------------------- | ---------------------------------- | -------------------------------- |
| `search_knowledge`     | pgvector store                     | RAG retrieval from embedded docs |
| `search_web`           | pkg/search/ (Tavily/Brave/SearXNG) | Live web search                  |
| `diagnose_rebuffering` | Periscope gRPC                     | Stream health diagnostics        |
| `diagnose_routing`     | Periscope gRPC                     | Viewer routing analysis          |

### Confidence Tagging

Every response section is tagged: `verified`, `sourced`, `best_guess`, or `unknown`. Sources are cited with URLs when available.

### Conversation Persistence

Multi-turn conversations stored in `skipper_conversations` / `skipper_messages` tables. All queries scoped by `tenant_id`.

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

| Variable          | Purpose                                    |
| ----------------- | ------------------------------------------ |
| `LLM_PROVIDER`    | LLM backend: openai, anthropic, ollama     |
| `LLM_MODEL`       | Model identifier                           |
| `LLM_API_KEY`     | API credentials                            |
| `LLM_API_URL`     | Custom endpoint (OpenRouter, local Ollama) |
| `SEARCH_PROVIDER` | Search backend: tavily, brave, searxng     |
| `SEARCH_API_KEY`  | Search API credentials                     |
| `SEARCH_API_URL`  | Custom search endpoint                     |

## Data Flow

```
User message
  → Chat Handler (JWT auth, tenant extraction)
    → Orchestrator (LLM + tool loop)
      → search_knowledge (pgvector)
      → search_web (Tavily/Brave)
      → diagnose_* (Periscope gRPC)
    → Confidence tagging
  → SSE stream to client
  → Conversation persistence
```
