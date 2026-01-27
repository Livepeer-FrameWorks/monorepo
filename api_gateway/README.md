# The Bridge (API Gateway)

The Bridge is the GraphQL API Gateway that provides a unified interface for all client applications. It aggregates data from multiple microservices into a single, strongly-typed GraphQL API—**same API whether self-hosted or cloud-managed**.

## Why Bridge?

- **Consistent API**: Identical GraphQL schema for self-hosted, hybrid, and managed deployments
- **Tenant isolation**: JWT-based multi-tenancy ensures strict data boundaries
- **No vendor lock-in**: Switch deployment models without changing client code

## What it does

- GraphQL API endpoint for web app, mobile, and developer APIs
- Real-time subscriptions via WebSocket
- Service aggregation from Commodore, Periscope, Purser, Signalman
- Authentication and authorization layer
- Query optimization with DataLoader pattern

## Architecture

- Framework: gqlgen (Go GraphQL server)
- Caching: optional Redis for query results
- Auth: JWT or service token; minimal public allowlist (status, viewer endpoint resolve, ingest endpoint resolve); WebSocket authenticates on init
- Subscriptions: WebSocket via Signalman
- Service calls: gRPC (primary) with a few HTTP integrations where needed

## Run (dev)

- Start the full stack from repo root: `docker-compose up -d`
- Or run just Bridge: `cd api_gateway && go run ./cmd/bridge`

## Health & endpoints

- Health: `GET /health`
- HTTP: 18000 (see root README “Ports”)
- GraphQL: `POST /graphql`, WS: `/graphql/ws` (Playground optional in non‑release builds)

Configuration relies on the shared env layers under `config/env`. Generate `.env` with `make env` (or `frameworks config env generate`) and keep secrets in `config/env/secrets.env`. Do not commit secrets.

## Related

- Root `README.md` (ports, stack overview)
- `website_docs/` (architecture, service boundaries)
