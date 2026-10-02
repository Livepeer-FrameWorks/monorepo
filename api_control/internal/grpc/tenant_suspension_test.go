package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

const suspensionTestTenant = "0dd30dec-a860-4d69-9f46-fc95251a556c"

func purserAdmissionUnavailable() *purserEntitlementFake {
	return &purserEntitlementFake{
		admission: func(context.Context, *purserpb.GetTenantAdmissionStatusRequest) (*purserpb.GetTenantAdmissionStatusResponse, error) {
			return nil, status.Error(codes.Internal, "database error: timeout: context deadline exceeded")
		},
	}
}

func expectCurrentTenantAuthority(t *testing.T, mock sqlmock.Sqlmock, payload *mediaauthoritypb.TenantAuthority, validUntil time.Time) {
	t.Helper()
	encoded, err := proto.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal tenant authority: %v", err)
	}
	mock.ExpectQuery("GetCurrentMediaAuthorityPayload").
		WithArgs("tenant", suspensionTestTenant).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(encoded, validUntil))
}

func tenantAuthorityWithDecision(decision mediaauthoritypb.TenantBillingDecision) *mediaauthoritypb.TenantAuthority {
	return &mediaauthoritypb.TenantAuthority{
		TenantId:        suspensionTestTenant,
		Lifecycle:       mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision: decision,
	}
}

func TestIsTenantSuspendedUsesPurserAnswer(t *testing.T) {
	for _, suspended := range []bool{true, false} {
		fake := &purserEntitlementFake{
			admission: func(context.Context, *purserpb.GetTenantAdmissionStatusRequest) (*purserpb.GetTenantAdmissionStatusResponse, error) {
				return &purserpb.GetTenantAdmissionStatusResponse{IsSuspended: suspended}, nil
			},
		}
		s := &CommodoreServer{logger: logrus.New(), purserClient: startPurserEntitlementFake(t, fake)}
		got, err := s.isTenantSuspended(context.Background(), suspensionTestTenant)
		if err != nil || got != suspended {
			t.Fatalf("Purser says suspended=%v: got (%v, %v)", suspended, got, err)
		}
	}
}

// An unanswered Purser call is not a "not suspended" answer. The tenant's
// current media authority carries the billing decision Purser last published,
// so it decides while it is valid.
func TestIsTenantSuspendedFallsBackToCurrentTenantAuthority(t *testing.T) {
	cases := []struct {
		name     string
		decision mediaauthoritypb.TenantBillingDecision
		want     bool
	}{
		{name: "suspended", decision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED, want: true},
		{name: "allowed", decision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW, want: false},
		{name: "payment required is not suspension", decision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			expectCurrentTenantAuthority(t, mock, tenantAuthorityWithDecision(tc.decision), time.Now().Add(time.Hour))

			s := &CommodoreServer{logger: logrus.New(), db: db, purserClient: startPurserEntitlementFake(t, purserAdmissionUnavailable())}
			got, err := s.isTenantSuspended(context.Background(), suspensionTestTenant)
			if err != nil {
				t.Fatalf("valid tenant authority should decide while Purser is unavailable, got %v", err)
			}
			if got != tc.want {
				t.Fatalf("suspended = %v, want %v", got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIsTenantSuspendedIsUnavailableWithoutValidLastKnownState(t *testing.T) {
	cases := []struct {
		name   string
		expect func(sqlmock.Sqlmock)
	}{
		{name: "no tenant authority", expect: func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery("GetCurrentMediaAuthorityPayload").
				WithArgs("tenant", suspensionTestTenant).
				WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}))
		}},
		{name: "expired tenant authority", expect: func(mock sqlmock.Sqlmock) {
			encoded, err := proto.Marshal(tenantAuthorityWithDecision(mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			mock.ExpectQuery("GetCurrentMediaAuthorityPayload").
				WithArgs("tenant", suspensionTestTenant).
				WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(encoded, time.Now().Add(-time.Minute)))
		}},
		{name: "inactive tenant authority", expect: func(mock sqlmock.Sqlmock) {
			payload := tenantAuthorityWithDecision(mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_INACTIVE)
			payload.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
			encoded, err := proto.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			mock.ExpectQuery("GetCurrentMediaAuthorityPayload").
				WithArgs("tenant", suspensionTestTenant).
				WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(encoded, time.Now().Add(time.Hour)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			tc.expect(mock)

			s := &CommodoreServer{logger: logrus.New(), db: db, purserClient: startPurserEntitlementFake(t, purserAdmissionUnavailable())}
			got, err := s.isTenantSuspended(context.Background(), suspensionTestTenant)
			if status.Code(err) != codes.Unavailable {
				t.Fatalf("want Unavailable without a valid last-known decision, got (%v, %v)", got, err)
			}
		})
	}
}

// Creating rated work while the suspension decision is unknown must be refused
// as retryable, not admitted as if the tenant were in good standing.
func TestCreateClipRefusesWhenSuspensionIsUnknown(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("GetCurrentMediaAuthorityPayload").
		WithArgs("tenant", suspensionTestTenant).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}))

	s := &CommodoreServer{logger: logrus.New(), db: db, purserClient: startPurserEntitlementFake(t, purserAdmissionUnavailable())}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, suspensionTestTenant)

	_, err = s.CreateClip(ctx, &sharedpb.CreateClipRequest{StreamId: proto.String("stream-1")})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("CreateClip with unknown suspension: want Unavailable, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
