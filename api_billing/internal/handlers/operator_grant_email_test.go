package handlers

import (
	"strings"
	"testing"
	"time"
)

func TestOperatorGrantNoticesRender(t *testing.T) {
	ends := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	es := &EmailService{}
	for _, c := range []struct {
		kind, wantTemplate, wantSubject string
		wantBody                        []string
	}{
		{OperatorGrantNoticeEnding, "operator_grant_ending", "Your FrameWorks arrangement ends on December 1, 2026",
			[]string{"ends on December 1, 2026", "the Developer plan at its regular price"}},
		{OperatorGrantNoticeEnded, "operator_grant_ended", "Your FrameWorks arrangement has ended",
			[]string{"ended on December 1, 2026", "now on the Developer plan"}},
	} {
		notice := OperatorGrantNotice{Recipient: "billing@example.com", TenantName: "Acme", Kind: c.kind, EndsAt: ends, PlanName: "Developer"}
		templateName, subject, err := operatorGrantTemplate(notice)
		if err != nil || templateName != c.wantTemplate || subject != c.wantSubject {
			t.Fatalf("%s: template=%q subject=%q err=%v", c.kind, templateName, subject, err)
		}
		body, err := es.renderTemplate(templateName, EmailData{TenantName: notice.TenantName, DueDate: ends, PlanName: notice.PlanName, LoginURL: billingPageURL()})
		if err != nil {
			t.Fatalf("%s: render: %v", c.kind, err)
		}
		for _, want := range append(c.wantBody, "Hello Acme") {
			if !strings.Contains(body, want) {
				t.Errorf("%s body missing %q", c.kind, want)
			}
		}
	}
	if _, _, err := operatorGrantTemplate(OperatorGrantNotice{Kind: "expired"}); err == nil {
		t.Fatal("an unknown notice kind rendered")
	}
}

// An unconfigured mail service fails the send, so the notice stays queued.
func TestOperatorGrantNoticeNeedsMail(t *testing.T) {
	if err := (&Service{}).SendOperatorGrantNotice(OperatorGrantNotice{Kind: OperatorGrantNoticeEnded}); err == nil {
		t.Fatal("send without a mail service succeeded")
	}
}
