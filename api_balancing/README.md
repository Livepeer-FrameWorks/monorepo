# Foghorn (Load Balancer)

Go implementation of MistServer's load balancer, replacing the original C++ MistUtilLoad binary.

## Overview

Routes streaming traffic to the best available media nodes based on:
- Geographic proximity
- Node performance (CPU, RAM, bandwidth)
- Stream availability
- Configurable weights

## Integration

- Receives node health updates from Helmsman
- Provides 100% compatible API for MistServer nodes
- Posts routing decisions to analytics pipeline

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Foghorn: `cd api_balancing && go run ./cmd/foghorn`

## Health & ports
- Health: `GET /health`
- HTTP: 18008 (routing API)
- gRPC control: 18019

Configuration is sourced from `config/env/base.env` + `config/env/secrets.env`. Generate `.env` with `make env` (or `frameworks config env generate`) before starting the stack. Adjust `config/env/secrets.env` for credentials. See `docs/configuration.md` for the full workflow. Do not commit secrets.

## Related
- Root `README.md` (ports, stack overview)
- `docs/IMPLEMENTATION.md` (balancing strategy)
