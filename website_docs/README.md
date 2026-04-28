# FrameWorks Documentation Site

Built with [Astro Starlight](https://starlight.astro.build/).

## Development

This project is part of the FrameWorks pnpm workspace.

```bash
# From the root of the monorepo
pnpm install

# Run dev server
pnpm --dir website_docs dev
```

## Build

```bash
pnpm --dir website_docs build
```

## Validation

```bash
pnpm --dir website_docs check:links
pnpm --dir website_docs check:graphql
pnpm --dir website_docs check:sdk-imports
```

## Deployment

This site is built as a static asset and served via nginx (docker) or any static host.
See `Dockerfile` for the containerized build process.
