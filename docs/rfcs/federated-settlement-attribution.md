# RFC: Federated Settlement Attribution

## Status

Draft

## TL;DR

- Attribute served traffic to the operators whose clusters actually carried it, in a way
  that stays correct when operators are independent and potentially adversarial: no
  participant can claim traffic they did not serve, because settlement-grade usage must be
  corroborated by signed serving evidence that the claiming operator did not produce alone.
- Close the full loop, not just attribution: manipulation-resistant attribution facts
  (Phase 1) feed the existing `operator_credit_ledger`, get an operator-visible reporting
  surface (Phase 2), and are paid out over real rails — bank and stablecoin — with
  reconciliation, dispute, and clawback handling (Phase 3).
- Build on what exists instead of inventing parallel machinery: the signed
  telemetry-token edge attribution from the analytics pipeline becomes the seed of the
  serving-evidence chain; Purser's Stripe and x402 settlement machinery becomes the seed
  of the payout rails; the fail-closed per-cluster attribution posture is preserved
  throughout.

## Owning services / modules

**Foghorn** owns serving evidence: it makes the routing decision, mints the signed
serving-evidence tokens at resolve time, and emits the routing and session lifecycle
triggers that anchor every settlement claim. **Periscope** (Ingest + Query) owns
attribution facts: settlement-grade ClickHouse tables, the corroboration computation, and
the per-cluster usage reports consumed by billing. **Purser** owns money: the operator
credit ledger, statement generation, payout batching, rail execution, reconciliation, and
clawbacks. Bridge exposes the operator-facing GraphQL surface but owns no settlement
logic; Quartermaster supplies cluster identity and operator vetting state
(`cluster_owners`) but is not a settlement participant.

## Current State

**Per-cluster usage attribution exists and fails closed.** Foghorn stamps `cluster_id`
(and `origin_cluster_id` from `ValidateStreamKey` / `ResolveIdentifier`) on every trigger
it forwards to Decklog; Periscope-Ingest persists cluster columns into the finalized fact
tables and canonical 5-minute ledgers; Periscope-Query's `generateTenantUsageSummary`
emits one usage-report record per cluster to Purser. An empty `cluster_id` on a rated row
is treated as an ingest bug and billing fails closed rather than guessing an attribution
cluster (`docs/architecture/cross-cluster-billing.md`).

**Routing decisions are attributed per event.** Every viewer endpoint resolution (HTTP
`/play/*` and gRPC `ResolveViewerEndpoint`) emits a `LoadBalancingData` event carrying
`tenant_id` (infra owner), `stream_tenant_id` (subject tenant), `cluster_id` (emitting
cluster), and `remote_cluster_id` for cross-cluster routes, persisted in
`periscope.routing_decisions` and queryable through Periscope-Query and Bridge GraphQL
(`docs/architecture/routing-events-attribution.md`).

**An internal operator credit ledger exists, unsurfaced.**
`purser.operator_credit_ledger` records `accrual` / `clawback` / `adjustment` entries per
cluster owner, sourced from marketplace invoice lines, provider-attributed storage usage
slices, storage usage corrections, and Stripe cluster-subscription invoices
(`api_billing/internal/operator/credit.go`). Platform fees are taken in basis points via
`platform_fee_policy` (per-owner override, then global default, fail-soft to zero fee).
Accruals are idempotent via partial unique indexes per source type, only `paid` invoices
accrue, and unvetted operators (no `cluster_owners` row, or not `approved` +
`payout_eligible`) accrue in `held` status. Clawback linkage to payment reversals exists
(`operator_credit_clawback_reversals`, migration v0.2.33/015). Status vocabulary already
anticipates payout (`accruing`, `eligible`, `paid_out`, `clawed_back`, `held`) and a
`payout_batch_id` column exists — but nothing writes it. Reads happen only via internal
gRPC RPCs; there is **no GraphQL surface**, no operator dashboard, and until the registry
items `settlement-attribution` and `operator-credit-ledger` were added to
`docs/platform-features.yaml` (2026-07-22), no registry presence at all.

**No payout execution.** The `operator` package comment defers "payment-rail payout
batching" to "settlement tooling outside this package"; that tooling does not exist.

**Signed edge attribution exists, but only for diagnostics.** `pkg/telemetrytoken` mints
short-lived HMAC tokens at `resolveViewerEndpoint` time binding a content id to the
serving node/cluster; the player echoes the token on boot/QoE beacons and Bridge sets
`cluster_attributed = 1` only on valid tokens. This is explicitly infrastructure
attribution, not billing truth (`docs/architecture/analytics-pipeline.md`).

**The gap:** every settlement input above is produced by platform-operated services
running on the operator's own infrastructure and is trusted implicitly at ingest. In a
marketplace of independent operators this is a payable-revenue channel fed by
self-reported usage — the emitting cluster asserts its own `cluster_id`, its own session
counts, and its own byte counts, and nothing corroborates them.

## Problem / Motivation

The cluster marketplace pays operators for served usage. The moment operators are
independent businesses, the attribution chain is adversarial: an operator has a direct
financial incentive to inflate viewer sessions, egress bytes, and processing seconds on
their own cluster, or to claim traffic actually served elsewhere. Today nothing prevents
this beyond the fact that operators run our binaries — which is not a security boundary
on hardware they control.

Symmetrically, honest operators have no visibility: accruals exist only as internal
ledger rows. An operator cannot see what they earned, why, for which period, or when they
get paid — and there is no way to pay them. The marketplace cannot launch to third
parties on "trust us, the money is in a table."

The referral RFC (`docs/rfcs/referral-attribution-network-usage.md`) covers acquisition
attribution — who brought a tenant — and deliberately excludes inter-operator
settlement. This RFC is the settlement half.

## Goals

- Settlement-grade usage attribution where no participant — serving operator, resolving
  party, or tenant — can unilaterally inflate or redirect payable usage.
- Serving evidence grounded in signed, verifiable records, extending the existing
  telemetry-token mechanism rather than introducing a second attestation scheme.
- Operator-visible reporting: ledger, period statements, and discrepancy visibility over
  GraphQL and the dashboard.
- Payout execution over bank and stablecoin rails, reusing Purser's Stripe and x402
  machinery where it fits, with reconciliation, disputes, and clawback netting.
- Preserve the fail-closed posture end to end: uncorroborated usage is held, never
  guessed into an operator's balance.
- Keep evidence portable and verifiable by parties, not by virtue of landing in the
  central ClickHouse — see the federation-plane-pluggability dependency below.

## Non-Goals

- Acquisition/referral attribution and reward crediting (referral RFC, stream-balances
  RFC).
- Changes to tenant-facing billing: what customers pay, and the fail-closed per-cluster
  invoice attribution, are unchanged. This RFC governs how the operator share of already
  settled customer revenue is verified and paid out.
- Inter-cluster DTSC transfer pricing between operators (infrastructure cost today;
  separate proposal if it ever becomes a billing item).
- KYC/tax program design. Phase 3 depends on operator vetting outcomes (existing
  `cluster_owners` gate) and names the hook, but the compliance program itself is
  operational scope.
- On-chain consensus or proof-of-delivery protocols. Evidence is signed and auditable,
  not trustless.

## Proposal

### Phase 1: Manipulation-resistant attribution

**Principle: a settlement claim needs corroboration from a party other than its
beneficiary.** Today one emitter (the serving cluster) produces the claim and all its
supporting facts. Phase 1 splits every payable viewer-session into three evidence legs
with distinct producers:

1. **Routing evidence** — the resolving Foghorn records the routing decision
   (`routing_decisions` today) and, new, signs a **serving-evidence token**: an extension
   of the `pkg/telemetrytoken` claims binding `(content_id, session correlation id,
serving node/cluster, origin cluster, resolve timestamp, TTL)`, signed with a
   per-cluster settlement key rather than the single shared platform telemetry secret.
   Key material derives from cluster identity (Quartermaster-issued; align with
   `docs/rfcs/token-authority.md` and `docs/architecture/tls.md`) so a token
   demonstrably came from the resolving party. When resolver and server are the same
   cluster — the common local case — the routing leg alone is self-serving, which is
   exactly why legs 2 and 3 exist.
2. **Client acknowledgment** — the player echoes the serving-evidence token on its
   beacons, as it does today for boot telemetry. A token-attested beacon is evidence that
   a real client received that endpoint and played against it. Beacons remain lossy and
   partially opt-in; they are used as a statistical corroboration band, never as an exact
   per-session gate (see Risks).
3. **Server session facts** — the serving cluster's own `USER_END` finalized sessions
   (`viewer_sessions_final`), which is today's sole source. These remain the _quantity_
   source (minutes, bytes) but stop being self-sufficient for _payability_.

**Corroboration computation (Periscope).** Periscope-Query gains a settlement pass that,
per `(cluster, tenant, meter, period)`, compares operator-reported usage (leg 3) against
corroborated usage: sessions joinable to a routing decision from an independent resolver
and/or a token-attested client acknowledgment. The payable quantity forwarded to Purser
is capped at corroborated usage plus a configured tolerance band; the excess is reported
but not payable. Attribution facts and corroboration outcomes land in dedicated
settlement-grade ClickHouse tables (append-only, versioned like the canonical ledgers) —
separate from diagnostic telemetry, so diagnostic retention/lossiness policies never
silently erode settlement inputs.

**Fail-closed carry-over.** Missing cluster identity already fails billing closed; the
same applies here: sessions with no evidence chain accrue nothing, and clusters whose
reported/corroborated discrepancy exceeds threshold have the period's accruals written in
`held` status pending dispute review, using the existing status machinery.

**Federation caveat.** `docs/rfcs/federation-plane-pluggability.md` (parallel, in
progress) proposes moving control/data-plane authority operator-local; settlement must
not assume the central data plane forever. Phase 1 therefore specifies evidence as
**signed portable records**: a serving-evidence token and its acknowledgment are
verifiable from the record itself plus published cluster keys, not from trust in the
central Periscope having ingested them. Central ClickHouse is the Phase 1 evaluation
venue, explicitly interim; the evidence format is the contract.

### Phase 2: Operator-visible reporting

- **GraphQL**: Bridge exposes the ledger for the authenticated operator tenant — entries
  (source, period, gross/fee/payable, status), balances grouped by status and currency,
  and per-period **statements**: immutable monthly summaries reconciling accrued amounts
  to the attribution facts behind them, including reported-vs-corroborated deltas so an
  operator sees _why_ usage was held, not just that it was. Purser already has internal
  gRPC read RPCs; Phase 2 wraps them for the operator scope (tenant-filtered, as all
  queries must be).
- **Dashboard**: an operator earnings section in Chartroom — balance, statement history,
  payout history and status, discrepancy/dispute view. Follows the existing
  infra-operator visibility model used by routing events (owner-tenant scoping).
- **Registry closure**: flips the `graphql`/`webapp`/`docs` required surfaces on the
  `settlement-attribution` registry item; `operator-credit-ledger` stays a platform
  foundation item.

Phase 2 has no ordering dependency on Phase 1's enforcement: reporting over today's
trust-based accruals is already valuable and can ship while Phase 1 runs in shadow mode.

### Phase 3: Payout execution

- **Batching (Purser).** A `payout_batches` table plus a batch builder: select
  `eligible` ledger rows past a settlement lag (clawback exposure window aligned with the
  payment-reversal horizon), net clawbacks against accruals per owner, group by currency,
  apply a minimum payout threshold, and stamp `payout_batch_id` (the column already
  exists). Owner-level netting means a negative balance carries forward — it is never
  invoiced back.
- **Rails.** Two initial rails, both behind one payout-state machine:
  - _Bank_: Stripe transfers to connected operator accounts, reusing Purser's Stripe
    client, webhook plumbing, and the async money-safety pattern from
    `docs/architecture/payment-settlement.md` — a payout row reaches `paid_out` only on
    the rail's settlement confirmation, never on submission.
  - _Stablecoin_: reuse the x402/crypto wallet machinery (HD wallet, gas monitor, and
    the x402 settlement reconciler state machine) inverted for outbound transfers to an
    operator-registered address.
- **Reconciliation.** The payout state machine mirrors the x402 reconciler pattern:
  submitted → confirmed/failed with idempotent transitions; a failed or reversed payout
  returns its ledger rows to `eligible` (or writes clawbacks if funds moved), and every
  batch reconciles ledger sum = rail amount before and after execution.
- **Disputes and clawbacks.** Post-payout customer payment reversals already generate
  clawback entries via `operator_credit_clawback_reversals`; Phase 3 adds the netting
  described above and an operator-initiated dispute flow over held/discrepant periods
  (Phase 1 output), resolved by adjustment entries — the ledger already models
  `adjustment` with `reverses_ledger_id` lineage.
- **Eligibility gate.** Payouts remain gated on the `cluster_owners` vetting state
  (`approved` + `payout_eligible`); rail onboarding (bank account / address
  registration, KYC hand-off) hangs on the same gate.

## Impact / Dependencies

- **Foghorn** (`api_balancing`): serving-evidence token minting at both resolve paths;
  session-correlation id threading into `USER_*` triggers; per-cluster settlement key
  handling.
- **Periscope** (`api_analytics_ingest`, `api_analytics_query`, ClickHouse schema):
  settlement-grade fact tables, corroboration pass, extended usage-report records toward
  Purser.
- **Purser** (`api_billing`): corroboration-aware accrual gating, statements,
  `payout_batches` schema + state machine, rail integrations, dispute/adjustment RPCs.
- **Bridge / GraphQL / webapp**: operator earnings API and dashboard
  (`pkg/graphql/schema.graphql`, Chartroom).
- **Quartermaster**: cluster settlement-key registration alongside existing cluster
  identity; no settlement logic.
- **pkg**: `telemetrytoken` claim extension (or a sibling settlement-token package),
  proto changes for evidence fields.
- **Cross-RFC**: `federation-plane-pluggability.md` (evidence portability requirement,
  above); `token-authority.md` / `service-identity-and-cluster-binding.md` (key
  derivation); `referral-attribution-network-usage.md` (adjacent, non-overlapping).

## Alternatives Considered

- **Status quo (trust operator-reported usage).** Acceptable while all operators are
  first-party; not a launchable marketplace posture.
- **Central log audit / sampling probes only.** Synthetic viewers probing operator
  clusters detect gross fraud but cannot bound payable quantities; kept as a
  complementary detection signal, not the attribution basis.
- **Per-request cryptographic receipts (client-signed delivery proofs).** Strongest
  guarantee, but requires client-side keys and per-segment signing; cost and player
  complexity are out of proportion when statistical corroboration with fail-closed holds
  achieves the economic goal.
- **On-chain settlement / probabilistic micropayments (Livepeer-style).** Solves
  trustless payment, not truthful serving attribution, and imports consensus overhead
  the marketplace does not need while the platform remains the clearing house.

## Risks & Mitigations

- **Beacon coverage bias.** Client acknowledgments are lossy, opt-in for QoE, and absent
  for non-browser players. Mitigation: corroboration uses tolerance bands calibrated per
  delivery type in shadow mode, and the routing leg (resolver-signed, present for every
  session) carries most of the weight; beacons tighten, never solely decide.
- **Resolver/server collusion or self-resolution.** Where one operator is both resolver
  and server, routing evidence is self-produced. Mitigation: discrepancy analysis weights
  independent-resolver and client legs higher for such traffic; pluggability work
  (federation RFC) will further shift resolution authority and is tracked jointly.
- **Tenant–operator collusion (fake viewers).** Real clients replaying real sessions pass
  all evidence legs. Mitigation: economic (platform fee makes self-dealing negative-sum
  for revenue-share traffic) plus anomaly detection on session-shape distributions;
  explicitly documented as a residual risk.
- **Privacy.** Evidence records must not widen exposure of viewer identifiers; tokens
  keep the existing claim discipline (no viewer identity, content-scoped), and
  settlement tables carry buckets/aggregates, not raw IPs.
- **Money-movement bugs.** Payouts reuse the proven verified-before-value pattern; the
  batch invariant (ledger sum = rail amount, idempotent transitions) is enforced in the
  state machine, and shadow-mode reconciliation runs before the first real batch.
- **Held-revenue operator friction.** Fail-closed holds create support load. Mitigation:
  Phase 2 statements expose the exact discrepancy behind every hold, and the dispute flow
  is part of Phase 3 scope, not an afterthought.

## Migration / Rollout

1. **Phase 1 shadow.** Evidence emission + corroboration tables live; deltas computed
   and reported, no effect on accruals. Calibrate tolerance bands per meter and delivery
   type on first-party clusters.
2. **Phase 2 reporting.** Operator GraphQL + dashboard over the existing ledger,
   including shadow-mode discrepancy visibility. No money behavior change.
3. **Phase 1 enforcement.** Flip corroboration caps and held-status gating per cluster
   kind: `third_party_marketplace` first, behind a per-cluster flag.
4. **Phase 3 payouts.** Bank rail first with manual batch approval, then automated
   scheduling; stablecoin rail second. Settlement lag starts conservative (aligned to
   the reversal horizon) and shortens with reconciliation history.
5. Ledger schema is additive throughout (new tables, existing status vocabulary and
   `payout_batch_id` already provisioned); no destructive migration. On implementation,
   fold outcomes into `docs/architecture/cross-cluster-billing.md` /
   `payment-settlement.md` and delete this RFC per RFC policy.

## Open Questions

- Session-correlation id: reuse the resolve-time token as the correlation carrier, or
  introduce an explicit session id minted at resolve and threaded through Mist triggers?
  The latter is cleaner but touches the media plane.
- Per-cluster settlement keys: derive from the mesh/service identity chain
  (`token-authority.md`) or a dedicated Quartermaster-issued keypair? Depends on
  token-authority sequencing.
- Statement immutability: content-addressed snapshot rows vs. reproducible-query
  discipline. Snapshots are safer for disputes; decide against storage cost.
- Which corroboration tolerance is acceptable at launch per meter (viewer minutes vs.
  egress bytes vs. processing seconds)? Shadow mode answers this empirically.
- Stablecoin payout jurisdictional constraints — which operator regions can be offered
  the rail at all?

## References, Sources & Evidence

- [Source] `docs/architecture/cross-cluster-billing.md` — per-cluster attribution flow,
  fail-closed posture, ledger accrual from storage provider usage.
- [Source] `docs/architecture/routing-events-attribution.md` — routing decision
  attribution model and emission paths.
- [Source] `docs/architecture/payment-settlement.md` — verified-before-value settlement
  pattern reused for payouts.
- [Source] `docs/architecture/analytics-pipeline.md` — signed telemetry-token edge
  attribution and its trust boundary.
- [Evidence] `api_billing/internal/operator/credit.go` — accrual idempotency, platform
  fee policy, held/accruing gating on `cluster_owners`; package comment deferring payout
  batching to nonexistent tooling.
- [Evidence] `pkg/database/sql/schema/purser.sql` (`operator_credit_ledger`) and
  `pkg/database/sql/migrations/purser/v0.2.33/expand/015_operator_credit_clawback_reversal_links.sql`
  — status vocabulary, `payout_batch_id`, clawback/reversal linkage.
- [Evidence] `pkg/telemetrytoken/token.go` — existing signed serving-endpoint claims.
- [Reference] `docs/rfcs/federation-plane-pluggability.md` (parallel, in progress) —
  operator-local plane authority; source of the evidence-portability requirement.
- [Reference] `docs/rfcs/referral-attribution-network-usage.md` — acquisition
  attribution; complementary, non-overlapping.
- [Reference] `docs/rfcs/token-authority.md`,
  `docs/rfcs/service-identity-and-cluster-binding.md`, `docs/architecture/tls.md` —
  cluster identity and key derivation candidates.
- [Reference] `docs/platform-features.yaml` — `settlement-attribution` (roadmap) and
  `operator-credit-ledger` (partial, platform) registry items.
