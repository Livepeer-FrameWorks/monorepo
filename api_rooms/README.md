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

### Broadcast Modes

- **Solo**: Traditional single streamer
- **Stage**: Multiple presenters to audience
- **Group**: Equal participants (Zoom-like)
- **Hybrid**: Mix of presenters and viewers
