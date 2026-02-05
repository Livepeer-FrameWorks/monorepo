# Security Policy

## Reporting Vulnerabilities

**Do not open public issues for security vulnerabilities.**

**Preferred:** Report via [GitHub Private Vulnerability Reporting](https://github.com/livepeer-frameworks/monorepo/security/advisories/new).
This creates a private advisory where we can collaborate on a fix before public disclosure.

**Alternative:** Email **security@frameworks.network** with a description, steps to reproduce, and potential impact.

We acknowledge reports within 48 hours. Once resolved, we credit reporters in release notes unless they prefer anonymity.

## Supported Versions

Only the latest release receives security updates.

## Security Practices

- Dependencies monitored via Dependabot (Go, NPM, GitHub Actions)
- Docker images built with SBOM and provenance attestation
- All PRs require code review (CODEOWNERS)
- Secrets managed via environment variables (Vault in production)
- No credentials committed to the repository
