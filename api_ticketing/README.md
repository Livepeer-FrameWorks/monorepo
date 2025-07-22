# Ticketing Service (Deckhand)

> **Status**: 🚧 **Planned** - Microservice not yet implemented

Deckhand is FrameWorks' support and ticketing system API, providing comprehensive 
customer support management, internal issue tracking, and knowledge base functionality.

## Build vs Buy Decision

Integrate with existing ticketing solutions or roll a basic custom microservice:

- **Use Existing Solutions**
  - Zammad (open source, self-hosted)
  - OSTicket (open source, simple)
  - Freshdesk (SaaS, full-featured)
  - Email piping (lightweight option)

- **Why Use Existing?**
  - Ticketing systems are feature-heavy
  - Many proven solutions exist
  - Common features needed:
    - Email integration
    - SLA tracking
    - Knowledge base
    - Agent routing
    - Reporting
    - Mobile apps
    - API access

## Integration Points

- User authentication
- Ticket creation/updates
- Status synchronization
- Analytics integration
- Email notifications
