//go:build schema_verify

package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// The month-end email of a prepaid tenant is its statement: it itemizes the
// period's usage, states what the balance paid, and asks for no payment.
func TestPrepaidStatementEmailReadsAsAStatement_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	periodStart, periodEnd := prepaidStatementPeriod()
	seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))
	tenantID := seedPrepaidTenant(t, db, periodStart, periodEnd, 1000)
	logger := logging.NewLogger()
	jm := &JobManager{db: db, logger: logger, billing: &Service{db: db, logger: logger}}
	summary := recordEgress(t, db, tenantID, strings.Repeat("b", 64), periodStart.Add(3*time.Hour), 150)
	if err := jm.processPrepaidUsage(ctx, summary, []canonicalUsageDelta{{usageType: "egress_gb", usageValue: 150}}); err != nil {
		t.Fatalf("processPrepaidUsage: %v", err)
	}
	// The deduction was posted when the report arrived, inside the period.
	if _, err := db.ExecContext(ctx, `UPDATE purser.balance_transactions SET created_at = $2 WHERE tenant_id = $1 AND reference_type = 'usage_summary'`,
		tenantID, periodStart.Add(3*time.Hour+5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	jm.generateMonthlyInvoices(ctx)

	var statementID string
	if err := db.QueryRowContext(ctx, `
		SELECT invoice_id::text FROM purser.invoice_email_outbox
		WHERE tenant_id = $1 AND notification_type = 'prepaid_statement'`, tenantID).Scan(&statementID); err != nil {
		t.Fatalf("read statement email: %v", err)
	}
	var sent EmailPrepaidStatement
	var sentLines []EmailInvoiceLineItem
	dispatcher := &invoiceEmailDispatcher{
		jobs: jm,
		send: func(string, string, float64, float64, float64, string, time.Time, []EmailInvoiceLineItem, EmailFX) error {
			t.Fatal("a prepaid statement was sent as an invoice")
			return nil
		},
		sendStatement: func(recipient string, statement EmailPrepaidStatement, lines []EmailInvoiceLineItem) error {
			if recipient != "billing@example.com" {
				t.Fatalf("statement recipient = %s", recipient)
			}
			sent, sentLines = statement, lines
			return nil
		},
	}
	if _, err := dispatcher.Dispatch(ctx, invoiceEmailPayload{
		InvoiceID: statementID, TenantID: tenantID, Recipient: "billing@example.com", NotificationType: prepaidStatementNotification,
	}); err != nil {
		t.Fatalf("dispatch statement email: %v", err)
	}
	if !strings.HasPrefix(sent.Number, "STM-") || sent.RatedUsage != "1.50" || sent.PaidFromBalance != "1.50" ||
		sent.PeriodEndBalance != "8.50" || sent.HasPeriodFees || !sent.PeriodStart.Equal(periodStart) || !sent.PeriodEnd.Equal(periodEnd) {
		t.Fatalf("statement email = %+v", sent)
	}
	var egress bool
	for _, line := range sentLines {
		egress = egress || (strings.HasPrefix(line.Quantity, "150") && line.Total == "1.50")
	}
	if !egress {
		t.Fatalf("statement email lines = %+v, want the 150 GiB egress line at 1.50", sentLines)
	}

	body, err := NewEmailService(logger).renderTemplate("prepaid_statement", EmailData{
		InvoiceID: sent.Number, Currency: "EUR", LoginURL: billingPageURL(),
		LineItems: sentLines, LineItemGroups: groupEmailLineItems(sentLines), Statement: sent,
	})
	if err != nil {
		t.Fatalf("render statement email: %v", err)
	}
	for _, want := range []string{"It is not a bill", "nothing to pay", "Paid from your prepaid balance for this usage", "1.50 EUR", sent.Number} {
		if !strings.Contains(body, want) {
			t.Errorf("statement email lacks %q", want)
		}
	}
	for _, unwanted := range []string{"View and pay invoice", "Due date", "pay the outstanding balance"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("statement email reads as a bill: contains %q", unwanted)
		}
	}
}
