# RFC: Cluster Marketplace

## Status

Draft

## TL;DR

- Define a marketplace model for clusters with visibility, access control, and pricing.
- Core schema exists; multi-cluster routing and failover remain future work.
- Split future phases into dedicated RFCs as needed.

## Current State (as of 2026-01-13)

- Marketplace-related schema exists in Quartermaster and Purser.
- No evidence in this repo of DNS-level routing or cross-cluster failover.

Evidence:

- `pkg/database/sql/schema/quartermaster.sql`
- `pkg/database/sql/schema/purser.sql`

## Problem / Motivation

To open the platform to third-party operators, we need cluster discovery, access control, and pricing while maintaining tenant isolation and billing integrity.

## Goals

- Cluster visibility and invitation flow.
- Tenant access controls with approvals.
- Pricing models tied to billing tiers.

## Non-Goals

- Full multi-cluster routing in this RFC.
- Automated failover between clusters.

## Proposal

- Use Quartermaster for cluster visibility/access metadata.
- Use Purser for per-cluster pricing configuration.
- Bridge exposes discovery, access, and subscription flows.

## Impact / Dependencies

- Quartermaster and Purser schemas.
- Bridge GraphQL and UI workflows.
- Billing tier enforcement.

## Alternatives Considered

- Single-operator only (status quo).
- Fully open cluster registry without approvals.

## Risks & Mitigations

- Risk: mispriced clusters or abuse. Mitigation: approvals + tier gates.
- Risk: UX confusion. Mitigation: explicit visibility states.

## Migration / Rollout

1. Validate schema and API coverage for discovery + access.
2. Add UI flows for invites and subscriptions.
3. Document operator onboarding.

## Open Questions

- How are cluster SLAs enforced across operators?
- What is the minimum compliance bar for third-party clusters?

## References, Sources & Evidence

- `pkg/database/sql/schema/quartermaster.sql`
- `pkg/database/sql/schema/purser.sql`
- `pkg/graphql/schema.graphql`
