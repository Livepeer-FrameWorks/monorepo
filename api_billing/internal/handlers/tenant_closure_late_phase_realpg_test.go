//go:build schema_verify

package handlers

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

func TestTenantClosureDoesNotInvoicePhaseOpenedAfterCutoff_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	closedAt := now.Add(-time.Hour)
	// Billing can have advanced while the permanent-close event was delayed.
	// That later phase has no billable interval before the closure cutoff.
	tenant := newPhaseTenant(t, db, "postpaid", now, now.AddDate(0, 1, 0), 0)
	event, err := events.New("commodore", tenant.id, tenant.id, &internalv1.TenantAccountClosed{}, events.WithTime(closedAt))
	if err != nil {
		t.Fatal(err)
	}
	key, headers, body, err := events.EncodeRecord(event.Envelope())
	if err != nil {
		t.Fatal(err)
	}
	msg := kafka.Message{Key: key, Value: body, Topic: topology.TopicDomainEvents, Headers: map[string]string{}}
	for _, h := range headers {
		msg.Headers[h.Key] = string(h.Value)
	}
	if err := tenant.jobs.handleBillingMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if err := tenant.svc.closeTenantBilling(t.Context(), tenant.id); err != nil {
		t.Fatal(err)
	}
	var status string
	var start, end time.Time
	if err := db.QueryRow(`SELECT status,billing_period_start,billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&status, &start, &end); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || start.After(end) || !end.Equal(closedAt) {
		t.Fatalf("late closure left an invalid billing phase: %s %s..%s, cutoff %s", status, start, end, closedAt)
	}
	var invoices int
	if err := db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1`, tenant.id).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	if invoices != 0 {
		t.Fatalf("charged a phase opened after closure: %d documents", invoices)
	}
}
