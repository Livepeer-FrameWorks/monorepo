//go:build schema_verify

package handlers

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

func TestTenantClosureCancelsLateClusterSubscription_RealPG(t *testing.T) {
	for _, tc := range []struct {
		moment string
		active bool
	}{
		{"after finalization", true}, {"during cancellation", true},
		{"after finalization", false}, {"during cancellation", false},
	} {
		t.Run(fmt.Sprintf("%s/active=%t", tc.moment, tc.active), func(t *testing.T) {
			moment := tc.moment
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC().Truncate(time.Microsecond)
			tenant := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
			if _, err := db.ExecContext(t.Context(), `UPDATE purser.billing_tiers SET metering_enabled=false WHERE id IN ($1,$2)`, tenant.supporterID, tenant.paygID); err != nil {
				t.Fatal(err)
			}
			fixture, client := installStripeFixture(t)
			tenant.svc.stripeClient = client
			states := map[string]string{"existing-cluster": "active", "late-cluster": "active"}
			if !tc.active {
				states["late-cluster"] = "incomplete"
			}
			var stateMu sync.Mutex
			getState := func(id string) string {
				stateMu.Lock()
				defer stateMu.Unlock()
				return states[id]
			}
			late := func() error {
				if !tc.active {
					return tenant.svc.stageClusterSubscriptionPending(t.Context(), "", tenant.id, "marketplace", "customer", "late-cluster")
				}
				return tenant.svc.activateClusterSubscriptionFromStripe(t.Context(), tenant.id, "marketplace", "customer", "late-cluster", "")
			}
			deliverDuringCancellation := moment == "during cancellation"
			for id := range states {
				path := "/v1/subscriptions/" + id
				fixture.route("GET", path, func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": id, "status": getState(id)}
				})
				fixture.route("DELETE", path, func(recordedProviderRequest) (int, any) {
					if id == "existing-cluster" && deliverDuringCancellation {
						deliverDuringCancellation = false
						if err := late(); err != nil {
							return 503, map[string]any{"error": map[string]any{"message": fmt.Sprintf("late callback: %v", err), "type": "api_error"}}
						}
					}
					stateMu.Lock()
					states[id] = "canceled"
					stateMu.Unlock()
					return 200, map[string]any{"id": id, "status": "canceled"}
				})
			}
			if !tc.active {
				if err := tenant.svc.stageClusterSubscriptionPending(t.Context(), "", tenant.id, "marketplace", "customer", "existing-cluster"); err != nil {
					t.Fatal(err)
				}
				var state string
				if err := db.QueryRowContext(t.Context(), `SELECT status FROM purser.cluster_subscriptions WHERE tenant_id=$1 AND cluster_id='marketplace'`, tenant.id).Scan(&state); err != nil || state != "pending_payment" {
					t.Fatalf("positive control did not stage the live account's cluster: %s %v", state, err)
				}
			}
			if err := tenant.svc.activateClusterSubscriptionFromStripe(t.Context(), tenant.id, "marketplace", "customer", "existing-cluster", ""); err != nil {
				t.Fatal(err)
			}
			var state string
			if err := db.QueryRowContext(t.Context(), `SELECT status FROM purser.cluster_subscriptions WHERE tenant_id=$1 AND cluster_id='marketplace'`, tenant.id).Scan(&state); err != nil || state != "active" {
				t.Fatalf("positive control did not activate the live account's cluster: %s %v", state, err)
			}
			event, err := events.New("commodore", tenant.id, tenant.id, &internalv1.TenantAccountClosed{}, events.WithTime(now))
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
			worker := &outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: tenant.svc}, Logger: tenant.jobs.logger}
			worker.ProcessBatch(t.Context())
			if moment == "after finalization" {
				closed, err := purserdb.New(db).GetTenantBillingClosure(t.Context(), tenant.id)
				if err != nil || !closed.FinalizedAt.Valid {
					t.Fatalf("positive control did not finalize closure: %+v %v", closed, err)
				}
				if err := late(); err != nil {
					t.Fatal(err)
				}
			}
			var installed string
			if err := db.QueryRowContext(t.Context(), `SELECT status,stripe_subscription_id FROM purser.cluster_subscriptions WHERE tenant_id=$1 AND cluster_id='marketplace'`, tenant.id).Scan(&state, &installed); err != nil || installed == "late-cluster" || moment == "after finalization" && state != "cancelled" {
				t.Fatalf("late cluster checkout reactivated a closed account: %s %s %v", state, installed, err)
			}
			for range 3 {
				if _, err := db.ExecContext(t.Context(), `UPDATE purser.provider_webhook_inbox SET next_attempt_at=NOW() WHERE provider='tenant_closure' AND headers->>'tenant_id'=$1`, tenant.id); err != nil {
					t.Fatal(err)
				}
				worker.ProcessBatch(t.Context())
			}
			if getState("late-cluster") != "canceled" || len(fixture.matching("DELETE", "/v1/subscriptions/late-cluster")) != 1 {
				t.Fatalf("closed account's late cluster still charges: state=%s cancellations=%d", getState("late-cluster"), len(fixture.matching("DELETE", "/v1/subscriptions/late-cluster")))
			}
			closed, err := purserdb.New(db).GetTenantBillingClosure(t.Context(), tenant.id)
			if err != nil || !closed.FinalizedAt.Valid || !closed.ProvidersCancelledAt.Valid {
				t.Fatalf("late provider did not converge: %+v %v", closed, err)
			}
			var documents int
			if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1`, tenant.id).Scan(&documents); err != nil || documents != 1 {
				t.Fatalf("late callback repeated final billing: documents=%d %v", documents, err)
			}
		})
	}
}
