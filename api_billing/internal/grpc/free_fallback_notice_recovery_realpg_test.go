//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func testAbandonedFallbackNoticeRetry(t *testing.T) {
	for _, moved := range []bool{false, true} {
		name := "grace"
		if moved {
			name = "completed move"
		}
		t.Run("operator resends abandoned "+name+" notice", func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			tenant, _, _ := seedFallbackLifecycle(t, db)
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_email='broken@example.test',free_fallback_notified_at=NULL WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			server := fallbackLifecycleServer(db, &fallbackLifecycleProvider{call: func(context.Context, billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
				return billing.SubscriptionEndResult{}, nil
			}})
			if moved {
				if err := server.moveTenantToFree(t.Context(), tenant); err != nil {
					t.Fatal(err)
				}
			}
			server.freeFallbackNoticeSender = func(handlers.FreeFallbackNotice) error { return errors.New("recipient rejected") }
			for i := 0; i < 5; i++ {
				if err := server.deliverFreeFallbackNotice(t.Context(), tenant); err == nil {
					t.Fatal("rejected notice was acknowledged")
				}
				if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET free_fallback_notice_attempted_at=NOW()-INTERVAL '2 days' WHERE tenant_id=$1`, tenant); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_email='repaired@example.test' WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			calls := 0
			server.freeFallbackNoticeSender = func(n handlers.FreeFallbackNotice) error {
				calls++
				if n.Recipient != "repaired@example.test" || n.Moved != moved {
					t.Errorf("retried the wrong notification: %+v", n)
				}
				return nil
			}
			actor := uuid.NewString()
			listener := bufconn.Listen(1 << 20)
			wire := grpcgo.NewServer(grpcgo.UnaryInterceptor(func(_ context.Context, req any, _ *grpcgo.UnaryServerInfo, next grpcgo.UnaryHandler) (any, error) {
				return next(operatorAssignCtx(actor), req)
			}))
			purserpb.RegisterPrepaidServiceServer(wire, server)
			serveErrors := make(chan error, 1)
			go func() { serveErrors <- wire.Serve(listener) }()
			t.Cleanup(func() {
				wire.Stop()
				if err := <-serveErrors; err != nil && !errors.Is(err, grpcgo.ErrServerStopped) {
					t.Error(err)
				}
			})
			conn, err := grpcgo.NewClient("passthrough:///fallback-notice", grpcgo.WithTransportCredentials(insecure.NewCredentials()), grpcgo.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := conn.Close(); err != nil {
					t.Error(err)
				}
			})
			client := purserpb.NewPrepaidServiceClient(conn)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			snapshot := func() string {
				t.Helper()
				var value string
				if err := db.QueryRow(`SELECT to_jsonb(subscription)::text FROM purser.tenant_subscriptions subscription WHERE tenant_id=$1`, tenant).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			before := snapshot()
			preview, err := client.AdminResendFreeFallbackNotice(ctx, &purserpb.AdminResendFreeFallbackNoticeRequest{TenantId: tenant})
			if err != nil || preview.GetStatus() != "notice_abandoned" || !strings.Contains(preview.GetDetail(), "recipient rejected") || calls != 0 || snapshot() != before {
				t.Fatalf("preview did not expose the abandoned notice without mutation: %v %v", preview, err)
			}
			request := &purserpb.AdminResendFreeFallbackNoticeRequest{TenantId: tenant, Confirmed: true, Reason: "billing email repaired", ScheduledAt: timestamppb.New(preview.ScheduledAt.AsTime().Add(time.Second)), Moved: moved}
			if _, err := client.AdminResendFreeFallbackNotice(ctx, request); status.Code(err) != codes.FailedPrecondition || snapshot() != before {
				t.Fatalf("stale confirmation changed notification: %v", err)
			}
			request.ScheduledAt = preview.ScheduledAt
			request.Moved = !moved
			if _, err := client.AdminResendFreeFallbackNotice(ctx, request); status.Code(err) != codes.FailedPrecondition || snapshot() != before {
				t.Fatalf("wrong notice phase changed notification: %v", err)
			}
			request.Moved = moved
			if _, err := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,reason,collection) SELECT id,'temporary grant','invoice' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			if _, err := client.AdminResendFreeFallbackNotice(ctx, &purserpb.AdminResendFreeFallbackNoticeRequest{TenantId: tenant}); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("grant-held notice preview accepted: %v", err)
			}
			if _, err := client.AdminResendFreeFallbackNotice(ctx, request); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "grant") {
				t.Fatalf("granted tenant accepted undeliverable notice: %v", err)
			}
			if _, err := db.Exec(`DELETE FROM purser.subscription_operator_grants WHERE subscription_id=(SELECT id FROM purser.tenant_subscriptions WHERE tenant_id=$1)`, tenant); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				confirmed, err := client.AdminResendFreeFallbackNotice(ctx, request)
				wantStatus := "queued"
				if i > 0 {
					wantStatus = "already_queued"
				}
				if err != nil || confirmed.GetStatus() != wantStatus || calls != 0 {
					t.Fatalf("notice retry did not queue durably: %v %v", confirmed, err)
				}
			}
			var count int
			var eventActor string
			var payload []byte
			if err := db.QueryRow(`SELECT COUNT(*) OVER(),actor_user_id,payload FROM purser.domain_event_outbox WHERE tenant_id=$1 AND actor_user_id=$2 AND event_type='billing.subscription_updated'`, tenant, actor).Scan(&count, &eventActor, &payload); err != nil {
				t.Fatal(err)
			}
			var audit internalv1.SubscriptionUpdated
			if err := proto.Unmarshal(payload, &audit); err != nil {
				t.Fatal(err)
			}
			if count != 1 || eventActor != actor || audit.GetReason() != "billing email repaired" || !slices.Contains(audit.GetChangedFields(), "free_fallback_notice_retry") {
				t.Fatalf("notice retry audit count=%d actor=%s reason=%s", count, eventActor, audit.GetReason())
			}
			server.sweepFreeFallbackNoticesOnce(ctx)
			server.sweepFreeFallbackNoticesOnce(ctx)
			var abandoned sql.NullTime
			var failures int
			if err := db.QueryRow(`SELECT free_fallback_notice_abandoned_at,free_fallback_notice_failures FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&abandoned, &failures); err != nil || abandoned.Valid || failures != 0 || calls != 1 {
				t.Fatalf("abandoned notification was not delivered once: abandoned=%v failures=%d calls=%d err=%v", abandoned, failures, calls, err)
			}
		})
	}
}
