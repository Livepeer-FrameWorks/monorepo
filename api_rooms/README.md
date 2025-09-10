# Parlor (Interactive Room Service)

Status: Planned — tracked on the roadmap. Not implemented yet.

## Overview

Parlor provides persistent, interactive rooms that serve as the social and economic layer of the platform. Rooms are multi-purpose spaces owned by tenants (streamers/creators) that can host various activities: live streams, group video calls, games, predictions, and community economies.

## Core Concepts

**Rooms as Tenant Spaces**: Each tenant (creator) has a primary room that persists 24/7, building community and enabling engagement whether streaming or not.

**Room Economy**: Rooms have their own credit system for predictions, rewards, and interactive features.

## Room Capabilities (Planned)

### Communication
- **Text Chat** - Persistent messaging with moderation
- **TTS/Bots** - Automated participants and text-to-speech     
- **Group Calls** - Zoom-like multi-participant video (participants can become co-hosts)
- **Broadcast Mode** - Restream group calls to wider audience
- **Primary Stream** - Main broadcast in room

### Interactive Features  
- **Predictions** - Viewers bet channel credits on outcomes
- **Polls & Voting** - Community decisions
- **Games** - Mini-games, trivia, tournaments
- **Rewards** - Channel point redemptions
- **Collaborative Canvas** - Draw together, shared whiteboards

### Economic System
- **Channel Credits** - Per-room currency earned by watching/participating
- **Predictions Market** - Bet on stream outcomes
- **Rewards Store** - Redeem credits for perks
- **Tips/Donations** - Direct support with credit bonuses

### Stream Integration
- **Primary Stream** - Main broadcast in room
- **Co-streaming** - Multiple hosts broadcasting together
- **Guest Spots** - Elevate viewers to video participants
- **Stage/Audience** - Zoom-like presenter mode

## Room Hierarchy

```
Tenant (Creator Account)
  └── Primary Room (24/7 persistent)
       ├── Channel Credits System
       ├── Subscriber Perks
       ├── Moderation Rules
       └── Sub-Rooms (optional)
            ├── VIP Lounge
            ├── Game Room
            └── Voice Hangout
```

## Use Cases

### 1. **Zoom-Like Broadcasting**
```
Scenario: Creator wants to do a talk show with guests
1. Start room in "Stage Mode"
2. Invite 3 guests to video call
3. Guests join with video/audio
4. Enable "Broadcast to Audience"
5. Thousands watch the group call
6. Audience participates via chat
7. Can promote audience member to stage
```

### 2. **Twitch-Style Predictions**
```
Scenario: Gaming stream with predictions
1. Viewer earns 100 credits/hour watching
2. Streamer starts prediction: "Will I beat this boss?"
3. Viewers bet credits on YES/NO
4. Outcome determines credit redistribution
5. Winners get proportional payout
6. Credits unlock channel rewards
```

### 3. **Hybrid Events**
```
Scenario: Conference with breakout rooms
1. Main stage room with keynote
2. Breakout rooms for discussions
3. Participants can move between rooms
4. Some rooms broadcast, others private
5. Credits for attending sessions
```

### 4. **Community Economy**
```
Scenario: 24/7 community with engagement rewards
1. Room exists even when creator offline
2. Active chatters earn credits
3. Play games to earn/bet credits
4. Redeem for: VIP status, emotes, song requests
5. Creator sets reward tiers and perks
```

## Technical Architecture

### Room Ownership Model
```go
type Room struct {
    ID         string
    TenantID   string  // Owner/creator
    ParentRoom *string // For sub-rooms
    RoomType   string  // primary|sub|event|temporary
    
    // Economic state
    Credits    CreditsConfig
    Rewards    []Reward
    
    // Capabilities
    Features   map[string]bool
    
    // Persistent state
    State      RoomState
    Metadata   map[string]interface{}
}

type CreditsConfig struct {
    Enabled        bool
    EarnRate       float64  // Credits per hour
    StartingAmount int
    Predictions    []ActivePrediction
    RewardsStore   []RewardTier
}
```

### Broadcast Modes
```go
type BroadcastMode string

const (
    ModeSolo      = "solo"       // Traditional single streamer
    ModeStage     = "stage"      // Multiple presenters to audience  
    ModeGroup     = "group"      // Equal participants (Zoom-like)
    ModeHybrid    = "hybrid"     // Mix of presenters and viewers
)
```

### Integration Points

- **Commodore**: Room permissions, credit balances
- **Quartermaster**: Billing for premium room features
- **Helmsman**: Participant tracking, credit earning
- **Signalman**: Real-time credit updates, predictions
- **MistServer**: Restreaming group calls
- **PostgreSQL**: Credit ledger, prediction history
- **ClickHouse**: Engagement analytics, credit flow

## Database Schema (Planned)

```sql
-- Core room with tenant ownership
CREATE TABLE rooms (
    id UUID PRIMARY KEY,
    tenant_id UUID REFERENCES tenants(id),
    parent_room_id UUID REFERENCES rooms(id),
    room_type VARCHAR(50),
    broadcast_mode VARCHAR(50),
    created_at TIMESTAMP
);

-- Channel credits system
CREATE TABLE channel_credits (
    tenant_id UUID,
    user_id UUID, 
    balance BIGINT,
    total_earned BIGINT,
    total_spent BIGINT,
    PRIMARY KEY (tenant_id, user_id)
);

-- Predictions
CREATE TABLE predictions (
    id UUID PRIMARY KEY,
    room_id UUID REFERENCES rooms(id),
    question TEXT,
    options JSONB,
    total_pot BIGINT,
    outcome VARCHAR(100),
    resolved_at TIMESTAMP
);

-- Credit transactions
CREATE TABLE credit_transactions (
    id UUID PRIMARY KEY,
    tenant_id UUID,
    user_id UUID,
    amount BIGINT,
    transaction_type VARCHAR(50), -- earn|spend|bet|win
    reference_id UUID, -- prediction_id, reward_id, etc
    created_at TIMESTAMP
);
```

## Build vs Buy Decision

### For Text Chat Component:
- **Consider Existing**: Matrix, Discord widgets
- **Build Custom When**: Deep stream integration needed

### For Room Orchestration:
- **Must Build Custom**: Unique to our platform
- **Leverage WebRTC**: For P2P voice/video

### For Credits/Economy:
- **Build Custom**: Core differentiator
- **Integrate Payment**: For credit purchases

## Monetization Opportunities

1. **Premium Rooms**: Higher participant limits, advanced features
2. **Credit Packages**: Buy channel credits directly
3. **Prediction Fees**: Platform takes % of prediction pots
4. **Branded Rooms**: Custom branding, white-label
5. **API Access**: Developers build on room platform

## Future Roadmap

### Phase 1: Foundation
- Basic rooms with chat
- Tenant ownership model
- Simple credit system

### Phase 2: Broadcasting
- Group video calls
- Restream to audience
- Stage/audience modes

### Phase 3: Economy
- Full credits system
- Predictions
- Rewards store

### Phase 4: Advanced
- Sub-rooms
- Room templates
- API/SDK
- Mobile apps
