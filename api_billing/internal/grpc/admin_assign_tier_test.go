package grpc

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAdminAssignTierRefusesIncompleteRequestsBeforeTheDatabase(t *testing.T) {
	const tenant = "7b000000-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		name string
		req  *purserpb.AdminAssignTierRequest
		want string
	}{
		{"missing tenant", &purserpb.AdminAssignTierRequest{TierName: "production", Reason: "r"}, "tenant_id is required"},
		{"malformed tenant", &purserpb.AdminAssignTierRequest{TenantId: "acme", TierName: "production", Reason: "r"}, "tenant_id must be a UUID"},
		{"missing tier", &purserpb.AdminAssignTierRequest{TenantId: tenant, Reason: "r"}, "tier_name is required"},
		{"missing reason", &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "production", Reason: "  "}, "reason is required"},
		{"unknown model", &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "production", BillingModel: "monthly", Reason: "r"}, `billing_model must be "prepaid" or "postpaid"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			server := &PurserServer{db: db, logger: logging.NewLogger()}
			_, err = server.AdminAssignTier(context.Background(), tc.req)
			if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), tc.want) {
				t.Fatalf("err = %v, want InvalidArgument containing %q", err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("refused request reached the database: %v", err)
			}
		})
	}
}

func TestOperatorTierBillingModel(t *testing.T) {
	for _, tc := range []struct {
		name           string
		level          int32
		defaultPrepaid bool
		requested      string
		want           string
		refusal        string
	}{
		{name: "payg defaults to prepaid", level: 0, defaultPrepaid: true, want: "prepaid"},
		{name: "payg prepaid", level: 0, defaultPrepaid: true, requested: "prepaid", want: "prepaid"},
		{name: "payg refuses postpaid", level: 0, defaultPrepaid: true, requested: "postpaid", refusal: "runs prepaid"},
		{name: "level-zero tier is prepaid", level: 0, want: "prepaid"},
		{name: "default-prepaid flag wins over level", level: 2, defaultPrepaid: true, want: "prepaid"},
		{name: "free defaults to postpaid", level: 1, want: "postpaid"},
		{name: "paid tier postpaid", level: 4, requested: "postpaid", want: "postpaid"},
		{name: "paid tier refuses prepaid", level: 4, requested: "prepaid", refusal: "runs postpaid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := operatorTierBillingModel("tier", tc.level, tc.defaultPrepaid, tc.requested)
			if tc.refusal != "" {
				if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), tc.refusal) {
					t.Fatalf("err = %v, want FailedPrecondition containing %q", err, tc.refusal)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("model = %q, err %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestAdminAssignTierAdmitsOnlyServiceAndPlatformOperator(t *testing.T) {
	interceptor := billingMutationAuthorizationInterceptor()
	method := &grpcpkg.UnaryServerInfo{FullMethod: purserpb.PrepaidService_AdminAssignTier_FullMethodName}
	req := &purserpb.AdminAssignTierRequest{TenantId: "tenant-1"}
	operator := context.WithValue(billingActorContext("jwt", "ops-tenant", "member"), ctxkeys.KeyPlatformOperator, true)
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want codes.Code
	}{
		{"tenant owner", billingActorContext("jwt", "tenant-1", "owner"), codes.PermissionDenied},
		{"scoped api token", delegatedBillingContext("tenant-1", "billing:write"), codes.PermissionDenied},
		{"platform operator", operator, codes.OK},
		{"service", context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service"), codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := interceptor(tc.ctx, req, method, func(context.Context, any) (any, error) { return nil, nil })
			if status.Code(err) != tc.want {
				t.Fatalf("status = %v, want %v", status.Code(err), tc.want)
			}
		})
	}
	apiToken := apiTokenAuthorizationInterceptor()
	_, err := apiToken(delegatedBillingContext("tenant-1", "billing:write"), req, method,
		func(context.Context, any) (any, error) { t.Fatal("handler called"); return nil, nil })
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("API token status = %v, want PermissionDenied", status.Code(err))
	}
}
