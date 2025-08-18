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
- Caching: Redis for query results
- Auth: JWT validation via Commodore
- Subscriptions: WebSocket connection to Signalman
- Service calls: HTTP to internal services over WireGuard mesh

## Configuration

| Variable | Required | Description |
|----------|----------|-------------|
| `BRIDGE_PORT` | No | HTTP port (default: 18000) |
| `COMMODORE_URL` | Yes | Commodore API URL |
| `PERISCOPE_QUERY_URL` | Yes | Periscope Query API URL |
| `PURSER_URL` | Yes | Purser API URL |
| `SIGNALMAN_WS_URL` | Yes | Signalman WebSocket URL |
| `JWT_SECRET` | Yes | JWT validation secret |
| `SERVICE_TOKEN` | Yes | Service-to-service auth token |
| `REDIS_URL` | No | Redis connection URL for caching |
| `GRAPHQL_PLAYGROUND_ENABLED` | No | Enable GraphQL playground (default: false) |
| `GRAPHQL_COMPLEXITY_LIMIT` | No | Max query complexity (default: 200) |
| `LOG_LEVEL` | No | `debug|info|warn|error` |

Health: `GET /health`.
GraphQL: `POST /graphql`.
Playground: `GET /graphql` (if enabled).

Cross-refs: see root README "Ports" and docs/IMPLEMENTATION.md for service boundaries.