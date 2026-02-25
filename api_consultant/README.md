# Skipper (AI Video Consultant)

RAG-grounded, tool-augmented chat for streaming troubleshooting, configuration guidance, and automated diagnostics. **Bring your own LLM — runs with any OpenAI-compatible provider, local or hosted.**

## Why Skipper?

- **Tenant isolation**: Every conversation, knowledge query, and diagnostic is scoped to the authenticated tenant
- **Self-hosted AI**: Run with a local model (Ollama, vLLM) for complete data sovereignty — no data leaves your infrastructure
- **No cloud lock-in**: Swap LLM, embedding, and search providers via environment variables without code changes

## What it does

- Multi-turn chat with SSE streaming and tool-use (up to 5 rounds per query)
- Knowledge base powered by pgvector — crawls sitemaps, chunks documents, serves semantic search
- Confidence tagging on responses: `verified`, `sourced`, `best_guess`, `unknown`
- MCP spoke: exposes `ask_consultant`, `search_knowledge`, `search_web` tools to the Gateway hub
- MCP client: invokes Gateway tools (diagnostics, GraphQL, stream management) on behalf of the user
- Heartbeat agent: periodic stream health analysis with email/WebSocket/MCP notifications
- Usage metering and per-tenant rate limiting

## Run (dev)

- Start the full stack from repo root: `docker-compose up -d`
- Or run just Skipper: `cd api_consultant && go run ./cmd/skipper`

## Health & ports

- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18018
- gRPC: 19007

## Configuration

Configuration lives in `config/env/base.env` and `config/env/secrets.env`. Generate `.env` with `make env` or `frameworks config env generate`. Do not commit secrets.

Key secrets:

- `LLM_PROVIDER`, `LLM_MODEL`, `LLM_API_KEY` — primary language model
- `EMBEDDING_PROVIDER`, `EMBEDDING_MODEL`, `EMBEDDING_API_KEY` — vector embeddings (defaults to LLM values if unset)
- `SEARCH_PROVIDER`, `SEARCH_API_KEY` — web search (Tavily, Brave, SearXNG)
- `SITEMAPS` — CSV of sitemap URLs to crawl for the knowledge base
- `SKIPPER_API_KEY` — admin key for the embedded WebUI (optional)

See `docs/architecture/skipper.md` for the full variable reference.

## Further reading

- Architecture: `docs/architecture/skipper.md`
- MCP & agent access: `docs/architecture/agent-access.md`
- Operator guide: `website_docs/src/content/docs/operators/skipper.mdx`
