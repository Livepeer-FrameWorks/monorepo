// Package notify emails a tenant's billing contact when Bosun disables one of
// the tenant's webhook endpoints automatically.
package notify

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"strings"

	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BillingContacts returns a tenant's billing details.
type BillingContacts interface {
	GetBillingDetails(ctx context.Context, tenantID string) (*purserpb.BillingDetails, error)
}

// Mailer sends one email.
type Mailer interface {
	SendMail(ctx context.Context, to, subject, htmlBody string) error
}

// Dispatcher is the pkg/outbox dispatcher of webhook_notification_outbox.
type Dispatcher struct {
	Contacts BillingContacts
	// Mailer is nil when SMTP is not configured; every notification then
	// fails and stays queued.
	Mailer   Mailer
	Branding config.EmailBranding
	Logger   logging.Logger
}

// Dispatch emails the billing contact. A tenant without a billing record or
// email has no one to notify; the notification completes and the dashboard
// still shows the disabled endpoint.
func (d *Dispatcher) Dispatch(ctx context.Context, n ledger.Notification) ([]string, error) {
	if d.Mailer == nil {
		return nil, errors.New("SMTP is not configured")
	}
	details, err := d.Contacts.GetBillingDetails(ctx, n.TenantID)
	if status.Code(err) == codes.NotFound || (err == nil && strings.TrimSpace(details.GetEmail()) == "") {
		if d.Logger != nil {
			d.Logger.WithFields(logging.Fields{"tenant_id": n.TenantID, "endpoint_id": n.EndpointID}).
				Warn("Webhook endpoint disabled; the tenant has no billing email to notify")
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up billing contact: %w", err)
	}
	subject, body, err := Render(n, d.Branding)
	if err != nil {
		return nil, err
	}
	if err := d.Mailer.SendMail(ctx, details.GetEmail(), subject, body); err != nil {
		return nil, fmt.Errorf("send endpoint disabled email: %w", err)
	}
	return nil, nil
}

type disabledEmail struct {
	EndpointURL string
	Link        string
}

const disabledTemplate = `<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px;">FrameWorks disabled your webhook endpoint after every delivery to it failed for at least 5 days.</p>
<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px; word-break:break-all;"><strong>{{.EndpointURL}}</strong></p>
<p style="margin:0 0 20px; color:#24283b; font-size:15px; line-height:23px;">Deliveries waiting for it were skipped. Fix the endpoint, enable it again, and replay the skipped deliveries from the delivery log if you need them.</p>
{{template "action" action}}`

// Render returns the subject and HTML body of a notification.
func Render(n ledger.Notification, branding config.EmailBranding) (string, string, error) {
	link := strings.TrimRight(branding.WebAppURL, "/") + "/developer/webhooks/" + n.EndpointID
	if strings.TrimSpace(branding.WebAppURL) == "" {
		link = ""
	}
	subject := "Your webhook endpoint was disabled"
	content := disabledEmail{EndpointURL: n.EndpointURL, Link: link}
	body, err := email.RenderLayout(email.LayoutData{
		LogoURL:      branding.Logo(),
		Preheader:    "Deliveries to " + n.EndpointURL + " kept failing.",
		Eyebrow:      "Webhooks",
		Title:        "Webhook endpoint disabled",
		SupportEmail: branding.Support(),
		Content:      content,
	}, disabledTemplate, template.FuncMap{
		"action": func() email.Action {
			return email.Action{URL: link, Label: "Open webhooks"}
		},
	})
	return subject, body, err
}
