# Security Policy

## Reporting Vulnerabilities

**Do not open public issues for security vulnerabilities.**

Email **info@frameworks.network** with:

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Any suggested fixes (optional)

We'll acknowledge your report within 48 hours and work with you on a fix. Once resolved, we'll credit you in the release notes (unless you prefer to remain anonymous).

## Supported Versions

Only the latest release receives security updates. We recommend always running the most recent version.

## Security Practices

- Dependencies are monitored via Dependabot
- All PRs require review before merge
- Secrets are managed via environment variables (Vault in production)
- No credentials are committed to the repository
