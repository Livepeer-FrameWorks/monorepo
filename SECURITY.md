# Security Policy

We welcome reports from security researchers and treat them as collaborators. This
document is our Vulnerability Disclosure Policy (VDP): how to report, what's in scope,
and the safe harbor we extend to good-faith research.

## Reporting Vulnerabilities

**Do not open public issues for security vulnerabilities.**

**Preferred:** Report via [GitHub Private Vulnerability Reporting](https://github.com/livepeer-frameworks/monorepo/security/advisories/new).
This creates a private advisory where we can collaborate on a fix before public disclosure.

**Alternative:** Email **security@frameworks.network** with a description, steps to
reproduce, and potential impact.

We acknowledge reports within 48 hours. Once resolved, we credit reporters in release
notes unless they prefer anonymity.

## Safe Harbor

We consider security research and vulnerability disclosure conducted under this policy
to be authorized, lawful, and in good faith. If you act in accordance with this policy:

- We will **not pursue or support legal action** against you for accidental, good-faith
  violations, including under anti-hacking laws (e.g. the CFAA) or anti-circumvention
  laws (e.g. the DMCA).
- We consider your research **authorized access** for the purpose of any applicable
  computer-misuse statutes, so that it does not constitute unauthorized access.
- If a third party brings legal action against you for activity that complied with this
  policy, we will make our authorization known.

You are expected to comply with all applicable laws. If in doubt about whether a
specific action is authorized, ask us first at security@frameworks.network. This safe
harbor applies only to legal claims under our control; it cannot bind third parties.

## Scope

**In scope**

- Our web surfaces under `frameworks.network` (marketing, docs, and the application at
  `chartroom.frameworks.network`) and the GraphQL API gateway.
- The open-source services and libraries in this repository.
- Self-hosted deployments you operate yourself (test only against your own instances).

**Out of scope / hard rules** — the following are never authorized:

- Accessing, modifying, or exfiltrating data belonging to **any tenant other than a test
  account you control**. Confirm an access-control finding with your own resources; do
  not pivot into real customer data.
- **Denial of service**, load/stress testing, or volumetric attacks against production
  ingest, edge delivery, or APIs.
- **Disrupting live streams** or pushing media to ingest endpoints you do not own.
- Automated mass scanning that degrades service, spam/social-engineering of staff or
  customers, or physical attacks.
- Data exfiltration beyond the **minimum proof-of-concept** needed to demonstrate the
  issue. If you encounter sensitive data (credentials, personal data, keys), stop,
  do not save or share it, and tell us in the report.

## Preferences (rules of engagement)

- Report each issue as soon as you can after discovery.
- Give us a reasonable time to remediate before any public disclosure, and coordinate
  timing with us (**coordinated disclosure** — ask before publishing).
- Use test accounts and your own tenants; use the minimum interaction needed to prove
  impact.

## Supported Versions

Only the latest release receives security updates.

## Security Practices

- Dependencies monitored via Dependabot (Go, NPM, GitHub Actions)
- Docker images built with SBOM and provenance attestation
- All PRs require code review (CODEOWNERS)
- Secrets managed via environment variables (Vault in production)
- No credentials committed to the repository
