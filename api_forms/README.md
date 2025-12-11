# Forms Service

Minimal contact API for handling contact form submissions. Go service using pkg/ infrastructure.

## Run (dev)
```bash
cd api_forms
go mod download
go run ./cmd/forms
```

Configure the marketing site to point `VITE_CONTACT_API_URL` to this service.

## Configuration

Environment variables (see `env.example`):

- `PORT` - Service port (default: 18032)
- `TURNSTILE_FORMS_SECRET_KEY` - Cloudflare Turnstile verification. Use test secret `1x0000000000000000000000000000000AA` for local development.
- `ALLOWED_ORIGINS` - Comma-separated list of allowed origins for CORS
- `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD` - Email configuration
- `FROM_EMAIL` - Sender email address
- `TO_EMAIL` - Recipient for contact form submissions
- `LISTMONK_URL`, `LISTMONK_USERNAME`, `LISTMONK_PASSWORD` - Connection to Listmonk (Newsletter)
- `DEFAULT_MAILING_LIST_ID` - ID of the Listmonk list to subscribe users to (default: 1)

## Endpoints

- `POST /api/contact`: Send contact email.
- `POST /api/subscribe`: Subscribe email to newsletter (Listmonk).

## Build

```bash
cd api_forms
CGO_ENABLED=0 go build -o forms ./cmd/forms
```

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
