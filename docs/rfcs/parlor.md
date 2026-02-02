# RFC: Parlor (Interactive Rooms)

## Status

Draft

## TL;DR

- Introduce a new service for persistent, tenant-owned rooms with realtime presence.
- Keep MVP small: rooms, participants, stage roles, and realtime updates.
- **Phase 2**: Viewer engagement economy (channel points, hype trains, leaderboards, flair).

## Current State (as of 2026-01-13)

- `api_rooms` is a stub only; no implementation exists.
- No GraphQL or gRPC surface for rooms.

Evidence:

- `api_rooms/README.md`

## Problem / Motivation

We need a lightweight, tenant-owned room primitive to support interactive experiences without coupling to streaming internals.

## Goals

- Durable rooms scoped to tenant.
- Realtime presence and role changes.
- Clean API surface (GraphQL + internal gRPC).

## Non-Goals (MVP / Phase 1)

- Full chat system.
- Moderation workflows.
- Economy or rewards in Phase 1 (see Phase 2 below).

## Proposal

### Phase 1: MVP (Room Primitives)

- Room CRUD.
- Participant join/leave + role updates.
- Presence events via Signalman.

### Phase 2: Viewer Engagement Economy

After MVP stabilizes, add viewer engagement features for loyalty and gamification.

#### Channel Points (Free Currency)

Viewers accrue points by watching. Redeemable for perks defined by the streamer.

```sql
CREATE TABLE parlor.viewer_points (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id UUID NOT NULL,
    viewer_id UUID NOT NULL,             -- From auth/session
    balance BIGINT DEFAULT 0,
    lifetime_earned BIGINT DEFAULT 0,
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(room_id, viewer_id)
);

CREATE TABLE parlor.point_transactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    viewer_points_id UUID REFERENCES parlor.viewer_points(id),
    amount BIGINT NOT NULL,              -- Positive = earn, negative = spend
    reason TEXT NOT NULL,                -- 'watch_minute', 'redemption', 'bonus', 'hype_train'
    metadata JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE parlor.point_rewards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id UUID NOT NULL,
    name TEXT NOT NULL,                  -- 'Highlight My Message'
    description TEXT,
    cost BIGINT NOT NULL,                -- 500 points
    action_type TEXT NOT NULL,           -- 'highlight_chat', 'custom_emote', 'streamer_action', 'unlock_emote'
    config JSONB,                        -- Action-specific config
    cooldown_seconds INT,                -- Per-viewer cooldown
    enabled BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
```

**Earning points:**

- 10 points per minute watched (configurable)
- Bonus for consecutive days (streak multiplier)
- Bonus during hype trains
- Bonus for participating in polls/predictions

**GraphQL:**

```graphql
type ViewerPoints {
  balance: Int!
  lifetimeEarned: Int!
}

type PointReward {
  id: ID!
  name: String!
  description: String
  cost: Int!
  actionType: String!
  cooldownSeconds: Int
}

extend type Query {
  myPoints(roomId: ID!): ViewerPoints
  availableRewards(roomId: ID!): [PointReward!]!
}

extend type Mutation {
  redeemReward(roomId: ID!, rewardId: ID!): RedeemRewardPayload!
}
```

#### Hype Trains

Collective goal that builds momentum from donations/subs/bits. Encourages community participation.

```sql
CREATE TABLE parlor.hype_trains (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id UUID NOT NULL,
    status TEXT DEFAULT 'inactive',      -- 'inactive', 'active', 'completed', 'expired'
    level INT DEFAULT 0,
    progress INT DEFAULT 0,              -- Current progress toward next level
    goal INT NOT NULL,                   -- Points needed for next level
    expires_at TIMESTAMPTZ,              -- Train expires if no activity
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

CREATE TABLE parlor.hype_contributions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hype_train_id UUID REFERENCES parlor.hype_trains(id),
    viewer_id UUID NOT NULL,
    contribution_type TEXT NOT NULL,     -- 'donation', 'subscription', 'bits', 'cheer'
    amount DECIMAL(10,2) NOT NULL,
    points_contributed INT NOT NULL,     -- Converted to train points
    created_at TIMESTAMPTZ DEFAULT NOW()
);
```

**Mechanics:**

- Train starts when first contribution received
- Contributions add points toward next level
- Train advances levels (visual progress bar on stream)
- Higher levels = bigger rewards for all viewers (bonus channel points, emotes)
- Train expires after N minutes of no activity (configurable, default 5 min)
- Reaching max level = celebration event

**Contribution conversion:**

- $1 donation = 100 hype points
- 1 subscription = 500 hype points
- Configurable per room

#### Leaderboards

Track and display top viewers across various metrics.

```sql
CREATE TABLE parlor.leaderboards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id UUID NOT NULL,
    type TEXT NOT NULL,                  -- 'donations', 'watch_time', 'points_spent', 'bits', 'gifted_subs'
    period TEXT NOT NULL,                -- 'all_time', 'monthly', 'weekly', 'stream'
    data JSONB NOT NULL,                 -- Cached rankings [{viewer_id, display_name, value, rank}]
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(room_id, type, period)
);
```

**Leaderboard types:**

- Top donors (all time, this month, this stream)
- Most watch time
- Most channel points spent
- Top cheerers
- Gifted sub leaderboard

**Update frequency:**

- Stream-scoped: Real-time
- Weekly/monthly: Hourly rollup
- All-time: Daily rollup

#### Viewer Flair

Badges and icons displayed next to viewer names in chat/presence.

```sql
CREATE TABLE parlor.viewer_flair (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id UUID NOT NULL,
    viewer_id UUID NOT NULL,
    flair_type TEXT NOT NULL,            -- 'subscriber', 'vip', 'moderator', 'top_donor', 'founder', 'custom'
    tier INT DEFAULT 1,                  -- For subscriber tiers (1, 2, 3)
    display_name TEXT,                   -- Badge name shown on hover
    icon_url TEXT,                       -- Badge image URL
    priority INT DEFAULT 0,              -- Display order (higher = first)
    expires_at TIMESTAMPTZ,              -- NULL = permanent
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(room_id, viewer_id, flair_type)
);
```

**Flair types:**

- `subscriber`: Paid subscriber badge (tiered)
- `vip`: VIP status granted by streamer
- `moderator`: Mod badge
- `top_donor`: Auto-assigned from leaderboard
- `founder`: Early supporter badge
- `custom`: Streamer-defined custom badges

#### Event Sync with Stream Delay

Events (donations, redemptions, hype train updates) must sync with stream delay so viewers see alerts at the right time.

**Solution:** Events have two timestamps:

- `occurred_at`: When the action happened (real time)
- `display_at`: When to show on stream (`occurred_at + stream_delay`)

```sql
ALTER TABLE parlor.point_transactions ADD COLUMN display_at TIMESTAMPTZ;
ALTER TABLE parlor.hype_contributions ADD COLUMN display_at TIMESTAMPTZ;
```

The overlay/player waits until `display_at` to render the event, keeping alerts in sync with what viewers see on their delayed stream.

**Stream delay source:** From Foghorn/MistServer stream metadata.

#### Viewer Games (Future)

Placeholder for interactive viewer games:

- Marble runs (!play to join)
- Predictions/betting with channel points
- Trivia with point rewards

Defer to Phase 3.

## Impact / Dependencies

**Phase 1:**

- New service `api_rooms`.
- Bridge GraphQL schema.
- Signalman for realtime presence.

**Phase 2:**

- Parlor schema extensions (points, rewards, hype trains, leaderboards, flair).
- Foghorn integration (stream delay for event sync).
- Purser integration (if donations/subs tied to billing).
- Player/overlay SDK (render events at correct time).

## Alternatives Considered

- Embed room state inside existing services (Bridge/Signalman).
- Use third-party room providers.

## Risks & Mitigations

- Risk: scope creep. Mitigation: strict MVP and non-goals.
- Risk: realtime scalability. Mitigation: Signalman-backed presence.

## Migration / Rollout

1. Implement room core (CRUD + presence).
2. Add client subscriptions.
3. Expand to additional features if needed.

## Open Questions

**Phase 1:**

- Should rooms exist without an associated stream by default?
- How should room permissions be modeled (role vs ACL)?

**Phase 2:**

- How are channel points earned cross-platform (web vs mobile vs embedded)?
- Should hype train levels/goals be configurable per room?
- How to handle point balance disputes or refunds?
- Should leaderboards be public or opt-in per viewer?
- How to integrate with paid subscriptions from Purser?

## References, Sources & Evidence

- `api_rooms/README.md`
- `api_realtime/`
- `pkg/graphql/schema.graphql`
- [Reference] Industry patterns for viewer loyalty programs and gamification
