# Chandler (Assets)

S3-backed asset proxy for stream media files. Serves posters, sprite sheets, and VTT files from object storage with an in-memory LRU cache.

## What it does

- Proxies allowed asset files (poster.jpg, sprite.jpg, sprite.vtt) from S3-compatible storage
- LRU cache to avoid redundant S3 fetches
- Tenant-scoped paths — assets are keyed by tenant and stream

## API

HTTP only. Assets are served via Gin routes.

## Run (dev)

- Start the full stack from repo root: `docker-compose up -d`
- Or run just Chandler: `cd api_assets && go run ./cmd/chandler`

Configuration is shared via `config/env/base.env` and `config/env/secrets.env`. Use `make env` or `frameworks config env generate` to create `.env`, and customise `config/env/secrets.env` for secrets. Do not commit secrets.

## Health & ports

- Health: `GET /health`
- HTTP: 18020
