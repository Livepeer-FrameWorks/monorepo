# Forms Service

Barebones API to deal with insecure form submissions. Should look into integrating third party forms providers.

## Status
- Minimal utility; consider third‑party integration later. Useful for demos and contact forms.

## Run (dev)
- From repo root: `docker-compose up -d`
- Or run just the service: `cd api_forms && npm install && npm run dev`

Configuration: copy `env.example` to `.env` and use the inline comments as reference. Do not commit secrets.

## Build vs Buy Decision

This service can be either built in-house or use third-party providers:

- **Initial Options**
  - Third-party services:
    - Formspree
    - Netlify Forms
    - Other form backends
  - Custom implementation (viable choice)

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
