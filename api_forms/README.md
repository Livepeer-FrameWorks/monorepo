# Forms Service

Minimal contact API used for demos. Not part of the dev docker‑compose stack.

Status
- Minimal utility; evaluate third‑party providers vs. in‑house.

## Run (dev)
- Run the service directly: `cd api_forms && npm install && npm run dev`
- Configure the marketing site to point `VITE_CONTACT_API_URL` to this service.

Configuration: copy `env.example` to `.env` and use the inline comments as reference. Do not commit secrets.

## Build vs Buy Considerations

This service can be either built in-house or use third-party providers:

- **Initial Options**
  - Third-party services:
    - Formspree
    - Netlify Forms
    - Other form backends
  - Custom implementation (viable, small scope)

- **Why Building is OK**
  - Low implementation effort
  - Avoids third-party lock-in
  - Common requirements:
    - Form validation
    - Spam prevention
    - File uploads
    - Email notifications
    - API access

- **Implementation Scope**
  Keep it focused:
  - Form schema validation
  - Submission handling
  - Basic spam protection
  - File upload support
  - Email notifications
  - Simple analytics

## Integration Points

- Frontend form components
- Email service
- File storage
- Analytics tracking
- Spam prevention
