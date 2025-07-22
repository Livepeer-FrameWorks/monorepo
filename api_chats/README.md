# Chat System Service (Messenger)

> **Status**: 🚧 **Planned** - Microservice not yet implemented

## Build vs Buy Decision

Integrate with existing chat solutions or roll a basic custom microservice:

- **Use Existing Solutions**
  - Matrix (open protocol, federated)
  - Discord widget integration
  - Other off-the-shelf chat platforms

- **When to Consider Custom**
  - In-stream chat is a core product differentiator
  - Deep integration with streaming
  - Custom moderation tools are essential

- **Why Use Existing?**
  - Chat systems are complex
  - Moderation tools
  - Existing solutions handle:
    - Message persistence
    - User presence
    - Rate limiting
    - Moderation tools
    - Mobile push notifications

## Integration Points

- User authentication
- Stream/room mapping
- Moderation interface
- Analytics integration
- Mobile notifications
