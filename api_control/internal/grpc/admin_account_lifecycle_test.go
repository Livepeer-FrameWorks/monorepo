package grpc

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAccountLifecycleRequiresOperatorJWT(t *testing.T) {
	s := &CommodoreServer{}
	for _, ctx := range []context.Context{context.Background(), context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service"), context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyRole, "owner")} {
		if _, err := s.AdminSetUserActive(ctx, &commodorepb.AdminSetUserActiveRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("user auth=%v", err)
		}
		if _, err := s.AdminListAccountHistory(ctx, &commodorepb.AdminListAccountHistoryRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("history auth=%v", err)
		}
		if _, err := s.AdminDeactivateTenant(ctx, &commodorepb.AdminDeactivateTenantRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("tenant auth=%v", err)
		}
	}
}

func TestSystemTenantClosureProtectsUUIDSpellings(t *testing.T) {
	id := uuid.MustParse("abcdef01-abcd-4abc-8abc-abcdefabcdef")
	server := &CommodoreServer{systemTenantID: id}
	ctx := context.WithValue(ctxAsOperator(uuid.NewString(), id.String(), "owner"), ctxkeys.KeyAuthType, "jwt")
	for _, spelling := range []string{strings.ToUpper(id.String()), strings.ReplaceAll(id.String(), "-", "")} {
		if _, err := server.AdminDeactivateTenant(ctx, &commodorepb.AdminDeactivateTenantRequest{TenantId: spelling, TenantName: "System", Reason: "must refuse"}); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("%s: %v", spelling, err)
		}
	}
}

func TestAccountHistoryValidatesPagination(t *testing.T) {
	tenant := uuid.NewString()
	ctx := context.WithValue(ctxAsOperator(uuid.NewString(), tenant, "owner"), ctxkeys.KeyAuthType, "jwt")
	for _, request := range []*commodorepb.AdminListAccountHistoryRequest{
		{TenantId: tenant, Limit: 501},
		{TenantId: tenant, Limit: -1},
		{TenantId: tenant, Before: "invalid"},
	} {
		if _, err := (&CommodoreServer{}).AdminListAccountHistory(ctx, request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("request=%v error=%v, want InvalidArgument", request, err)
		}
	}
}

func TestClosureFailedReadRetainsLastCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_closure_failed"}, nil)
	gauge.WithLabelValues().Set(100)
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_closure_count_read_failures"}, nil)
	mock.ExpectQuery("ClaimPendingTenantAccountClosures").WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}))
	mock.ExpectQuery("CountFailedTenantAccountClosures").WillReturnError(errors.New("database unavailable"))
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), metrics: &ServerMetrics{TenantAccountClosureFailures: gauge, TenantAccountClosureReadFailures: counter}}
	server.retryTenantAccountClosuresOnce(context.Background())
	if got := testutil.ToFloat64(gauge.WithLabelValues()); got != 100 {
		t.Fatalf("count reset to %v on failed read", got)
	}
	if got := testutil.ToFloat64(counter.WithLabelValues()); got != 1 {
		t.Fatalf("read failure counter=%v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
