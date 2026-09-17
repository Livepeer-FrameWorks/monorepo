# Email standards

FrameWorks sends transactional mail through the shared `pkg/email` package. Service-owned templates keep their content near the service, while the shared package owns presentation and SMTP message construction.

## Brand presentation

- Use `email.RenderLayout` for Go-owned HTML email.
- Use the FrameWorks light raster logomark served by the web application. `email.PublicLogoURL` derives its URL from `WEBAPP_PUBLIC_URL`; `EMAIL_LOGO_URL` can override it when assets are hosted separately.
- Use `#0f4b6e` as the primary brand color. Red, amber, and green are reserved for meaningful error, warning, and success states.
- Keep critical information in text. Images are decorative because many clients block remote images by default.
- Use the display name `FrameWorks`. The SMTP envelope address remains configured through `FROM_EMAIL`.

## Content and accessibility

- Dynamic values must be rendered by `html/template`. Never concatenate user-controlled HTML.
- Give every message a useful subject, preheader, heading, and support address.
- Account verification and password reset messages must state the expiry, explain what to do when the request was unexpected, and show a copyable fallback URL.
- Account bearer tokens belong in the action URL fragment, not its query string. The web application must copy the token into memory, remove it from browser history immediately, and submit it to the API in a request body.
- Buttons must use descriptive labels such as “Verify email address” or “View invoice.”
- The shared sender emits `multipart/alternative` with plain-text and HTML parts. Provide an explicit text body when precise wording matters; otherwise `SendMail` derives one from the HTML.
- Set `Reply-To` to the appropriate support mailbox for customer-facing messages when using `email.Message`.

## Configuration

| Variable                                               | Purpose                                                              | Default behavior                                               |
| ------------------------------------------------------ | -------------------------------------------------------------------- | -------------------------------------------------------------- |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD` | SMTP transport                                                       | STARTTLS is required by the shared sender                      |
| `SMTP_ALLOW_INSECURE`                                  | Explicit development/test escape hatch for a trusted plaintext relay | `false`; never enable in production                            |
| `FROM_EMAIL`                                           | Envelope and header mailbox                                          | `noreply@frameworks.network` where a service defines a default |
| `FROM_NAME`                                            | Human-readable sender                                                | `FrameWorks`                                                   |
| `WEBAPP_PUBLIC_URL`                                    | Public application base URL and default logo origin                  | Required for account action links                              |
| `EMAIL_LOGO_URL`                                       | Optional absolute raster logo URL                                    | `<WEBAPP_PUBLIC_URL>/frameworks-light-logomark.png`            |
| `SUPPORT_EMAIL`                                        | Footer and reply mailbox                                             | `support@frameworks.network`                                   |

Production values must be absolute public HTTPS URLs without user information, queries, or fragments. HTTP is accepted only for loopback development. Do not use container hostnames in messages.

Public password-reset, verification, and resend endpoints use isolated per-IP rate-limit buckets. Password reset also has a persistent per-account cooldown, and recovery CAPTCHA tokens are mandatory whenever Turnstile is configured. All account-state variants return the same public response.

## Ownership

The shared layout currently covers:

- Commodore account verification and password reset
- Purser invoices, payment updates, overdue reminders, and suspension notices
- Skipper investigation, infrastructure, and social-draft messages
- Lookout incident mail
- Steward contact-form intake

Listmonk campaigns, Chatwoot notifications, and Alertmanager notifications use external template engines. Configure their sender identity and templates in those systems; do not duplicate their templates in `pkg/email`.

## Verification

Template tests should check brand structure, required action text, URL handling, and HTML escaping. SMTP tests should parse the message and verify both MIME alternatives. Run the service-specific Makefile test target rather than invoking `go test` directly.
