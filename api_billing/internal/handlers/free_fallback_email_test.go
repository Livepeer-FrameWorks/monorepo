package handlers

import (
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/subscriptions"
)

// The grace notice's "Update payment" button lands on the billing page's
// payment action, not the top of the page.
func TestGraceNoticeLinksToThePaymentAction(t *testing.T) {
	appconfigtest.Set(t, "WEBAPP_PUBLIC_URL", "https://app.example.test")
	grace := freeFallbackActionURL(FreeFallbackNotice{Reason: subscriptions.FallbackPaymentFailed})
	if grace != "https://app.example.test/account/billing#update-payment" {
		t.Fatalf("grace notice links to %q", grace)
	}
	if moved := freeFallbackActionURL(FreeFallbackNotice{Reason: subscriptions.FallbackPaymentFailed, Moved: true}); moved != "https://app.example.test/account/billing" {
		t.Fatalf("move notice links to %q", moved)
	}
}

func TestFreeFallbackNoticesRender(t *testing.T) {
	appconfigtest.Set(t, "WEBAPP_PUBLIC_URL", "https://app.example.test")
	moveAt := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	es := &EmailService{}
	for _, c := range []struct {
		notice FreeFallbackNotice
		want   string
	}{
		{FreeFallbackNotice{Reason: subscriptions.FallbackPaymentFailed, MoveAt: moveAt}, "November 13, 2026"},
		{FreeFallbackNotice{Reason: subscriptions.FallbackPaymentFailed, MoveAt: moveAt, Moved: true}, "final invoice covers your usage up to the move"},
		{FreeFallbackNotice{Reason: subscriptions.FallbackCancelled, MoveAt: moveAt, Moved: true}, "Your cancellation is complete"},
	} {
		templateName, subject := freeFallbackTemplate(c.notice)
		if subject == "" {
			t.Fatalf("%s has no subject", templateName)
		}
		body, err := es.renderBillingTemplate(templateName, EmailData{TenantName: "Acme", DueDate: c.notice.MoveAt, LoginURL: "https://app.example.test/account/billing"})
		if err != nil {
			t.Fatalf("%s: %v", templateName, err)
		}
		if !strings.Contains(body, c.want) {
			t.Fatalf("%s does not say %q", templateName, c.want)
		}
	}
}
