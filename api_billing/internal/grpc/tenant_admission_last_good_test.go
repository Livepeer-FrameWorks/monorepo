package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

const admissionTestTenant = "tenant-1"

// slowAdmissionRead is longer than tenantAdmissionQueryTimeout, the shape of
// the production reads that timed out against YugabyteDB.
const slowAdmissionRead = 400 * time.Millisecond

func admissionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"billing_model", "subscription_status", "balance_cents", "reserved_balance_cents",
		"payment_method", "stripe_subscription_id", "mollie_subscription_id", "tier_name", "tier_level",
		"stripe_customer_id", "has_valid_mollie_mandate", "grant_collection", "grant_waive_usage", "effective_base_price",
	}).AddRow("prepaid", "active", int64(5000), int64(100), nil, nil, nil, "pro", int32(3), nil, false, nil, false, "0.00")
}

func newAdmissionServer(t *testing.T) (*PurserServer, sqlmock.Sqlmock, *logrustest.Hook) {
	t.Helper()
	server, mock := newReadServer(t, true)
	hook := logrustest.NewLocal(server.logger)
	return server, mock, hook
}

func expectAdmissionRead(mock sqlmock.Sqlmock) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(`FROM purser\.tenant_subscriptions ts`).WithArgs("EUR", admissionTestTenant)
}

func admissionRequest() *purserpb.GetTenantAdmissionStatusRequest {
	return &purserpb.GetTenantAdmissionStatusRequest{TenantId: admissionTestTenant}
}

// A read that outlives the query budget must not flip an admitted tenant to
// deny: the tenant's last decision read from the database answers, and the
// fallback is logged with the database error.
func TestGetTenantAdmissionStatusServesLastGoodWhenDatabaseIsSlow(t *testing.T) {
	server, mock, hook := newAdmissionServer(t)
	expectAdmissionRead(mock).WillReturnRows(admissionRows())
	fresh, err := server.GetTenantAdmissionStatus(context.Background(), admissionRequest())
	if err != nil {
		t.Fatalf("fresh admission read: %v", err)
	}

	expectAdmissionRead(mock).WillDelayFor(slowAdmissionRead).WillReturnRows(admissionRows())
	started := time.Now()
	served, err := server.GetTenantAdmissionStatus(context.Background(), admissionRequest())
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("slow admission read returned %v, want the last-good decision", err)
	}
	if elapsed >= slowAdmissionRead {
		t.Fatalf("slow admission read answered after %s, want within the %s query budget", elapsed, tenantAdmissionQueryTimeout)
	}
	if served.BillingModel != fresh.BillingModel || served.IsBalanceNegative || served.AvailableBalanceCents != 4900 || served.TierLevel != 3 {
		t.Fatalf("served decision = %+v, want last-good %+v", served, fresh)
	}

	var logged *logrus.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == "Serving last-good tenant admission status after database error" {
			logged = entry
		}
	}
	if logged == nil {
		t.Fatalf("last-good fallback was not logged; entries: %v", hook.AllEntries())
	}
	if logged.Level != logrus.WarnLevel || logged.Data["tenant_id"] != admissionTestTenant || logged.Data["error"] == nil || logged.Data["decision_age"] == nil {
		t.Fatalf("last-good log entry = %v %v", logged.Level, logged.Data)
	}
}

// Past tenantAdmissionLastGoodTTL the remembered decision is no longer
// authority, so a failing read fails closed again.
func TestGetTenantAdmissionStatusExpiredLastGoodFailsClosed(t *testing.T) {
	server, mock, _ := newAdmissionServer(t)
	now := time.Now()
	server.admissionLastGood.now = func() time.Time { return now }
	expectAdmissionRead(mock).WillReturnRows(admissionRows())
	if _, err := server.GetTenantAdmissionStatus(context.Background(), admissionRequest()); err != nil {
		t.Fatalf("fresh admission read: %v", err)
	}

	now = now.Add(tenantAdmissionLastGoodTTL + time.Second)
	expectAdmissionRead(mock).WillDelayFor(slowAdmissionRead).WillReturnRows(admissionRows())
	if _, err := server.GetTenantAdmissionStatus(context.Background(), admissionRequest()); status.Code(err) != codes.Internal {
		t.Fatalf("expired last-good read error = %v, want Internal", err)
	}
}

// A missing subscription is an authoritative answer: it replaces the last
// decision, so a later database fault cannot re-admit the tenant.
func TestGetTenantAdmissionStatusMissingSubscriptionDropsLastGood(t *testing.T) {
	server, mock, _ := newAdmissionServer(t)
	expectAdmissionRead(mock).WillReturnRows(admissionRows())
	if _, err := server.GetTenantAdmissionStatus(context.Background(), admissionRequest()); err != nil {
		t.Fatalf("fresh admission read: %v", err)
	}
	expectAdmissionRead(mock).WillReturnError(sqlmockNoRows())
	missing, err := server.GetTenantAdmissionStatus(context.Background(), admissionRequest())
	if err != nil || !missing.IsBalanceNegative {
		t.Fatalf("missing subscription = %+v, %v; want fail-closed default", missing, err)
	}

	expectAdmissionRead(mock).WillDelayFor(slowAdmissionRead).WillReturnRows(admissionRows())
	if _, err := server.GetTenantAdmissionStatus(context.Background(), admissionRequest()); status.Code(err) != codes.Internal {
		t.Fatalf("read after missing subscription error = %v, want Internal", err)
	}
}

// A caller whose deadline is shorter than the query budget still receives the
// last-good decision before that deadline.
func TestGetTenantAdmissionStatusAnswersWithinCallerDeadline(t *testing.T) {
	server, mock, _ := newAdmissionServer(t)
	expectAdmissionRead(mock).WillReturnRows(admissionRows())
	if _, err := server.GetTenantAdmissionStatus(context.Background(), admissionRequest()); err != nil {
		t.Fatalf("fresh admission read: %v", err)
	}

	const callerBudget = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), callerBudget)
	defer cancel()
	expectAdmissionRead(mock).WillDelayFor(slowAdmissionRead).WillReturnRows(admissionRows())
	served, err := server.GetTenantAdmissionStatus(ctx, admissionRequest())
	if err != nil {
		t.Fatalf("short-deadline admission read: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("answered after the caller's %s deadline", callerBudget)
	}
	if served.TierLevel != 3 || served.IsBalanceNegative {
		t.Fatalf("served decision = %+v, want last-good", served)
	}
}
