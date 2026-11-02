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

// A sub-minimum balance has no later invoice once the account closes, so the
// closure records its write-off instead of stranding the carry.
func TestTenantClosureWritesOffCollectionCarry_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	tenant := newPhaseTenant(t, db, "postpaid", now.Add(-24*time.Hour), now.AddDate(0, 1, 0), 0)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.billing_collection_balances(tenant_id,currency,balance_cents) VALUES($1,'EUR',321)`, tenant.id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE purser.billing_tiers SET metering_enabled=false WHERE id=$1`, tenant.supporterID); err != nil {
		t.Fatal(err)
	}
	event, err := events.New("commodore", tenant.id, tenant.id, &internalv1.TenantAccountClosed{}, events.WithTime(now.Add(-time.Minute)))
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
	if err = tenant.jobs.handleBillingMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if err = tenant.svc.closeTenantBilling(t.Context(), tenant.id); err != nil {
		t.Fatal(err)
	}
	if err = tenant.svc.closeTenantBilling(t.Context(), tenant.id); err != nil {
		t.Fatal(err)
	}
	var balance, written int64
	var rows int
	var reason string
	if err = db.QueryRowContext(t.Context(), `SELECT
		(SELECT balance_cents FROM purser.billing_collection_balances WHERE tenant_id=$1 AND currency='EUR'),
		(SELECT COALESCE(SUM(amount_cents),0)::bigint FROM purser.billing_collection_writeoffs WHERE tenant_id=$1),
		(SELECT count(*) FROM purser.billing_collection_writeoffs WHERE tenant_id=$1),
		COALESCE((SELECT MAX(reason) FROM purser.billing_collection_writeoffs WHERE tenant_id=$1),'')`, tenant.id).Scan(&balance, &written, &rows, &reason); err != nil {
		t.Fatal(err)
	}
	if balance != 0 || written != 321 || rows != 1 || reason != "account_closed" {
		t.Fatalf("closure carry: balance=%d written=%d rows=%d reason=%q, want balance=0 written=321 rows=1 reason=account_closed", balance, written, rows, reason)
	}
}
