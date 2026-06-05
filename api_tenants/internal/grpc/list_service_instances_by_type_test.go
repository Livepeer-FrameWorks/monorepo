package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A non-service caller (tenant/user JWT, or no auth_type) must be denied before
// any physical inventory is returned.
func TestListServiceInstancesByType_DeniesNonServiceCallers(t *testing.T) {
	s := &QuartermasterServer{}
	for _, ctx := range []context.Context{
		context.Background(), // no auth_type
		context.WithValue(context.Background(), ctxkeys.KeyAuthType, "user"),
		context.WithValue(context.Background(), ctxkeys.KeyAuthType, "tenant"),
	} {
		_, err := s.ListServiceInstancesByType(ctx, &quartermasterpb.ListServiceInstancesByTypeRequest{
			ServiceType: "livepeer-gateway",
		})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for non-service caller, got %v", err)
		}
	}
}

// A service caller is admitted past the auth gate, then rejected for a service
// type that has no physical-endpoint contract.
func TestListServiceInstancesByType_RejectsNonPhysicalServiceType(t *testing.T) {
	s := &QuartermasterServer{}
	_, err := s.ListServiceInstancesByType(serviceCtx(), &quartermasterpb.ListServiceInstancesByTypeRequest{
		ServiceType: "chandler",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for non-physical service type, got %v", err)
	}
}

func TestListServiceInstancesByType_RequiresServiceType(t *testing.T) {
	s := &QuartermasterServer{}
	_, err := s.ListServiceInstancesByType(serviceCtx(), &quartermasterpb.ListServiceInstancesByTypeRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for empty service_type, got %v", err)
	}
}

// A per-row scan error must fail closed (Internal), not skip the row into a
// truncated physical inventory that Navigator could prune against.
func TestListServiceInstancesByType_FailsClosedOnScanError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	s := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	cols := []string{"instance_id", "service_id", "cluster_id", "node_id", "external_ip", "status", "health_status", "port", "protocol"}
	mock.ExpectQuery(`FROM quartermaster\.service_instances si`).
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("inst-1", "livepeer-gateway", "media-eu-1", "core-eu-1", "203.0.113.10", "running", "healthy", "NOT_AN_INT", "http"))

	_, err = s.ListServiceInstancesByType(serviceCtx(), &quartermasterpb.ListServiceInstancesByTypeRequest{
		ServiceType: "livepeer-gateway",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal on scan error, got %v", err)
	}
}
