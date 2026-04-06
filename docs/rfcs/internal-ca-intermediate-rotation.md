# RFC: Internal CA Intermediate Rotation

## Status

Draft

## TL;DR

- The current internal PKI supports initial bootstrap and normal leaf renewal, but it does not yet support clean rollover from one intermediate CA to another.
- Add dual-intermediate support in Navigator, dual-bundle trust distribution through Privateer, and a simple operator workflow so intermediate rotation does not break live gRPC traffic.

## Problem / Motivation

The current internal PKI implementation is good enough for:

- first production rollout
- ongoing leaf renewal under the same intermediate

It is not yet good enough for:

- planned intermediate renewal before expiry
- emergency intermediate replacement after suspected compromise

Today the system effectively assumes one active intermediate at a time:

- Navigator stores one active signing intermediate
- `GetCABundle()` returns one root + one intermediate
- new leaves are all signed by that one intermediate

That means replacing the intermediate later would require a sharp cutover. During a mixed window, some services would still
present leaf certs from the old intermediate while other clients might already trust only the new intermediate. That creates
avoidable validation failures.

## Current State

Implemented today:

- offline-ish root plus online intermediate model in Navigator
- internal leaf issuance authorized by the Quartermaster + Navigator + Privateer node-based flow
- Privateer distribution of CA bundle + service leaf certs to `/etc/frameworks/pki/...`
- repeated leaf renewal under a single intermediate

Not implemented today:

- storing more than one active/grace intermediate in Navigator
- returning a dual-intermediate CA bundle during overlap
- switching issuance to a new intermediate while the old one remains trusted
- retiring the old intermediate after convergence

## Goals

- Support intermediate CA rotation without breaking live internal gRPC traffic.
- Keep the root private key offline and out of Navigator runtime.
- Reuse the existing Quartermaster + Navigator + Privateer model.
- Make rotation operationally simple enough for both scheduled renewal and incident response.

## Non-Goals

- Root CA rotation in this RFC
- CRL / OCSP / revocation infrastructure
- Replacing the existing node-authenticated issuance model
- Changing public/browser-facing TLS

## Proposal

### 1. Support multiple intermediate records in Navigator

Navigator should track intermediate roles explicitly:

- `current`: used for new leaf issuance
- `grace`: still trusted during overlap, but no longer used for new issuance

The root remains the long-lived trust anchor. Intermediates remain leaf issuers.

### 2. Return overlap bundles from `GetCABundle()`

During steady state:

- bundle contains `root + current intermediate`

During rotation:

- bundle contains `root + old grace intermediate + new current intermediate`

This lets clients validate both old and new leaf certificates during the transition window.

### 3. Switch issuance to the new intermediate only

After the new intermediate is imported:

- all newly issued leaf certs are signed by the new `current` intermediate
- existing leaf certs from the old intermediate remain valid until they naturally expire or are replaced

This keeps rollover bounded to normal cert renewal and restart/reload behavior.

### 4. Reuse Privateer for distribution

No new distribution subsystem is required.

Privateer already:

- fetches CA bundle material from Navigator
- writes it to `/etc/frameworks/pki/ca.crt`
- syncs service leaf certs to `/etc/frameworks/pki/services/<service>/...`

Rotation support only requires that the fetched bundle may temporarily contain more than one intermediate.

### 5. Operator workflow

The expected operator sequence is:

1. Generate a new intermediate offline using the existing root.
2. Import the new intermediate into Navigator and mark it `current`.
3. Mark the old intermediate `grace`.
4. Navigator starts issuing new leaf certs from the new intermediate.
5. Privateer distributes a CA bundle containing both intermediates.
6. Services renew/reload/restart onto new leafs.
7. After the overlap window, remove the old intermediate from trust and from Navigator active state.

## Data Model

Navigator internal CA state should distinguish:

- one `root_cert_only`
- one `current` intermediate
- zero or more `grace` intermediates retained until retirement

Whether this is modeled via:

- a new status column
- explicit role values
- or a separate rotation table

is an implementation detail. The required behavior is more important than the exact schema shape.

## Operational Policy

Recommended cadence:

- leaf certs: continue short-lived rotation (`72h` today)
- intermediate: rotate deliberately before expiry, e.g. yearly or every 18 months even if valid for 2 years
- root: rotate only on long-lifecycle schedule or compromise

Recommended overlap window:

- long enough for Privateer sync, service restarts/reloads, and old leaf expiry
- short enough to avoid indefinite trust sprawl

For the current `72h` leaf lifetime, the simplest safe overlap window is slightly longer than max leaf lifetime.

## Failure / Recovery Model

### Planned rotation

- import new intermediate
- distribute dual bundle
- wait for fleet convergence
- retire old intermediate

### Suspected intermediate compromise

- generate/import new intermediate
- distribute dual bundle only for the shortest practical recovery window
- aggressively replace leaf certs
- retire compromised intermediate as soon as the fleet is converged

### Root compromise

Out of scope for this RFC. That remains a larger full-PKI recovery event.

## Impact / Dependencies

- Navigator internal CA storage and bundle generation
- Navigator issuer selection logic
- Privateer CA-bundle sync path
- service restart/reload policy for newly written leaf certs
- operator documentation and rollout runbook

## Migration / Rollout

1. Extend Navigator storage and `GetCABundle()` to support current + grace intermediates.
2. Keep steady-state behavior unchanged when only one intermediate exists.
3. Add operator import flow for a new intermediate signed by the existing root.
4. Exercise one non-emergency rotation in staging.
5. Document the production runbook.

## Open Questions

- Should Navigator retain more than one grace intermediate, or exactly one?
- Do we want an explicit `activate rotation` operator command, or is importing a new `current` intermediate enough?
- Should the overlap window be time-based, leaf-observation-based, or operator-driven?

## References, Sources & Evidence

- `api_dns/internal/logic/internal_ca.go`
- `api_mesh/internal/agent/agent.go`
- `PLAN_REPO_WIDE_GRPC_TLS_2026-04-02.md`
- `docs/rfcs/grpc-tls-mesh.md`
