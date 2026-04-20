# RFC: Creator Commerce (Live Shopping & Auctions)

## Status

Draft

## TL;DR

- Introduce a new `creator commerce` product category for FrameWorks, starting with QVC-style live shopping and timed auctions.
- Build on existing streaming, realtime, multistreaming, analytics, and billing primitives; do not overload `DayDream` or tenant billing for shopper commerce.
- Split the domain into two new services: `api_commerce` for catalog/cart/order flows and `api_auctions` for server-authoritative auction mechanics.
- Treat `DayDream` as an optional presentation layer for AI-enhanced selling experiences, not the system of record.

## Current State

FrameWorks already provides strong media and platform primitives:

- Live ingest, delivery, and orchestration via Foghorn, Helmsman, MistServer, and Livepeer Gateway.
- Realtime delivery via Signalman and GraphQL subscriptions.
- Multistreaming to external destinations through Commodore-managed push targets.
- Analytics and event fanout through Decklog, Kafka, Periscope Ingest, and Periscope Query.
- Platform billing rails in Purser for tenant subscriptions, prepaid balance top-ups, and x402 payments.

What does not exist today:

- No commerce domain for products, SKUs, carts, orders, taxes, shipping, or refunds.
- No auction domain for lots, bids, reserve prices, anti-sniping, winner settlement, or payment holds.
- No live-shopping presenter tools such as pinned products, timed drops, featured items, or replay-to-product linkage.
- No audience interaction room/chat system for live retail events; `api_rooms` is still an RFC/stub.
- `DayDream` exists only as a coming-soon navigation placeholder for live video generative effects.

Evidence:

- `README.md`
- `docs/architecture/multistreaming.md`
- `docs/architecture/service-events.md`
- `docs/architecture/agent-access.md`
- `website_application/src/lib/navigation.ts`
- `docs/rfcs/parlor.md`

## Problem / Motivation

FrameWorks can already power the video side of a live commerce product, but it has no product-side system for selling through live video.

That gap blocks a set of adjacent product opportunities:

- QVC-style hosted product showcases.
- Influencer-led product drops.
- Real-time flash sales and limited inventory events.
- Single-seller live auctions.
- Future multi-seller marketplace workflows.

Calling this category `creator monetization` is too narrow. Monetization is only one outcome. The real domain includes:

- Catalog management.
- Merchandising.
- Transactional commerce.
- Auction rules and dispute handling.
- Seller operations.
- Buyer experience.

For that reason, this RFC uses `creator commerce` as the category. `Creator monetization` remains a related umbrella for tips, subscriptions, sponsorships, and pay-per-view, but it is not specific enough for this proposal.

## Goals

- Define a first-class `creator commerce` product area in FrameWorks.
- Support hosted live shopping with pinned products, timed offers, and in-stream purchase flows.
- Support server-authoritative live auctions with low-latency bid updates and clear winner determination.
- Reuse existing FrameWorks primitives for streaming, realtime, analytics, and operator tooling.
- Keep service boundaries clean and consistent with the monorepo architecture.
- Preserve strict tenant isolation for all commerce and auction data.

## Non-Goals

- Replacing Purser with a general consumer payments platform.
- Supporting a full multi-seller marketplace in the first release.
- Building warehouse management, ERP sync, or fulfillment automation in v1.
- Making `DayDream` the core commerce engine.
- Building a full social/chat platform in this RFC.

## Proposal

Introduce a new product capability called `creator commerce`, delivered in phases.

### Category

Recommended category: `creator commerce`

Sub-capability in this RFC:

- `live shopping`
- `live auctions`

Adjacent but out of scope for this RFC:

- tips and donations
- memberships and subscriptions
- paid chat / paid questions
- pay-per-view
- sponsorship marketplaces

### Product Model

The initial creator commerce experience should support:

- A seller runs a live stream.
- The seller or producer pins products during the broadcast.
- Viewers can open a product drawer, add items to cart, and complete checkout without leaving the event flow.
- Optional timed drops expose price windows, countdowns, or limited stock states.
- Auction events allow a host to open a lot, accept bids in real time, and close on a server-defined winner.

This should be framed as a commerce layer attached to streams, not embedded inside the media pipeline itself.

### Architecture

Add two new control-plane services:

#### `api_commerce`

Owns:

- products
- variants / SKUs
- collections
- inventory snapshots
- pinned products / featured products per stream
- carts
- orders
- checkout sessions
- promotions

Responsibilities:

- Persist catalog and transactional commerce state in Postgres.
- Expose gRPC APIs to Bridge.
- Emit service events for order/cart/catalog lifecycle changes.
- Integrate with external PSPs for shopper payments.
- Integrate with Chandler for product media assets.

#### `api_auctions`

Owns:

- auctions
- lots
- bid increments
- reserve prices
- bid ledger
- anti-sniping rules
- winner selection
- settlement state

Responsibilities:

- Maintain the authoritative clock and bid acceptance logic.
- Persist all auction decisions and provide an audit trail.
- Publish real-time bid and lot state updates through Signalman-compatible event flows.
- Coordinate with `api_commerce` for product references and with payment providers for pre-authorization or collection.

### Why separate services

Commerce transactions and auction mechanics have different correctness profiles:

- Commerce is CRUD-heavy and integration-heavy.
- Auctions are timing-sensitive, state-machine-heavy, and dispute-prone.

Keeping them separate reduces coupling and lets auction logic evolve without contaminating basic cart/order flows.

### Existing services reused

- `api_gateway` for GraphQL aggregation and auth.
- `api_realtime` for subscriptions and viewer-side updates.
- `api_firehose` and Kafka for service event fanout.
- `api_analytics_ingest` and `api_analytics_query` for event analysis and reporting.
- `api_assets` for product imagery and video snippets.
- `api_balancing` / `api_sidecar` / MistServer / Livepeer Gateway for the live media path.

### Explicit non-reuse: Purser

Purser should remain the platform billing system for:

- tenant subscriptions
- prepaid balances
- internal usage accounting
- x402 top-ups

It should not become the consumer checkout/order ledger for live shopping.

Reason:

- shopper orders, taxes, refunds, shipping, and PSP workflows are a different domain from tenant infrastructure billing
- mixing them would blur service ownership and create fragile accounting semantics

### DayDream positioning

`DayDream` should be treated as an enhancement layer for creator commerce, not its category or system boundary.

Possible future `DayDream` integrations:

- AI-generated product spotlight scenes
- background replacement for retail segments
- virtual set changes between lots
- auto-generated promo bumpers
- live video stylization for product demos

This RFC does not require `DayDream` to launch creator commerce.

### Data Model Direction

#### Commerce core

Indicative entities:

- `products`
- `product_variants`
- `product_media`
- `inventory_levels`
- `stream_merchandising_state`
- `carts`
- `cart_items`
- `orders`
- `order_items`
- `checkout_sessions`
- `promotions`

#### Auction core

Indicative entities:

- `auctions`
- `auction_lots`
- `bids`
- `bid_rejections`
- `auction_extensions`
- `auction_winners`
- `auction_settlements`

All tables must be tenant-scoped. Any query path must filter by `tenant_id`.

### API Surface

Bridge GraphQL should expose:

- seller-side mutations for catalog, merchandising, and auction control
- buyer-side queries for visible products, lots, availability, and order status
- subscriptions for pinned product changes, inventory changes, bid updates, lot state, and winner announcements

Internal service-to-service APIs should remain gRPC-first.

### Realtime Model

Use Signalman-backed subscriptions for:

- product pinned / unpinned
- featured item changes
- inventory state changes relevant to the live event
- bid accepted / outbid / lot extended / lot closed
- order confirmation and checkout state transitions where appropriate

Auction acceptance must remain server-authoritative. The client may optimistically render activity, but the accepted bid stream is the source of truth.

### MVP Phasing

#### Phase 1: Live Shopping MVP

- single seller per tenant
- product catalog
- product pinning per stream
- simple stock tracking
- shopper checkout via external PSP
- orders and basic order history
- realtime product state updates

#### Phase 2: Timed Drops

- countdown-based drops
- limited inventory windows
- scheduled merchandising changes
- event-linked offers and overlays

#### Phase 3: Live Auctions

- host-controlled lot lifecycle
- server-authoritative bids
- reserve price support
- minimum increment support
- anti-sniping extension windows
- winner and settlement state

#### Phase 4: Expansion

- replay commerce
- richer moderation
- seller analytics
- affiliate/referral hooks
- deeper `DayDream` presentation tooling

## Impact / Dependencies

- New service `api_commerce`
- New service `api_auctions`
- Bridge GraphQL schema additions
- New protobuf definitions under `pkg/proto`
- New Postgres schema sections for commerce and auctions
- Signalman subscription additions
- Decklog / service-events additions for commerce and auction lifecycles
- Svelte application surfaces for seller console and buyer-facing live commerce UI
- PSP integration for shopper checkout

Potential future dependency:

- `api_rooms` / Parlor for richer audience interaction, chat, and presenter-room mechanics

## Alternatives Considered

### Call it `creator monetization`

Rejected because it is too broad in one direction and too narrow in another. It includes revenue outcomes but not the operational commerce domain.

### Put commerce inside Purser

Rejected because shopper commerce and tenant infrastructure billing are materially different accounting and lifecycle domains.

### Make DayDream the live shopping product

Rejected because `DayDream` is currently an AI effects concept, not the system of record for catalog, checkout, or auction state.

### Build auctions inside `api_commerce`

Possible for an MVP, but not preferred. Auction correctness, timing, and dispute handling justify a separate bounded context.

## Risks & Mitigations

- Risk: scope explosion into full marketplace tooling.
  Mitigation: keep v1 single-seller and stream-attached.

- Risk: auction latency or race conditions create trust problems.
  Mitigation: server-authoritative bid acceptance, monotonic event ordering, explicit audit logs, anti-sniping rules.

- Risk: billing/accounting confusion if shopper and tenant money flows mix.
  Mitigation: keep Purser separate from commerce order flows.

- Risk: missing interaction primitives reduce engagement.
  Mitigation: make live shopping MVP work without Parlor; integrate richer room/chat later.

- Risk: tax, shipping, and compliance work dominates the roadmap.
  Mitigation: use a constrained PSP/integration model and avoid multi-seller marketplace requirements in v1.

## Migration / Rollout

1. Define `creator commerce` as a product category in roadmap and navigation planning.
2. Implement `api_commerce` with catalog, merchandising, carts, checkout sessions, and orders.
3. Add buyer-facing and seller-facing GraphQL surfaces in Bridge.
4. Add Signalman-backed subscriptions for merchandising and commerce event updates.
5. Launch single-seller live shopping MVP.
6. Add timed drops and event merchandising controls.
7. Implement `api_auctions` and launch host-controlled live auctions.
8. Integrate optional `DayDream` enhancements after commerce fundamentals are stable.

## Open Questions

- Should buyer identities be guest-first, account-first, or both for the MVP?
- Do we want shopper checkout embedded in FrameWorks UI, hosted by the PSP, or both?
- Should auction winners require pre-authorization before bidding?
- How much inventory truth should live in FrameWorks versus an external commerce backend?
- Does replay commerce belong in the same RFC or a follow-up RFC?
- How should moderation work for bidding abuse, auction cancellation, and seller disputes?
- Should Parlor become a hard dependency before auction launch, or remain optional?

## References, Sources & Evidence

- [Evidence] `website_application/src/lib/navigation.ts`
- [Evidence] `README.md`
- [Evidence] `docs/architecture/multistreaming.md`
- [Evidence] `docs/architecture/service-events.md`
- [Evidence] `docs/architecture/agent-access.md`
- [Reference] `docs/rfcs/parlor.md`
- [Reference] `docs/rfcs/stream-balances.md`
