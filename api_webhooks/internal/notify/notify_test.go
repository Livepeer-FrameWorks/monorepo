package notify

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type contacts struct {
	details *purserpb.BillingDetails
	err     error
}

func (c contacts) GetBillingDetails(context.Context, string) (*purserpb.BillingDetails, error) {
	return c.details, c.err
}

type mailer struct {
	to, subject, body string
	err               error
}

func (m *mailer) SendMail(_ context.Context, to, subject, body string) error {
	m.to, m.subject, m.body = to, subject, body
	return m.err
}

var disabled = ledger.Notification{ID: "n1", TenantID: "t1", EndpointID: "ep1", Kind: "endpoint_disabled", EndpointURL: "https://hooks.example.com/in"}

func TestDispatchEmailsTheBillingContact(t *testing.T) {
	m := &mailer{}
	d := &Dispatcher{Contacts: contacts{details: &purserpb.BillingDetails{Email: "billing@example.com"}}, Mailer: m, Branding: config.EmailBranding{WebAppURL: "https://app.example.com/"}}
	if _, err := d.Dispatch(context.Background(), disabled); err != nil {
		t.Fatal(err)
	}
	if m.to != "billing@example.com" || !strings.Contains(m.body, "https://hooks.example.com/in") || !strings.Contains(m.body, "https://app.example.com/developer/webhooks/ep1") {
		t.Fatalf("email to %q: %s", m.to, m.body)
	}
}

func TestDispatchFailuresAndNoContact(t *testing.T) {
	if _, err := (&Dispatcher{Contacts: contacts{}}).Dispatch(context.Background(), disabled); err == nil {
		t.Fatal("dispatch without SMTP succeeded; the notification would be lost")
	}
	m := &mailer{err: errors.New("421")}
	if _, err := (&Dispatcher{Contacts: contacts{details: &purserpb.BillingDetails{Email: "b@example.com"}}, Mailer: m}).Dispatch(context.Background(), disabled); err == nil {
		t.Fatal("an SMTP failure completed the notification")
	}
	if _, err := (&Dispatcher{Contacts: contacts{err: errors.New("purser down")}, Mailer: &mailer{}}).Dispatch(context.Background(), disabled); err == nil {
		t.Fatal("a Purser failure completed the notification")
	}
	for _, c := range []contacts{{err: status.Error(codes.NotFound, "none")}, {details: &purserpb.BillingDetails{}}} {
		m := &mailer{}
		if _, err := (&Dispatcher{Contacts: c, Mailer: m}).Dispatch(context.Background(), disabled); err != nil || m.to != "" {
			t.Fatalf("no billing contact = %v, sent to %q; want completed without a send", err, m.to)
		}
	}
}
