//go:build schema_verify

package grpc

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// grantNoticeFixture is one tenant on a granted tier whose grant falls back
// to another tier, with a sender that records what reaches its billing email.
type grantNoticeFixture struct {
	db       *sql.DB
	server   *PurserServer
	tenantID string
	email    string
	sent     []handlers.OperatorGrantNotice
	failSend bool
}

func newGrantNoticeFixture(t *testing.T, status string, expiresIn time.Duration) *grantNoticeFixture {
	t.Helper()
	db := startOperatorGrantDatabase(t)
	f := &grantNoticeFixture{db: db, tenantID: uuid.NewString()}
	f.email = "grant-notice-" + f.tenantID[:8] + "@example.com"
	fallbackID, grantedID := uuid.NewString(), uuid.NewString()
	f.exec(t, `INSERT INTO purser.billing_tiers
		(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,is_active)
		VALUES ($1::text::uuid,'notice-fallback-'||$1::text,'Notice Fallback',0,0,'EUR',false,true),
		       ($2::text::uuid,'notice-granted-'||$2::text,'Notice Granted',4,0,'EUR',false,true)`,
		fallbackID, grantedID)
	f.exec(t, `INSERT INTO purser.tenant_subscriptions
		(tenant_id,tier_id,billing_model,status,billing_email,billing_name,billing_period_start,billing_period_end,presentment_currency)
		VALUES ($1,$2,'prepaid',$3,$4,'Acme',NOW()-INTERVAL '1 day',NOW()+INTERVAL '29 days','EUR')`, f.tenantID, grantedID, status, f.email)
	f.exec(t, `INSERT INTO purser.subscription_operator_grants
		(subscription_id,waive_usage,collection,reason,granted_by,granted_at,expires_at,assigned_tier_id,fallback_tier_id)
		SELECT id,true,'provider','trial','operator',NOW()-INTERVAL '12 hours',date_trunc('second',NOW())+$2*INTERVAL '1 second',$3,$4
		FROM purser.tenant_subscriptions WHERE tenant_id=$1`, f.tenantID, int64(expiresIn/time.Second), grantedID, fallbackID)
	f.server = &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{},
		operatorGrantNoticeSender: func(n handlers.OperatorGrantNotice) error {
			if n.Recipient != f.email {
				return nil
			}
			if f.failSend {
				return errors.New("550 mailbox unavailable")
			}
			f.sent = append(f.sent, n)
			return nil
		}}
	return f
}

func (f *grantNoticeFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
}

func (f *grantNoticeFixture) expiresAt(t *testing.T) time.Time {
	t.Helper()
	var at time.Time
	if err := f.db.QueryRowContext(t.Context(), `SELECT g.expires_at FROM purser.subscription_operator_grants g
		JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE s.tenant_id=$1`, f.tenantID).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func (f *grantNoticeFixture) queued(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.operator_grant_notices WHERE tenant_id=$1`, f.tenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The expiry sweep, the main trial-end path, queues one ended notice for
// the grant's own end naming the tier the account falls back to.
func testOperatorGrantEndedNoticeOnExpiry(t *testing.T) {
	f := newGrantNoticeFixture(t, "active", -time.Minute)
	ends := f.expiresAt(t)
	f.server.expireOperatorGrantsOnce(t.Context())
	f.server.sweepOperatorGrantNoticesOnce(t.Context())
	if len(f.sent) != 1 || f.sent[0].Kind != handlers.OperatorGrantNoticeEnded || !f.sent[0].EndsAt.Equal(ends) || f.sent[0].PlanName != "Notice Fallback" {
		t.Fatalf("notices after expiry = %+v, want one ended notice for %s naming Notice Fallback", f.sent, ends)
	}
}

// A closed account is told neither that its grant ends nor that it ended.
func testOperatorGrantNoticesSkipClosedAccounts(t *testing.T) {
	f := newGrantNoticeFixture(t, "cancelled", 3*24*time.Hour)
	f.server.sweepOperatorGrantNoticesOnce(t.Context())
	if n := f.queued(t); n != 0 {
		t.Fatalf("%d ending notices queued for a closed account", n)
	}
	f.exec(t, `UPDATE purser.subscription_operator_grants g SET expires_at=NOW()-INTERVAL '1 minute'
		FROM purser.tenant_subscriptions s WHERE s.id=g.subscription_id AND s.tenant_id=$1`, f.tenantID)
	f.server.expireOperatorGrantsOnce(t.Context())
	var grants int
	if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.subscription_operator_grants g
		JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE s.tenant_id=$1`, f.tenantID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("closed account grant not ended: rows=%d err=%v", grants, err)
	}
	f.server.sweepOperatorGrantNoticesOnce(t.Context())
	if n := f.queued(t); n != 0 || len(f.sent) != 0 {
		t.Fatalf("closed account got notices: queued=%d sent=%+v", n, f.sent)
	}
}

// A notice abandoned after five failed sends leaves the repeated-failure
// gauge, so the alert on it can clear, and the operator requeue sends it.
func testOperatorGrantNoticeAbandonAndRequeue(t *testing.T) {
	f := newGrantNoticeFixture(t, "active", 3*24*time.Hour)
	ctx := t.Context()
	count := func() int64 {
		t.Helper()
		n, err := purserdb.New(f.db).CountRepeatedOperatorGrantNoticeFailures(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	baseline := count()
	f.failSend = true
	for attempt := 1; attempt <= 5; attempt++ {
		f.server.sweepOperatorGrantNoticesOnce(ctx)
		if attempt == 2 {
			if got := count(); got != baseline+1 {
				t.Fatalf("after two failures the gauge counts %d, want %d", got, baseline+1)
			}
		}
		f.exec(t, `UPDATE purser.operator_grant_notices SET attempted_at=NOW()-INTERVAL '1 day' WHERE tenant_id=$1`, f.tenantID)
	}
	var abandoned bool
	if err := f.db.QueryRowContext(ctx, `SELECT abandoned_at IS NOT NULL FROM purser.operator_grant_notices WHERE tenant_id=$1`, f.tenantID).Scan(&abandoned); err != nil || !abandoned {
		t.Fatalf("notice not abandoned after five failures: %v", err)
	}
	if got := count(); got != baseline {
		t.Fatalf("an abandoned notice still counts as repeatedly failing: %d, want %d", got, baseline)
	}

	op := operatorAssignCtx(uuid.NewString())
	preview, err := f.server.AdminResendOperatorGrantNotice(op, &purserpb.AdminResendOperatorGrantNoticeRequest{TenantId: f.tenantID})
	if err != nil {
		t.Fatal(err)
	}
	if preview.GetStatus() != "notice_abandoned" || preview.GetKind() != handlers.OperatorGrantNoticeEnding ||
		!preview.GetEndsAt().AsTime().Equal(f.expiresAt(t)) || preview.GetDetail() != "550 mailbox unavailable" {
		t.Fatalf("preview = %+v", preview)
	}
	confirm := &purserpb.AdminResendOperatorGrantNoticeRequest{
		TenantId: f.tenantID, Confirmed: true, Reason: "mailbox repaired", Kind: preview.GetKind(), EndsAt: preview.GetEndsAt(),
	}
	for _, want := range []string{"queued", "already_queued"} {
		out, confirmErr := f.server.AdminResendOperatorGrantNotice(op, confirm)
		if confirmErr != nil || out.GetStatus() != want {
			t.Fatalf("confirmation = %+v, %v; want %s", out, confirmErr, want)
		}
	}
	if _, err = f.server.AdminResendOperatorGrantNotice(op, &purserpb.AdminResendOperatorGrantNoticeRequest{TenantId: f.tenantID}); status.Code(err) != codes.NotFound {
		t.Fatalf("preview after requeue: %v, want NotFound", err)
	}
	f.failSend = false
	f.server.sweepOperatorGrantNoticesOnce(ctx)
	if len(f.sent) != 1 || f.sent[0].Kind != handlers.OperatorGrantNoticeEnding {
		t.Fatalf("requeued notice sends = %+v", f.sent)
	}
}
