# The Bridge (API Gateway)

The Bridge is the GraphQL API Gateway that provides a unified interface for all client applications. It aggregates data from multiple microservices into a single, strongly-typed GraphQL API.

## What it does
- GraphQL API endpoint for web app, mobile, and developer APIs
- Real-time subscriptions via WebSocket
- Service aggregation from Commodore, Periscope, Purser, Signalman
- Authentication and authorization layer
- Query optimization with DataLoader pattern
- REST compatibility layer for gradual migration

## Architecture
- Framework: gqlgen (Go GraphQL server)
- Caching: optional Redis for query results
- Auth: JWT or service token; minimal public allowlist (status, player endpoint resolve); WebSocket authenticates on init
- Subscriptions: WebSocket via Signalman
- Service calls: HTTP to internal services

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Bridge: `cd api_gateway && go run ./cmd/bridge`

## Health & endpoints
- Health: `GET /health`
- HTTP: 18000 (see root README “Ports”)
- GraphQL: `POST /graphql`, WS: `/graphql/ws` (Playground optional in non‑release builds)

Configuration: copy `env.example` to `.env` and use the inline comments as reference. Do not commit secrets.

## Related
- Root `README.md` (ports, stack overview)
- `docs/IMPLEMENTATION.md` (service boundaries)
